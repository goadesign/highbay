// This file merges a design change made outside Surla into the draft's text
// of one design file, with git merge-file.
package designversion

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// draftLabel, baseLabel and projectLabel name the three texts in
	// MergeFile's conflict markers; git prints the base's only in the diff3
	// styles, which MergeFile never uses.
	draftLabel, baseLabel, projectLabel = "draft", "base", "project folder"
	// maxConflicts is the largest exit status by which git merge-file
	// reports conflicts: it exits with their number, truncated to 127, and
	// above it on an error.
	maxConflicts = 127
)

// MergeFile merges into draft, the draft's text of one design file, the
// changes that lead from base to project, the project folder's text of that
// file, as `git merge-file -p` does; base is the version both descend from,
// "" when neither had the file. Changes to separate lines are both kept, and
// a change both made alike is kept once. Where both changed the same lines
// differently, conflict is true and merged holds both changes between git's
// markers, "<<<<<<< draft", "=======" and ">>>>>>> project folder", which Go
// cannot parse until someone resolves them.
//
// Merging a clean result again with the same base and project returns it
// unchanged. A result that holds markers, merged again, gets new markers
// nested in them, so callers merge only a draft text Go can parse. A clean
// result need not be in gofmt form: lines from both sides can need another
// alignment.
//
// Git runs on copies of the three texts in a temporary folder that MergeFile
// removes, outside any repository and with no configuration file, so a user's
// merge.conflictStyle cannot change the markers. A text git refuses, such as
// one holding a NUL byte, and any failure to run git are errors, with merged
// empty.
func MergeFile(ctx context.Context, draft, base, project string) (merged string, conflict bool, err error) {
	dir, err := os.MkdirTemp("", "surla-merge-*")
	if err != nil {
		return "", false, fmt.Errorf("merge a design file: %w", err)
	}
	defer func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			merged, conflict, err = "", false, errors.Join(err, fmt.Errorf("merge a design file: %w", rmErr))
		}
	}()
	texts := []struct{ name, text string }{{"draft", draft}, {"base", base}, {"project", project}}
	for _, t := range texts {
		if err := os.WriteFile(filepath.Join(dir, t.name), []byte(t.text), 0o600); err != nil {
			return "", false, fmt.Errorf("merge a design file: %w", err)
		}
	}
	cmd := exec.CommandContext(ctx, "git", "merge-file", "-p", "-q",
		"-L", draftLabel, "-L", baseLabel, "-L", projectLabel, "draft", "base", "project")
	cmd.Dir = dir
	cmd.Env = mergeEnv(dir)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	var exit *exec.ExitError
	switch {
	case runErr == nil:
		return stdout.String(), false, nil
	case errors.As(runErr, &exit) && exit.ExitCode() >= 1 && exit.ExitCode() <= maxConflicts:
		return stdout.String(), true, nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return "", false, fmt.Errorf("git merge-file: %w: %s", runErr, msg)
	}
	return "", false, fmt.Errorf("git merge-file: %w", runErr)
}

// mergeEnv is the environment of MergeFile's git, run in dir: the server's
// without any GIT_* variable, which could name a repository or add
// configuration (GIT_CONFIG_PARAMETERS), with no system or global
// configuration file, and with repository discovery stopped at dir, so that
// no repository around a temporary folder lends its configuration either.
// LC_ALL=C keeps git's messages in English.
func mergeEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(dir),
		"LC_ALL=C",
	)
}
