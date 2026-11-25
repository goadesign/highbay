package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/shquote"
)

// initNextSteps is what `surla init` prints after creating a project; the
// verbs are the shell-quoted project directory, the service name, the
// sentence saying what was created (initCreatedBlank or initCreatedExample,
// which take the service name) and the entries the first commit stages. The
// commands end at `surla`, which starts the project's server and opens its
// views page; the operator tools come after them.
const initNextSteps = `%[3]s

Next, make the first commit and start Surla in the project:
  cd %[1]s
  git init
  git add %[4]s
  git commit -m "Start %[2]s from surla init"
  surla

surla starts the project's server and opens its views page in your browser,
where you confirm changes and approve results.

README.md in the project explains each change's steps, building and testing
with check_project before committing, and verifying a candidate commit against
the confirmed Change Card's request_revision. Coding agents read the
architecture and evaluator guide at surla://golden-path. Tool inputs and results:
https://github.com/goadesign/surla/blob/main/docs/REFERENCE.md#mcp-tools

Operator tools: surla verify and surla approve check a commit outside the
server; README.md shows the surla verify command for the evaluator suite.
`

const (
	// initCreatedBlank says what `surla init` created by default.
	initCreatedBlank = "Created the %s service as a blank project: no methods yet, an empty Story (design/story.go) and an evaluator suite (eval/) with one test that the program starts."
	// initCreatedExample says what `surla init -example` created.
	initCreatedExample = "Created the %s service as Surla's worked example: a price-answering service with a helper agent, its Story (design/story.go) and its evaluator suite (eval/)."
)

// init creates a starter project with scaffold.Init and prints the commands
// that commit it and start Surla in it. The project is blank unless -example
// asks for the worked example. The evaluator module requires the
// Surla version this binary was built from unless -surla-version names
// another exact version. SIGINT and SIGTERM cancel it, leaving -dir unchanged.
func (runtime projectRuntime) init(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory to create; it must not exist (required)")
	modulePath := fs.String("module", "", "Go module path of the new service, such as example.com/shop; its last element names the service (required)")
	version := fs.String("surla-version", "", "exact github.com/goadesign/surla version the evaluator suite requires (default: the version of this surla binary)")
	example := fs.Bool("example", false, "create Surla's worked example, a price-answering service with a helper agent, instead of a blank project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if *dir == "" || *modulePath == "" {
		return errors.New("init requires -dir and -module")
	}
	if *version == "" {
		*version = surlaBuildVersion()
		if !scaffold.ValidVersion(*version) {
			return fmt.Errorf("this surla binary has no exact module version (%q); pass -surla-version with a published version such as v0.0.0-20261118203415-bec57123a61f", buildVersion())
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	target, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	workDir, err := runtime.localInitWorkDir(filepath.Dir(target))
	if err != nil {
		return err
	}
	evalWorkDir, err := runtime.localEvalWorkDir(filepath.Dir(target))
	if err != nil {
		return err
	}
	name, err := scaffold.Init(ctx, scaffold.Options{
		Dir: target, Module: *modulePath, SurlaVersion: *version, WorkDir: workDir, Example: *example,
	}, scaffold.RunCommand, designeval.NewEvaluatorInDir(evalWorkDir))
	if err != nil {
		return err
	}
	staged, err := initStagedEntries(target)
	if err != nil {
		return err
	}
	created := initCreatedBlank
	if *example {
		created = initCreatedExample
	}
	_, err = fmt.Fprintf(stdout, initNextSteps, shquote.Quote(*dir), name, fmt.Sprintf(created, name), staged)
	return err
}

// initStagedEntries returns the top-level entries of the project
// scaffold.Init just wrote in dir, in name order, for the first commit's
// git add. Listing what was written keeps the command right for both the
// blank project and the worked example, which has more folders. Init writes
// only fixed names such as go.mod and design, none of which needs quoting in
// a shell.
func initStagedEntries(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("list the new project: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return strings.Join(names, " "), nil
}

// runVersion prints the module version this surla binary was built from to
// stdout. It accepts -h/--help through its FlagSet and rejects positional
// arguments.
func runVersion(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	v := buildVersion()
	if v == "" {
		v = "(devel)"
	}
	_, err := fmt.Fprintln(stdout, v)
	return err
}

// buildVersion returns the module version this binary was built from, such
// as a pseudo-version stamped from a clean checkout, or "(devel)".
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return info.Main.Version
}

// surlaBuildVersion returns the exact github.com/goadesign/surla module version
// from runtime build metadata when available, or "" for development builds.
func surlaBuildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return scaffold.SurlaVersion(info)
}
