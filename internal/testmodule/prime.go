// This file primes the Go module and checksum-database cache for the committed
// testdata modules and synthetic modules constructed by ordinary-partition
// tests at runtime.
//
// Real starter-template initialization (scaffold.Init and scaffold.Prepare with
// scaffold.RunCommand on internal/scaffold/templates/{candidate,eval}) is
// intentionally out of scope: those paths exercise a user's real networked
// first run and belong to the non-PR Full / starter (-tags=scaffold
// ./internal/scaffold) and Full / generation (-tags=e2e ./cmd/surla) jobs in
// .github/workflows/integration.yml, while ordinary-partition tests in
// internal/scaffold and internal/controlplane substitute fake runners and
// generators.
package testmodule

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PrimeProfile describes one synthetic module fixture whose go mod tidy closure
// and sum.golang.org verification tiles must be present in GOMODCACHE.
type PrimeProfile struct {
	// Name identifies the fixture closure in diagnostics.
	Name string
	// Files maps slash-separated module-relative paths to their initial contents
	// before go mod tidy runs without a go.sum file.
	Files map[string]string
	// ExtraCommands lists additional go subcommands to run in the tidied module
	// directory with GOWORK=off, GOFLAGS=-mod=mod, and GOTOOLCHAIN=local.
	ExtraCommands [][]string
}

// fixedFixtureDirs lists the committed test module profiles that offline
// evaluator and Story tests load with GOPROXY=off.
var fixedFixtureDirs = []string{
	".",
	"internal/testmodule/testdata/goa-goaai-dsl",
	"internal/testmodule/testdata/goa-goaai-evaluator",
	"internal/project/testdata/story-fixture",
	"internal/project/testdata/story-evaluator",
}

