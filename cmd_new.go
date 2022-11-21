package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/subcommands"
)

const (
	versionFile = "testrepo-version"
)

type newCmd struct{}

func (c newCmd) Name() string                { return "new" }
func (c newCmd) Synopsis() string            { return "create a new repository" }
func (c newCmd) Usage() string               { return "repos new [repo-name]\n" }
func (c newCmd) SetFlags(fset *flag.FlagSet) {}
func (c newCmd) Execute(ctx context.Context, fset *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	var base, name string
	switch fset.NArg() {
	case 0:
		var err error
		name, err = newTestrepoVersion()
		if err != nil {
			fmt.Fprintln(os.Stderr, "repos new: get testrepo version:", err)
			return subcommands.ExitFailure
		}

		base, err = os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "repos new: get home dir:", err)
			return subcommands.ExitFailure
		}
		base = filepath.Join(base, "tmp")

	case 1:
		name = fset.Arg(0)

		var err error
		base, err = os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "repos new: get current dir:", err)
			return subcommands.ExitFailure
		}

	default:
		fmt.Fprintln(os.Stderr, "repos new: got args:", fset.NArg(), "expected at most 1")
		return subcommands.ExitUsageError
	}

	err := c.run(ctx, base, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repos new:", err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}

func (c newCmd) run(ctx context.Context, base, name string) error {
	fp := filepath.Join(base, name)
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

	fmt.Println("cd", fp)
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
