package main

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/go-github/v47/github"
	"golang.org/x/oauth2"
)

var (
	//go:embed template/license.tpl
	licenseRaw string
	licenseTpl = template.Must(template.New("license").Parse(licenseRaw))

	//go:embed template/readme.tpl
	readmeRaw string
	readmeTpl = template.Must(template.New("readme").Parse(readmeRaw))
)

func main() {
	err := run(os.Args, os.Stdout, os.Stderr)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, outEval, outLog io.Writer) error {
	cmds := map[string]func(args []string, eval, log io.Writer) error{
		"sync-github": cmdSyncGtihub,
		"sync":        cmdSync,
		"last":        cmdLast,
		"new":         cmdNew,
	}

	fset := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fset.SetOutput(outLog)
	fset.Usage = func() {
		fmt.Fprintf(outLog, `manage local repositories

USAGE:
  repos last
  repos new [repo-name]
  repos sync
  repos sync-github

OPTIONS:
`)
		fset.PrintDefaults()
	}
	err := fset.Parse(args[1:])
	cmd, ok := cmds[fset.Arg(0)]
	if err != nil || !ok {
		fset.PrintDefaults()
		return err
	}

	return cmd(fset.Args(), outEval, outLog)
}

func cmdSync(args []string, outEval, outLog io.Writer) error {
	parallel := 5
	fset := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fset.SetOutput(outLog)
	fset.Usage = func() {
		fmt.Fprintf(outLog, `pull in updates from remote repos

USAGE:
  repos sync

OPTIONS:
`)
		fset.PrintDefaults()
	}
	fset.IntVar(&parallel, "parallel", 5, "parallel downloads")
	err := fset.Parse(args[1:])
	if err != nil {
		return err
	}

	baseDir := "."

	des, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("sync: read %s: %w", baseDir, err)
	}
	dirs := make(chan string, len(des))
	for _, de := range des {
		if de.IsDir() {
			dirs <- filepath.Join(baseDir, de.Name())
		}
	}
	close(dirs)

	resc := make(chan syncResult)
	var wg sync.WaitGroup
	wg.Add(parallel)
	for i := 0; i < parallel; i++ {
		go syncWorker(&wg, dirs, resc)
	}
	go func() {
		wg.Wait()
		close(resc)
	}()
	var i int
	for res := range resc {
		i++
		msg := fmt.Sprintf("%4d %s: ", i, res.dir)
		if res.err != nil {
			msg += res.err.Error()
		} else if res.oldRef == res.newRef {
			msg += res.newRef
		} else {
			msg += res.oldRef + " -> " + res.newRef
		}
		fmt.Fprintln(outLog, msg)
	}
	return nil
}

type syncResult struct {
	dir    string
	err    error
	oldRef string
	newRef string
}

func syncWorker(wg *sync.WaitGroup, in <-chan string, out chan syncResult) {
	defer wg.Done()
	for dir := range in {
		out <- syncRepo(dir)
	}
}

func syncRepo(dir string) syncResult {
	res := syncResult{
		dir: filepath.Base(dir),
	}

	wd := filepath.Join(dir, "default")
	gitDir := filepath.Join(wd, ".git")
	_, err := os.Stat(gitDir)
	if err != nil {
		wd = dir
		gitDir = filepath.Join(wd, ".git")
		_, err = os.Stat(gitDir)
		if err != nil {
			res.err = fmt.Errorf("no git dir found")
			return res
		}
	}

	cmd := exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("get old ref: %w", err)
		return res
	}
	res.oldRef = string(bytes.TrimSpace(out))

	// ensure we're on the default branch
	cmd = exec.Command("git", "rev-parse", "--abbrev-ref", "origin/HEAD")
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("get remote default branch: %w\n%s", err, out)
		return res
	}

	defaultBranch := path.Base(string(bytes.TrimSpace(out)))

	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("switch to default branch: %w\n%s", err, out)
		return res
	}

	cmd = exec.Command("git", "fetch", "--tags", "--prune", "--prune-tags", "--force", "--jobs=10")
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("fetch: %w\n%s", err, out)
		return res
	}
	cmd = exec.Command("git", "merge", "--ff-only", "--autostash")
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("merge: %w\n%s", err, out)
		return res
	}

	cmd = exec.Command("git", "rev-parse", "--short", "HEAD")
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("get new ref: %w\n%s", err, out)
		return res
	}
	res.newRef = string(bytes.TrimSpace(out))

	return res
}

