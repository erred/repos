package main

import (
	"bytes"
	"context"
	_ "embed"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sync"
	"text/template"

	"github.com/google/subcommands"
)

var (
	//go:embed template/license.tpl
	licenseRaw string
	licenseTpl = template.Must(template.New("license").Parse(licenseRaw))

	//go:embed template/readme.tpl
	readmeRaw string
	readmeTpl = template.Must(template.New("readme").Parse(readmeRaw))
)

type syncCmd struct {
	parallel int
}

func (c syncCmd) Name() string     { return "sync" }
func (c syncCmd) Synopsis() string { return "sync repositories with upstream" }
func (c syncCmd) Usage() string    { return "repos sync [-parallel=N]\n" }
func (c *syncCmd) SetFlags(fset *flag.FlagSet) {
	fset.IntVar(&c.parallel, "parallel", 5, "parallel syncs to run")
}

func (c syncCmd) Execute(ctx context.Context, fset *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if fset.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "repos sync: unexpected args:", args)
		return subcommands.ExitUsageError
	}

	err := c.run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repos sync:", err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}

func (c syncCmd) run(ctx context.Context) error {
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
	for i := 0; i < c.parallel; i++ {
		wg.Add(1)
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

	cmd = exec.Command("git", "worktree", "prune")
	cmd.Dir = wd
	out, err = cmd.CombinedOutput()
	if err != nil {
		res.err = fmt.Errorf("prune worktrees: %w\n%s", err, out)
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
