package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/subcommands"
)

type lastCmd struct{}

func (c lastCmd) Name() string                { return "last" }
func (c lastCmd) Synopsis() string            { return "jumps to the most recently created test repo" }
func (c lastCmd) Usage() string               { return "repos last\n" }
func (c lastCmd) SetFlags(fset *flag.FlagSet) {}
func (c lastCmd) Execute(ctx context.Context, fset *flag.FlagSet, args ...any) subcommands.ExitStatus {
	if fset.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "repos last: unexpected args:", args)
		return subcommands.ExitUsageError
	}
	err := c.run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repos last:", err)
		return subcommands.ExitFailure
	}
	return subcommands.ExitSuccess
}

func (c lastCmd) run(ctx context.Context) error {
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