// PrimeCache downloads the committed test module profiles and runs go mod tidy
// in temporary directories without go.sum for every synthetic test module
// profile in PrimeProfiles, populating both $GOMODCACHE/cache/download and
// $GOMODCACHE/cache/download/sumdb/sum.golang.org.
func PrimeCache() (err error) {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	for _, rel := range fixedFixtureDirs {
		args := []string{"mod", "download"}
		if rel != "." {
			args = append(args, "all")
		}
		if err := runGoMod(filepath.Join(root, filepath.FromSlash(rel)), "", args...); err != nil {
			return fmt.Errorf("download %s: %w", rel, err)
		}
	}
	profiles, err := PrimeProfiles()
	if err != nil {
		return err
	}
	tmpRoot, err := os.MkdirTemp("", "surla-testmodule-prime-*")
	if err != nil {
		return fmt.Errorf("create module-primer temp dir: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(tmpRoot))
	}()
	for i, profile := range profiles {
		dir := filepath.Join(tmpRoot, fmt.Sprintf("%02d-%s", i, profile.Name))
		if _, err := tidyInDir(dir, profile.Files); err != nil {
			return fmt.Errorf("prime %s: %w", profile.Name, err)
		}
		for _, args := range profile.ExtraCommands {
			if err := runGoMod(dir, "-mod=mod", args...); err != nil {
				return fmt.Errorf("prime %s (%s): %w", profile.Name, strings.Join(args, " "), err)
			}
		}
		if err := runGoMod(dir, "-mod=readonly", "mod", "download", "all"); err != nil {
			return fmt.Errorf("download all for %s: %w", profile.Name, err)
		}
	}
	return nil
}

// PrimeProfiles returns the synthetic module definitions covering every
// pinned test module version and package import closure used across the suite.
func PrimeProfiles() ([]PrimeProfile, error) {
	goaVer, err := surlaVersion("goa.design/goa/v3")
	if err != nil {
		return nil, err
	}
	goaAIVer, err := surlaVersion("goa.design/goa-ai")
	if err != nil {
		return nil, err
	}
	modelVer, err := surlaVersion("goa.design/model")
	if err != nil {
		return nil, err
	}
	storyVer, err := surlaVersion("github.com/goadesign/surla/story")
	if err != nil {
		return nil, err
	}

	profiles := []PrimeProfile{
		{
			Name: "current-core-and-starter",
			Files: map[string]string{
				"go.mod": "module example.com/design\n\ngo 1.24\n\nrequire (\n" +
					"\tgithub.com/goadesign/surla/story " + storyVer + "\n" +
					"\tgoa.design/goa/v3 " + goaVer + "\n" +
					"\tgoa.design/goa-ai " + goaAIVer + "\n" +
					"\tgoa.design/model " + modelVer + "\n" +
					"\tgithub.com/kr/text " + KRTextVersion + "\n)\n",
				"design/design.go": blankImports(
					"github.com/goadesign/surla/story/dsl",
					"github.com/goadesign/surla/story/expr",
					"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp",
					"go.opentelemetry.io/otel/codes",
					"go.opentelemetry.io/otel/trace",
					"goa.design/goa-ai/codegen/agent",
					"goa.design/goa-ai/codegen/ir",
					"goa.design/goa-ai/codegen/jsonschema",
					"goa.design/goa-ai/codegen/mcp",
					"goa.design/goa-ai/dsl",
					"goa.design/goa-ai/eval",
					"goa.design/goa-ai/eval/dsl",
					"goa.design/goa-ai/eval/expr",
					"goa.design/goa-ai/expr/agent",
					"goa.design/goa-ai/features/model/gateway",
					"goa.design/goa-ai/runtime/agent/model",
					"goa.design/goa-ai/runtime/agent/planner",
					"goa.design/goa-ai/runtime/agent/prompt",
					"goa.design/goa-ai/runtime/agent/runtime",
					"goa.design/goa-ai/runtime/agent/storage/inmem",
					"goa.design/goa-ai/runtime/agent/tools",
					"goa.design/goa/v3/codegen",
					"goa.design/goa/v3/codegen/generator",
					"goa.design/goa/v3/dsl",
					"goa.design/goa/v3/eval",
					"goa.design/goa/v3/expr",
					"goa.design/goa/v3/grpc",
					"goa.design/goa/v3/grpc/pb",
					"goa.design/goa/v3/http",
					"goa.design/goa/v3/security",
					"goa.design/model/dsl",
					"goa.design/model/expr",
					"goa.design/model/mdl",
					"google.golang.org/grpc",
					"google.golang.org/grpc/credentials/insecure",
				),
			},
		},
		{
			Name: "published-surla-suite",
			Files: map[string]string{
				"go.mod": "module example.com/shop/eval\n\ngo 1.24\n\nrequire (\n" +
					"\tgithub.com/goadesign/surla " + PublishedSurlaVersion + "\n" +
					"\tgithub.com/goadesign/surla/story " + storyVer + "\n" +
					"\tgoa.design/goa/v3 " + goaVer + "\n" +
					"\tgoa.design/goa-ai " + goaAIVer + "\n" +
					"\tgoa.design/model " + modelVer + "\n" +
					"\tgithub.com/kr/text " + KRTextVersion + "\n)\n",
				"design/design.go": blankImports(
					"github.com/goadesign/surla/story/dsl",
					"github.com/goadesign/surla/verify/candidate",
					"github.com/goadesign/surla/verify/suite",
					"goa.design/goa-ai/dsl",
					"goa.design/goa-ai/eval",
					"goa.design/goa-ai/eval/dsl",
					"goa.design/goa-ai/features/model/gateway",
					"goa.design/goa-ai/runtime/agent/model",
					"goa.design/goa-ai/runtime/agent/tools",
					"goa.design/goa/v3/dsl",
					"goa.design/goa/v3/http",
					"goa.design/model/dsl",
					"google.golang.org/grpc",
				),
			},
			ExtraCommands: [][]string{
				{"get", "github.com/goadesign/surla@" + PublishedSurlaVersion},
				{"mod", "tidy"},
			},
		},
	}

	for _, historicalGoa := range []struct {
		label      string
		version    string
		withGoaCmd bool
	}{
		{label: "complete-sources-goa", version: CompleteSourcesGoaVersion, withGoaCmd: true},
		{label: "starter-pre-complete-sources-goa", version: StarterPreCompleteSourcesGoaVersion},
		{label: "minimum-goa-ai-goa", version: MinimumGoaAIGoaVersion},
	} {
		var extra [][]string
		if historicalGoa.withGoaCmd {
			extra = [][]string{{"list", "-deps", "goa.design/goa/v3/cmd/goa"}}
		}
		profiles = append(profiles,
			PrimeProfile{
				Name: "snapshot-" + historicalGoa.label,
				Files: map[string]string{
					"go.mod":           moduleGoMod("example.com/design", historicalGoa.version, goaAIVer, modelVer),
					"design/design.go": blankImports("goa.design/goa/v3/dsl"),
				},
				ExtraCommands: extra,
			},
			PrimeProfile{
				Name: "story-and-goa-" + historicalGoa.label,
				Files: map[string]string{
					"go.mod": "module example.com/story-design\n\ngo 1.24\n\nrequire (\n" +
						"\tgithub.com/goadesign/surla/story " + storyVer + "\n" +
						"\tgoa.design/goa/v3 " + historicalGoa.version + "\n)\n",
					"design/design.go": blankImports(
						"github.com/goadesign/surla/story/dsl",
						"github.com/goadesign/surla/story/expr",
						"goa.design/goa/v3/codegen",
						"goa.design/goa/v3/dsl",
						"goa.design/goa/v3/eval",
						"goa.design/goa/v3/expr",
						"goa.design/goa/v3/grpc",
						"goa.design/goa/v3/grpc/pb",
						"goa.design/goa/v3/http",
					),
				},
			},
		)
	}

	profiles = append(profiles,
		PrimeProfile{
			Name: "older-than-goa-ai-goa",
			Files: map[string]string{
				"go.mod": "module example.com/design\n\ngo 1.24\n\nrequire goa.design/goa/v3 " +
					OlderThanGoaAIGoaVersion + "\n",
				"design/design.go": blankImports("goa.design/goa/v3/dsl"),
			},
		},
		PrimeProfile{
			Name: "dependency-repair-fixtures",
			Files: map[string]string{
				"go.mod": "module example.com/deps\n\ngo 1.24\n\nrequire (\n" +
					"\trsc.io/binaryregexp " + BinaryRegexpVersion + "\n" +
					"\trsc.io/qr " + QRVersion + "\n)\n",
				"design/design.go": blankImports(
					"rsc.io/binaryregexp",
					"rsc.io/qr",
				),
			},
		},
	)
	return profiles, nil
}

// blankImports returns a Go source file in package design that blank-imports
// each package in pkgs.
func blankImports(pkgs ...string) string {
	var b strings.Builder
	b.WriteString("package design\n\nimport (\n")
	for _, pkg := range pkgs {
		b.WriteString("\t_ \"")
		b.WriteString(pkg)
		b.WriteString("\"\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// runGoMod runs a go command in dir with GOWORK=off, GOTOOLCHAIN=local, and
// the given GOFLAGS value.
func runGoMod(dir, goflags string, args ...string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS="+goflags, "GOTOOLCHAIN=local")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}
