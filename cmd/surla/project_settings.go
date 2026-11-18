// This file implements stopped-or-running project management of startup verification and promotion settings.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/project"
)

// settingsHelp is the file format `surla help <kind>` prints after the usage
// line: every field the owning loader requires and a minimal valid file. The
// loaders, verificationConfig and decodePromotionFileAtBase, stay the
// authority; TestSettingsHelpExamplesDecode keeps each example accepted by
// the loader's strict decoder.
var settingsHelp = map[string]string{
	"verification": `set reads a JSON file outside the project folder and its Git working tree,
refuses unknown fields, resolves relative paths against the file's folder and
stores a copy; the server uses it from its next start. Every field is required:

  candidate_repo    Git repository of the application (path or remote address)
  candidate_module  the application's Go module folder inside candidate_repo
  binaries          binary name to package path in that module
  suite_repo        Git repository of the evaluator Surla runs instead of the
                    test writer's tests
  suite_commit      full commit ID of the evaluator every run uses
  suite_module      the evaluator's Go module folder inside suite_repo
  suite_command     the evaluator program's package path inside suite_module
  timeout           how long one run may take, such as 30m

Example:

  {
    "candidate_repo": "../shop",
    "candidate_module": ".",
    "binaries": {"shop": "./cmd/shop"},
    "suite_repo": "../shop-evals",
    "suite_commit": "0123456789abcdef0123456789abcdef01234567",
    "suite_module": ".",
    "suite_command": "./cmd/shop-evals",
    "timeout": "30m"
  }
`,
	"promotion": `Promotion settings are optional; approval needs none. When stored, each
approval also checks the binaries published in <release_dir>/<candidate_commit>/.
set reads a JSON file outside the project folder and its Git working tree,
refuses unknown fields and stores a copy; the server uses it from its next
start. Every field is required:

  release_dir  folder outside the project folder holding one folder per
               candidate commit, each published complete in one rename
  binaries     plain names of the binaries every commit folder holds

Example:

  {
    "release_dir": "../shop-releases",
    "binaries": ["shop"]
  }
`,
}

// settings validates a named operator file before atomically storing its normalized copy.
// Relative local repository and release paths are resolved against the named file's directory.
func (runtime projectRuntime) settings(kind string, args []string, stdout io.Writer) (err error) {
	if len(args) == 1 && slices.Contains(helpWords, args[0]) {
		_, err := fmt.Fprintf(os.Stderr, "Usage: surla %s set <file> | show | remove\n\n%s", kind, settingsHelp[kind])
		return err
	}
	valid := len(args) == 2 && args[0] == "set" || len(args) == 1 && (args[0] == "show" || args[0] == "remove")
	if !valid {
		return fmt.Errorf("usage: surla %s set <file> | show | remove", kind)
	}
	state, err := runtime.discover()
	if err != nil {
		return err
	}
	if state.Record.ID == "" && args[0] != "set" {
		_, err := fmt.Fprintf(stdout, "No %s settings are stored.\n", kind)
		return err
	}
	var snapshot []byte
	if args[0] == "set" {
		path, err := abspath.Eval(args[1])
		if err != nil {
			return err
		}
		snapshot, err = validatedSettings(kind, path, state)
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lock, err := state.StartLock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	created, err := state.Initialize(fmt.Sprintf("uid:%d", os.Geteuid()))
	if err != nil {
		return err
	}
	if created {
		if err := writeProjectCreatedNotice(stdout); err != nil {
			return err
		}
	}
	switch args[0] {
	case "show":
		raw, err := state.ReadSettings(kind)
		if errors.Is(err, os.ErrNotExist) {
			_, err = fmt.Fprintf(stdout, "No %s settings are stored.\n", kind)
			return err
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s\n", raw)
		return err
	case "remove":
		if err := state.RemoveSettings(kind); err != nil {
			return err
		}
	case "set":
		if err := state.SaveSettings(kind, snapshot); err != nil {
			return err
		}
	}
	verb := "stored"
	if args[0] == "remove" {
		verb = "removed"
	}
	_, err = fmt.Fprintf(stdout, "%s settings %s. Changes take effect the next time this project's server starts.\n", kind, verb)
	return err
}

// validatedSettings reuses the owning loaders and rejects removed storage fields before any persistence.
func validatedSettings(kind, path string, state *project.State) ([]byte, error) {
	return validatedSettingsWithTimeout(kind, path, state, context.WithTimeout)
}

// validatedSettingsWithTimeout injects the workspace Git deadline factory without
// changing the production allowance or the settings persistence boundary.
func validatedSettingsWithTimeout(
	kind, path string,
	state *project.State,
	withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc),
) ([]byte, error) {
	protected, err := workspaceProtectedDirs(state.Record.Directory, withTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w; check that Git can read the workspace, then run surla %s set <file> again", err, kind)
	}
	if err := validateWorkspaceDirSeparation("settings file", path, protected...); err != nil {
		return nil, err
	}
	switch kind {
	case "verification":
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var file verificationFile
		if err := decodeSettings(raw, &file); err != nil {
			return nil, err
		}
		cfg, err := verificationConfig(file, "", "")
		if err != nil {
			return nil, err
		}

		file.CandidateRepo, err = resolveRepositorySetting(filepath.Dir(path), cfg.Plan.CandidateRepo)
		if err != nil {
			return nil, err
		}
		file.SuiteRepo, err = resolveRepositorySetting(filepath.Dir(path), cfg.Plan.SuiteRepo)
		if err != nil {
			return nil, err
		}
		return json.Marshal(file)
	case "promotion":
		file, err := decodePromotionFileAtBase(path, filepath.Dir(path))
		if err != nil {
			return nil, err
		}
		file.ReleaseDir, err = abspath.EvalMissing(file.ReleaseDir)
		if err != nil {
			return nil, err
		}
		if err := validateWorkspaceDirSeparation("release_dir", file.ReleaseDir, protected...); err != nil {
			return nil, err
		}
		return json.Marshal(file)
	default:
		return nil, errors.New("unknown settings")
	}
}

// resolveRepositorySetting preserves remote Git addresses and resolves local path spellings before storing them.
func resolveRepositorySetting(base, repository string) (string, error) {
	if strings.Contains(repository, ":") {
		return repository, nil
	}
	return abspath.EvalFrom(base, repository)
}

// decodeSettings applies the same strict current-format boundary to persisted settings and operator files.
func decodeSettings(raw []byte, value any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("data after the configuration")
	}
	return nil
}
