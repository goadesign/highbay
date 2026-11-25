// Package testmodule builds real Go module snapshots of Goa designs for tests
// of the design evaluator and its callers.
package testmodule

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

const (
	// PublishedHighbayVersion is the published github.com/goadesign/highbay
	// module version that starter, test-writer, and build-report fixtures
	// require on the module proxy.
	PublishedHighbayVersion = "v0.0.0-20261125231720-7e4be2233e61"

	// CompleteSourcesGoaVersion is the first goa.design/goa/v3 version
	// with generator.CompleteSources.
	CompleteSourcesGoaVersion = "v3.35.1-0.20261103205041-35e789e8f9b1"

	// StarterPreCompleteSourcesGoaVersion is the goa.design/goa/v3 version
	// that starters created before generator.CompleteSources require.
	StarterPreCompleteSourcesGoaVersion = "v3.35.1-0.20261030163856-3e08a6128171"

	// MinimumGoaAIGoaVersion is goa-ai's minimum goa.design/goa/v3 version,
	// which lacks generator.CompleteSources.
	MinimumGoaAIGoaVersion = "v3.35.1-0.20261102054348-8481e49a6c89"

	// OlderThanGoaAIGoaVersion is a goa.design/goa/v3 version older than
	// the evaluator's goa-ai requirement, used to verify upgrade refusal.
	OlderThanGoaAIGoaVersion = "v3.21.0"

	// BinaryRegexpVersion is the rsc.io/binaryregexp version used by the
	// dependency-repair end-to-end journey.
	BinaryRegexpVersion = "v0.2.0"

	// QRVersion is the rsc.io/qr version used by the workspace checksum
	// and container dependency-repair end-to-end tests.
	QRVersion = "v0.2.0"

	// KRTextVersion is the github.com/kr/text version that go mod tidy
	// resolves through gopkg.in/yaml.v3 -> gopkg.in/check.v1 ->
	// github.com/kr/pretty when a design imports both Goa and Goa-AI.
	// Including it in Module's initial require block prevents go mod tidy
	// from querying the module proxy for @latest on every synthetic module.
	KRTextVersion = "v0.2.0"
)

// Snapshot returns the files of a tidy module named example.com/design whose
// design/ package holds design (as design/design.go), with Module's
// requirements.
func Snapshot(t testing.TB, goaVersion, design string) map[string]string {
	t.Helper()
	return Module(t, "example.com/design", goaVersion, map[string]string{"design/design.go": design})
}

// Module returns the files of a tidy module at modulePath holding files. It
// requires goa.design/goa/v3 at goaVersion (Highbay's own version when empty)
// and goa.design/goa-ai and goa.design/model at Highbay's versions; go mod
// tidy drops the ones the files do not import and completes go.mod and go.sum.
func Module(t testing.TB, modulePath, goaVersion string, files map[string]string) map[string]string {
	t.Helper()
	if goaVersion == "" {
		goaVersion = HighbayVersion(t, "goa.design/goa/v3")
	}
	out := maps.Clone(files)
	out["go.mod"] = moduleGoMod(
		modulePath,
		goaVersion,
		HighbayVersion(t, "goa.design/goa-ai"),
		HighbayVersion(t, "goa.design/model"),
	)
	return Tidy(t, out)
}

// Tidy writes files, which must include go.mod, to a new module directory,
// runs go mod tidy, and returns the files with the resulting go.mod and
// go.sum.
func Tidy(t testing.TB, files map[string]string) map[string]string {
	t.Helper()
	out, err := tidyInDir(t.TempDir(), files)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// HighbayVersion returns the version Highbay's own go.mod requires of path.
func HighbayVersion(t testing.TB, path string) string {
	t.Helper()
	v, err := highbayVersion(path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// WithFile returns a copy of files with name set to content.
func WithFile(files map[string]string, name, content string) map[string]string {
	out := maps.Clone(files)
	out[name] = content
	return out
}

// Replace returns files with old replaced by new in the named file.
func Replace(t testing.TB, files map[string]string, name, old, replacement string) map[string]string {
	t.Helper()
	if !strings.Contains(files[name], old) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	return WithFile(files, name, strings.Replace(files[name], old, replacement, 1))
}

// moduleGoMod returns the initial go.mod content for Module before go mod tidy
// prunes unused requirements and writes go.sum.
func moduleGoMod(modulePath, goaVersion, goaAIVersion, modelVersion string) string {
	return "module " + modulePath + "\n\ngo 1.24\n\nrequire (\n" +
		"\tgoa.design/goa/v3 " + goaVersion + "\n" +
		"\tgoa.design/goa-ai " + goaAIVersion + "\n" +
		"\tgoa.design/model " + modelVersion + "\n" +
		"\tgithub.com/kr/text " + KRTextVersion + "\n)\n"
}

// tidyInDir writes files into dir, runs go mod tidy with the isolated test
// module environment, and returns files updated with the resulting go.mod and
// go.sum.
func tidyInDir(dir string, files map[string]string) (map[string]string, error) {
	out := maps.Clone(files)
	for name, content := range out {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return nil, err
		}
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	tidy.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	if b, err := tidy.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go mod tidy: %w\n%s", err, b)
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out[name] = string(b)
	}
	return out, nil
}

// highbayVersion returns the version Highbay's own go.mod requires of path.
func highbayVersion(path string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	mod, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		return "", err
	}
	for _, r := range mod.Require {
		if r.Mod.Path == path {
			return r.Mod.Version, nil
		}
	}
	return "", fmt.Errorf("Highbay's go.mod does not require %s", path)
}

// repoRoot locates the repository root from this source file's path.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve internal/testmodule source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}