const (
	versionFile = "testrepo-version"
)

func cmdNew(args []string, outEval, outLog io.Writer) error {
	var baseDir, name string
	fset := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fset.SetOutput(outLog)
	fset.Usage = func() {
		fmt.Fprintf(outLog, `create new repositories

USAGE:
  repos new [repo-name]

OPTIONS:
`)
		fset.PrintDefaults()
	}
	err := fset.Parse(args[1:])
	if err != nil {
		return err
	}
	switch fset.NArg() {
	case 0:
		n, err := newTestrepoVersion()
		if err != nil {
			return fmt.Errorf("new: get testrepo version: %w", err)
		}
		name = n

		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("new: get home dir: %w", err)
		}
		baseDir = filepath.Join(homeDir, "tmp")
	case 1:
		name = fset.Arg(0)
		baseDir, _ = os.Getwd()
	default:
		return fmt.Errorf("unexpected extra args: %v", args[2:])
	}

	fp := filepath.Join(baseDir, name)

	err = os.MkdirAll(fp, 0o755)
	if err != nil {
		return fmt.Errorf("new: mkdir %s: %w", fp, err)
	}

	cmd := exec.Command("go", "mod", "init", "go.seankhliao.com/"+name)
	cmd.Dir = fp
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("new: go mod init: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "init")
	cmd.Dir = fp
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("new: git init: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "root-commit")
	cmd.Dir = fp
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("new: git commit: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "remote", "add", "origin", "s:"+name)
	cmd.Dir = fp
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("new: git remote add: %w\n%s", err, out)
	}

	lf := filepath.Join(fp, "LICENSE")
	f, err := os.Create(lf)
	if err != nil {
		return fmt.Errorf("new: create %s: %w", lf, err)
	}
	defer f.Close()
	err = licenseTpl.Execute(f, map[string]string{
		"Date": time.Now().Format("2006"),
	})
	if err != nil {
		return fmt.Errorf("new: render license: %w", err)
	}

	lf = filepath.Join(fp, "README.md")
	f, err = os.Create(lf)
	if err != nil {
		return fmt.Errorf("new: create %s: %w", lf, err)
	}
	defer f.Close()
	err = readmeTpl.Execute(f, map[string]string{
		"Name": name,
	})
	if err != nil {
		return fmt.Errorf("new tmp: render readme: %w", err)
	}

	fmt.Fprintf(outEval, "cd %s\n", fp)
	return nil
}

func newTestrepoVersion() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("get cache dir: %w", err)
	}
	vf := filepath.Join(cacheDir, versionFile)
	b, err := os.ReadFile(vf)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", vf, err)
	}
	ctr, _ := strconv.Atoi(string(b))
	ctr++

	err = os.WriteFile(vf, []byte(strconv.Itoa(ctr)), 0o644)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", vf, err)
	}

	name := fmt.Sprintf("testrepo%04d", ctr)
	return name, nil
}

func cmdLast(args []string, outEval, outLog io.Writer) error {
	fset := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fset.SetOutput(outLog)
	fset.Usage = func() {
		fmt.Fprintf(outLog, `switch to the last created temporary repository

USAGE:
  repos last

OPTIONS:
`)
		fset.PrintDefaults()
	}
	err := fset.Parse(args[1:])
	if err != nil {
		return err
	} else if fset.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fset.Args())
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("tmp: get home directory: %w", err)
	}
	tmpDir := filepath.Join(homeDir, "tmp")
	des, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("tmp: read %s: %w", tmpDir, err)
	}
	var last string
	for _, de := range des {
		if n := de.Name(); strings.HasPrefix(n, "testrepo") && n > last {
			last = n
		}
	}
	if last == "" {
		return fmt.Errorf("tmp: no repo found")
	}

	fmt.Printf("cd %s\n", filepath.Join(tmpDir, last))
	return nil
}

