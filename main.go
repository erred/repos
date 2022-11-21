package main

import (
	"flag"
	"os"

	"github.com/google/subcommands"
)

func main() {
	fset := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	cmdr := subcommands.NewCommander(fset, os.Args[0])

	cmdr.Register(cmdr.HelpCommand(), "")
	cmdr.Register(&syncCmd{}, "")
	cmdr.Register(&syncGHCmd{}, "")
	cmdr.Register(&lastCmd{}, "")
	cmdr.Register(&newCmd{}, "")
}
