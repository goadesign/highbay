// This file exposes failed generation phases and the output of the build or
// the design program with exact known path spellings replaced. It preserves
// the complete original error for CLI callers and operator traces.
package designeval

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type (
	// diagnosticPaths identifies the exact directories supplied to the build
	// and the design program. Only their known path spellings are replaced.
	diagnosticPaths struct {
		// source is the staged copy of the caller's module.
		source string
		// temporary holds this operation's build, logs, and generated output.
		temporary string
		// work is the evaluator-owned compiler workspace.
		work string
		// buildCache is the selected private or owner-retained compiler cache.
		buildCache string
		// goroot is the exact compiler toolchain directory.
		goroot string
		// modCache is the operator's read-only module cache.
		modCache string
	}
	// phaseError identifies the operation in progress when evaluation failed.
	// Its phase is one of the descriptions runProgram sets; modulePhases
	// lists those that work on the target module's own files.
	// It makes no claim about whether source, storage, or setup caused failure.
	phaseError struct {
		// phase is the owner-selected operation description, never raw output.
		phase string
		// err retains every original cause, including cleanup failures.
		err error
	}
	// snapshotValidationError contains only errors derived from caller-supplied
	// strings by validateSnapshot, before operator files or commands are used.
	// Its original detail is safe for the caller, including missing-file remedies.
	snapshotValidationError struct {
		// err is the unchanged validation failure and its original Go causes.
		err error
	}
	// storyRequirementError is a failure to load the design's dependencies
	// whose cause the snapshot shows: a design file imports the Story
	// language and the module's go.mod does not require the Story module.
	// withStoryRequirementStep is its only constructor.
	storyRequirementError struct {
		// err is the unchanged module loading failure.
		err error
		// version is the Story module version the evaluator's go.mod
		// requires, which GenerationDiagnostic tells the caller to require.
		version string
	}
	// commandOutputError retains the output of the build or the design
	// program once. Module commands never construct it because their output
	// can include operator proxy credentials and private configuration.
	commandOutputError struct {
		// err is the operation failure without appended output.
		err error
		// output is the original command output, without another formatted copy.
		output string
		// paths identifies the exact private directories the command used.
		paths diagnosticPaths
	}
)

// The phases of runProgram that check, resolve, compile or evaluate the
// target module's own files. GenerationDiagnostic names every phase in its
// message; FailedOnModule reports these four.
const (
	phaseCheckFiles  = "checking the local module files"
	phaseLoadModules = "loading the design module dependencies"
	phaseCompile     = "compiling the design"
	phaseEvaluateDSL = "evaluating the design and generating files"
)

// modulePhases lists the module phases FailedOnModule reports.
var modulePhases = []string{phaseCheckFiles, phaseLoadModules, phaseCompile, phaseEvaluateDSL}

// GenerationDiagnostic describes the phase where generation failed and any
// available output of the build or the design program, with exact private
// directory names replaced. HTTP and MCP callers receive this message while
// CLI callers and traces keep err unchanged. A compiler error exit does not
// distinguish invalid source from cache, disk, or setup failures, so this
// function never makes that verdict. Module-loading stderr is always withheld
// because those commands run with the operator's HOME and proxy settings; when
// the snapshot shows that a design file imports the Story language its go.mod
// does not require, the message names the go get step that adds the
// requirement instead.
//
// Known-path substitution is diagnostic hygiene, not confidentiality. The
// executed design is an ordinary process with the user's file and network
// access, so its output can hold anything the user can read.
func GenerationDiagnostic(err error) string {
	phase := "starting the design compiler"
	var operation *phaseError
	if errors.As(err, &operation) {
		phase = operation.phase
	}
	message := "Code generation failed while " + phase + "."
	var validation *snapshotValidationError
	if errors.As(err, &validation) {
		message += "\n" + validation.Error()
	}
	var story *storyRequirementError
	if errors.As(err, &story) {
		message += "\nThe design imports the Story language, but its module's go.mod does not require it. " +
			"In the folder that holds that go.mod, run: go get " + harnessStoryModule + "@" + story.version +
			", then try again."
	}
	var output *commandOutputError
	if errors.As(err, &output) && output.output != "" {
		message += "\nOutput:\n" + output.paths.forCaller(output.output)
	}
	var build *buildError
	if errors.As(err, &build) {
		message += "\nIf the compiler reports that generator.CompleteSources is undefined, the module needs a goa.design/goa/v3 requirement or versioned replacement at " + completeSourcesGoa + " or later."
	}
	return message
}

