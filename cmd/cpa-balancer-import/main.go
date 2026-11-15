// Command cpa-balancer-import writes the transcript store from Claude Code's transcripts: the
// exact tokens each session used, per hour and model. Run it whenever the store should catch up,
// with --dry-run first.
//
//	cpa-balancer-import --projects <dir> --state <balancer data dir> [--dry-run]
//
// It reads only the usage fields of assistant messages, never their text. The store is
// <state>/transcripts, written whole on every run, so running it again changes nothing. It never
// touches the balancer's history, which stays quota-accurate.
package main

import (
	"errors"
	"flag"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"

	"github.com/wappsdev/cpa-balancer/internal/backfill"
	"github.com/wappsdev/cpa-balancer/internal/transcripts"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cpa-balancer-import:", plain(err))
		os.Exit(1)
	}
}

// plain is the error without the file path an operating system error carries: a transcript's
// path names a project, and nothing the tool prints may.
func plain(err error) string {
	var pe *iofs.PathError
	if errors.As(err, &pe) {
		return fmt.Sprintf("%s failed: %v", pe.Op, pe.Err)
	}
	var le *os.LinkError // a rename or link names both paths
	if errors.As(err, &le) {
		return fmt.Sprintf("%s failed: %v", le.Op, le.Err)
	}
	return err.Error()
}

func run(args []string) error {
	fs := flag.NewFlagSet("cpa-balancer-import", flag.ContinueOnError)
	projects := fs.String("projects", "", "the directory with one directory of transcripts per project (required)")
	state := fs.String("state", "", "the balancer's data directory, the one holding its history directory (required)")
	dry := fs.Bool("dry-run", false, "print counts and write nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *projects == "" || *state == "" {
		fs.Usage()
		return fmt.Errorf("--projects and --state are required")
	}
	if info, err := os.Stat(*state); err != nil || !info.IsDir() {
		return fmt.Errorf("--state is not a directory")
	}
	rep, err := backfill.Run(backfill.Options{Projects: *projects, Store: filepath.Join(*state, transcripts.Dir), DryRun: *dry})
	rep.Write(os.Stdout)
	return err
}
