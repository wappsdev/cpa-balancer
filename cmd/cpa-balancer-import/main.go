// Command cpa-balancer-import rebuilds the balancer's per-session history from Claude Code
// transcripts. It is a one-off: run it once after installing the plugin, with --dry-run first.
//
//	cpa-balancer-import --projects <dir> --state <balancer data dir> [--dry-run] [--fable <name part>]
//
// It reads only the usage fields of assistant messages. It writes only into time slots that hold
// no recorded traffic, so running it again changes nothing. Run it while CPA is idle or stopped.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wappsdev/cpa-balancer/internal/backfill"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpa-balancer-import:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("cpa-balancer-import", flag.ContinueOnError)
	projects := fs.String("projects", "", "the directory with one directory of transcripts per project (required)")
	state := fs.String("state", "", "the balancer's data directory, the one holding its history directory (required)")
	dry := fs.Bool("dry-run", false, "print counts and write nothing")
	fable := fs.String("fable", "fable", "a model whose name holds this belongs to the Fable bucket; the others to main")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projects == "" || *state == "" {
		fs.Usage()
		return fmt.Errorf("--projects and --state are required")
	}
	if info, err := os.Stat(*state); err != nil || !info.IsDir() {
		return fmt.Errorf("--state %q is not a directory", *state)
	}
	rep, err := backfill.Run(backfill.Options{Projects: *projects, History: filepath.Join(*state, "history"), FableMark: *fable, DryRun: *dry})
	rep.Write(os.Stdout)
	return err
}