// FailedOnModule reports whether err, from Evaluate or Generate, ended in a
// phase that works on the target module's own files: checking them, loading
// the dependencies its go.mod and go.sum name, compiling its design, or
// evaluating it. Such a failure is first the
// module's author's to look at. A failure while preparing or removing the
// compiler's own files, while starting the design generator or reading its
// output, is not; nor is any failure of an evaluation that Close ended or
// whose processes would not stop, whatever phase it had reached. Like
// GenerationDiagnostic it makes no verdict that the module is wrong: a full
// disk while the design compiles still fails in a module phase.
func FailedOnModule(err error) bool {
	// An evaluation that Close ended, or whose processes would not stop,
	// failed for a reason outside the module, whatever phase it had reached.
	if stuck := (*stuckGroupError)(nil); errors.Is(err, errEvaluatorClosed) || errors.As(err, &stuck) {
		return false
	}
	if validation := (*snapshotValidationError)(nil); errors.As(err, &validation) {
		return true
	}
	operation := (*phaseError)(nil)
	return errors.As(err, &operation) && slices.Contains(modulePhases, operation.phase)
}

// FailedLoadingDependencies reports whether err, from Evaluate or Generate, is
// a failure FailedOnModule reports that ended while loading the target
// module's dependencies: the go commands that resolve the
// packages its design imports and the versions its go.mod and go.sum select.
// Those commands run with the operator's Go settings and may download modules, so such a
// failure can come from the module's files or from where the evaluator runs
// (no network, a proxy that cannot be reached, a Go older than the module
// requires). Their output is withheld, and this function does not say which.
func FailedLoadingDependencies(err error) bool {
	operation := (*phaseError)(nil)
	return FailedOnModule(err) && errors.As(err, &operation) && operation.phase == phaseLoadModules
}

// Error preserves the original CLI and trace error text without adding a phase.
func (e *phaseError) Error() string { return e.err.Error() }

// Unwrap preserves the original error causes for Go callers.
func (e *phaseError) Unwrap() error { return e.err }

// Error preserves the original validation error text for CLI and trace callers.
func (e *snapshotValidationError) Error() string { return e.err.Error() }

// Unwrap preserves the validator's original error causes for Go callers.
func (e *snapshotValidationError) Unwrap() error { return e.err }

// Error preserves the module loading failure's CLI and trace text, which
// ends with the go command's own output.
func (e *storyRequirementError) Error() string { return e.err.Error() }

// Unwrap preserves the module loading failure's original causes.
func (e *storyRequirementError) Unwrap() error { return e.err }

// Error reproduces the existing parenthesized command-output error text.
func (e *commandOutputError) Error() string { return fmt.Sprintf("%s (%s)", e.err, e.output) }

// Unwrap preserves the command's actual failure without inferring its cause.
func (e *commandOutputError) Unwrap() error { return e.err }

// forCaller replaces only exact known directory names, both as raw text and as
// the interior of a Go %q string. Replacing the encoded spelling is necessary
// for filenames containing quotes, backslashes, control bytes, or invalid UTF-8.
// Raw errors and their output remain unchanged.
func (p diagnosticPaths) forCaller(output string) string {
	for _, mapping := range []struct{ from, to string }{
		{p.source, "."}, {p.temporary, "<generation>"}, {p.work, "<compiler>"},
		{p.buildCache, "<build-cache>"}, {p.goroot, "<toolchain>"}, {p.modCache, "<modules>"},
	} {
		if mapping.from != "" {
			from := filepath.Clean(mapping.from)
			quoted := strconv.Quote(from)
			for _, spelling := range []string{from, quoted[1 : len(quoted)-1]} {
				output = strings.ReplaceAll(output, spelling, mapping.to)
			}
		}
	}
	return output
}
