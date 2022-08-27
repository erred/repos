package main

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"golang.org/x/exp/maps"
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
	err := run(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmds := map[string]func([]string) error{
		// "status": cmdStatus,
		"sync": cmdSync,
		"last": cmdLast,
		"new":  cmdNew,
	}

	args = args[1:]
	if len(args) == 0 {
		return fmt.Errorf("missing command: %v", maps.Keys(cmds))
	}
	cmd, ok := cmds[args[0]]
	if !ok {
		return fmt.Errorf("command %v not in %v", args[0], maps.Keys(cmds))
	}

	return cmd(args[1:])
}

func cmdSync([]string) error {
	baseDir := "."
	parallel := 5

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
		fmt.Fprintln(os.Stderr, msg)
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

func cmdNew(args []string) error {
	var baseDir, name string
	if len(args) > 1 {
		return fmt.Errorf("unexpected extra args: %v", args[2:])
	} else if len(args) == 1 {
		name = args[0]
		baseDir, _ = os.Getwd()
	} else {
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
	}

	fp := filepath.Join(baseDir, name)

	err := os.MkdirAll(fp, 0o755)
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

	fmt.Printf("cd %s\n", fp)
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

func cmdLast([]string) error {
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
