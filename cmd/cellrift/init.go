package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/riftcell/cellrift/scenarios"
	"github.com/spf13/cobra"
)

// initCmd writes the bundled scenario library to a directory. It exists for
// the `go install` user, who has no clone: without it the README quickstart's
// first `cellrift validate scenarios/...` fails with "no such file".
func initCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "init [dir]",
		Short:         "Write the bundled example scenarios to a directory (default ./scenarios)",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "scenarios"
			if len(args) == 1 {
				dir = args[0]
			}
			written, skipped, err := unpackLibrary(scenarios.FS, dir)
			if err != nil {
				return &exitError{code: exitExecutionError, msg: err.Error()}
			}
			printInitSummary(os.Stdout, dir, written, skipped)
			return nil
		},
	}
	return cmd
}

// unpackLibrary copies every file in fsys under dir, creating directories as
// needed. A file that already exists at its destination is never overwritten
// — a user's edited copy of an example is theirs — and is reported in skipped
// instead. Returned paths are relative to dir.
func unpackLibrary(fsys fs.FS, dir string) (written, skipped []string, err error) {
	err = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		dest := filepath.Join(dir, filepath.FromSlash(path))
		if _, statErr := os.Stat(dest); statErr == nil {
			skipped = append(skipped, path)
			return nil
		}
		data, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		if writeErr := os.WriteFile(dest, data, 0o644); writeErr != nil {
			return writeErr
		}
		written = append(written, path)
		return nil
	})
	return written, skipped, err
}

func printInitSummary(w *os.File, dir string, written, skipped []string) {
	for _, p := range written {
		fmt.Fprintf(w, "wrote   %s\n", filepath.Join(dir, filepath.FromSlash(p)))
	}
	for _, p := range skipped {
		fmt.Fprintf(w, "kept    %s (already exists)\n", filepath.Join(dir, filepath.FromSlash(p)))
	}
	fmt.Fprintf(w, "\n%d scenario file(s) written to %s/, %d left untouched.\n", len(written), dir, len(skipped))
	fmt.Fprintf(w, "Next: `cellrift selftest` to watch one pass, `cellrift list` to browse, "+
		"`cellrift validate %s/ims/basic_call.yaml` to check one.\n", dir)
}