func cmdSyncGtihub(args []string, outEval, outLog io.Writer) error {
	var archived, worktree, prune, dryrun bool
	var tokenEnv string
	var users, orgs []string
	fset := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fset.SetOutput(outLog)
	fset.Usage = func() {
		fmt.Fprintf(outLog, `sync the local repo list from github

USAGE:
  repos github-sync [user/orgs...]

OPTIONS:
`)
		fset.PrintDefaults()
	}
	fset.BoolVar(&archived, "archived", false, "include archived repositories")
	fset.BoolVar(&dryrun, "dryrun", false, "print actions instead of executing them")
	fset.BoolVar(&prune, "prune", false, "prune repositories not found on the remote")
	fset.BoolVar(&worktree, "worktree", false, "nest checkouts under repo/default")
	fset.StringVar(&tokenEnv, "token-env", "GH_TOKEN", "env var to read github token from")
	fset.Func("user", "github user", func(s string) error {
		users = append(users, s)
		return nil
	})
	fset.Func("org", "github org", func(s string) error {
		orgs = append(orgs, s)
		return nil
	})

	err := fset.Parse(args[1:])
	if err != nil {
		return err
	} else if fset.NArg() > 0 {
		return fmt.Errorf("unexpected args: %v", fset.Args())
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv(tokenEnv)},
	)
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)

	allReposM := make(map[string]string)
	for _, user := range users {
		for page := 1; true; page++ {
			repos, res, err := client.Repositories.List(ctx, user, &github.RepositoryListOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				return fmt.Errorf("list repos page %d for %s: %v", page, user, err)
			}
			for _, repo := range repos {
				if !archived && *repo.Archived {
					continue
				}
				allReposM[*repo.Name] = *repo.Owner.Login
			}
			if page >= res.LastPage {
				break
			}
		}
	}
	for _, org := range orgs {
		for page := 1; true; page++ {
			repos, res, err := client.Repositories.ListByOrg(ctx, org, &github.RepositoryListByOrgOptions{
				ListOptions: github.ListOptions{
					Page:    page,
					PerPage: 100,
				},
			})
			if err != nil {
				return fmt.Errorf("list repos page %d for %s: %v", page, org, err)
			}
			for _, repo := range repos {
				if !archived && *repo.Archived {
					continue
				}
				allReposM[*repo.Name] = *repo.Owner.Login
			}
			if page >= res.LastPage {
				break
			}
		}
	}

	localRepoM := make(map[string]struct{})
	des, err := os.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read .: %w", err)
	}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		localRepoM[de.Name()] = struct{}{}
	}

	var toClone []struct {
		owner, repo string
	}
	for k, v := range allReposM {
		if _, ok := localRepoM[k]; !ok {
			toClone = append(toClone, struct {
				owner string
				repo  string
			}{
				v, k,
			})
		}
	}
	sort.Slice(toClone, func(i, j int) bool {
		if toClone[i].owner != toClone[j].owner {
			return toClone[i].owner < toClone[j].owner
		}
		return toClone[i].repo < toClone[j].repo
	})
	var toPrune []string
	for r := range localRepoM {
		if _, ok := allReposM[r]; !ok {
			toPrune = append(toPrune, r)
		}
	}
	sort.Strings(toPrune)

	for _, r := range toClone {
		u := fmt.Sprintf("https://github.com/%s/%s", r.owner, r.repo)
		dst := r.repo
		if worktree {
			dst += "/default"
		}
		msg := "git clone " + u + " " + dst
		if !dryrun {
			cmd := exec.Command("git", "clone", u, dst)
			out, err := cmd.CombinedOutput()
			if err != nil {
				msg += ": " + err.Error() + "\n" + string(out)
			}
		}
		fmt.Fprintln(outLog, msg)
	}
	for _, r := range toPrune {
		msg := "rm -rf " + r
		if !dryrun {
			err := os.RemoveAll(r)
			if err != nil {
				msg += ": " + err.Error()
			}
		}
		fmt.Fprintln(outLog, msg)
	}
	return nil
}
