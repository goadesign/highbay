package verify

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goadesign/highbay/internal/gitenv"
	"github.com/goadesign/highbay/verify/candidate"
	"github.com/goadesign/highbay/verify/suite"
	"goa.design/goa-ai/eval"
)

type (
	// Plan says what to verify: an exact candidate commit, the binaries to
	// build from it, and an exact evaluator suite commit.
	Plan struct {
		// CandidateRepo is the git repository holding the candidate.
		CandidateRepo string
		// CandidateCommit is the full commit ID to build, in the repository's
		// object format. Branch names and other revision expressions are
		// rejected.
		CandidateCommit string
		// CandidateModule is the Go module directory inside the candidate
		// repository that the binaries are built from, such as
		// "examples/orders" or ".". After symbolic links are resolved it must
		// still be inside the checkout.
		CandidateModule string
		// Binaries maps each artifact name to its package path inside
		// CandidateModule, such as "./cmd/orders". A name is the artifact ID
		// the suite receives as --artifact name=path: it must be non-empty,
		// not "." or "..", and contain no "/", "\\", "=" or NUL. Each package
		// path is "." or starts with "./", and after symbolic links are
		// resolved it must name a directory inside CandidateModule.
		Binaries map[string]string
		// SuiteRepo is the git repository holding the evaluator suite.
		SuiteRepo string
		// SuiteCommit is the full ID of the suite commit to run.
		SuiteCommit string
		// SuiteModule is the suite's Go module directory inside SuiteRepo.
		// Like CandidateModule, it must resolve to a directory inside the
		// checkout.
		SuiteModule string
		// SuiteCommand is the suite command's package path inside
		// SuiteModule, following the suite command contract. Like a candidate
		// package path, it is "." or starts with "./" and must resolve to a
		// directory inside SuiteModule.
		SuiteCommand string
		// RequestRevision identifies the confirmed request being verified.
		RequestRevision string
		// WorkDir is a private directory for clones, build scratch, outputs and
		// evidence. It must not be shared with the candidate's builder. It is
		// required and must be missing or empty. Retained compiled packages are
		// a separate trusted input supplied to Run or Prepare. A relative path
		// is made absolute against the current directory.
		WorkDir string
		// ReportDir receives the report, named by its digest.
		ReportDir string
		// Tests says who wrote the test program, which decides the
		// environment it starts with (see ProgramEnv): TestsEvaluator for
		// the operator's evaluator, TestsWritten for a test module agents on
		// this machine wrote.
		Tests TestsSource
	}

	// TestsSource says who wrote a plan's test program.
	TestsSource string

	// Stage names the step of Run that stopped a verification.
	Stage string

	// StageError is every error Run returns: the step that failed, a summary
	// that is safe to show builders, and the full error. Err may quote command
	// output and must stay with the evaluator.
	StageError struct {
		// Stage is the step that failed.
		Stage Stage
		// Summary describes the failure in one sentence without command output.
		Summary string
		// Err is the full error.
		Err error
	}

	// Prepared is what Prepare built: the binding of the exact commits and
	// candidate binary digests, the suite program, and the --artifact flags
	// that hand the built candidate binaries to it.
	Prepared struct {
		// Binding names the candidate and suite commits, the request revision
		// and every built binary's digest. Its SuiteID and Environment are
		// empty until the suite describes itself.
		Binding Binding
		// SuiteBin is the built suite program.
		SuiteBin string
		// ArtifactFlags are the suite's --artifact name=path arguments, in
		// binary-name order.
		ArtifactFlags []string
		// EvidenceDir holds every preparation command's output.
		EvidenceDir string
		// TmpDir is the empty folder inside WorkDir that a test program
		// agents wrote gets as TMPDIR in a verification run.
		TmpDir string
		// runner runs Run's later suite commands with the same evidence
		// directory.
		runner *runner
	}

	// runner runs the verifier's commands with file-backed output.
	runner struct {
		// evidenceDir receives every command's stdout and stderr.
		evidenceDir string
	}

	// buildToolchain is what a candidate build runs with.
	buildToolchain struct {
		// goroot is the resolved toolchain the candidate module selects.
		goroot string
		// modCache is the module cache holding the candidate's dependencies.
		modCache string
		// workDir is the build's operation-owned TMPDIR and staging parent.
		workDir string
	}
)

const (
	// StageCheckout covers the plan, the work directory and the candidate and
	// suite checkouts.
	StageCheckout Stage = "checkout"
	// StageBuild covers preparing and building the candidate and the suite.
	StageBuild Stage = "build"
	// StageDescribe is the suite's --describe.
	StageDescribe Stage = "describe"
	// StageSuite is running the suite against the candidate.
	StageSuite Stage = "suite"
	// StageReport is assembling and saving the report.
	StageReport Stage = "report"

	// holdoutTag marks a scenario whose details builders must never see.
	holdoutTag = "holdout"
	// maxErrorExcerpt bounds how much of a failed command's output an error
	// message quotes; the full output stays in the evidence directory.
	maxErrorExcerpt = 4 << 10
	// suiteStopGrace is how long the suite gets to stop its candidates and
	// report after SIGTERM before it is killed.
	suiteStopGrace = 30 * time.Second
	// summaryPlan is the builder-safe summary of an invalid verification plan
	// or work directory, which the operator configures.
	summaryPlan = "The operator's verification configuration is invalid."
	// summaryCandidateBuild is the builder-safe summary of a candidate that
	// did not build.
	summaryCandidateBuild = "The candidate did not build."
	// summarySuiteBuild is the builder-safe summary of an evaluator suite that
	// did not build.
	summarySuiteBuild = "The evaluator suite did not build."
)

// commitID matches a full commit ID in either Git object format: 40 hex digits
// for SHA-1 repositories, 64 for SHA-256 ones. The checkout then requires HEAD
// to equal it, which only the full ID in the repository's own format can.
var commitID = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// IsCommitID reports whether commit is a full commit ID in either Git object
// format: 40 lowercase hex digits for SHA-1 repositories, 64 for SHA-256 ones.
func IsCommitID(commit string) bool {
	return commitID.MatchString(commit)
}

// Run verifies a plan. Prepare first clones the candidate at its exact commit
// and builds its binaries into WorkDir/candidate-bin, then clones the suite at
// its exact commit and builds it into WorkDir/suite-bin. Run then asks the
// suite for its declaration, runs it against the built binaries, and returns
// the CP4 report bound to everything that was tested. The report is also
// written to ReportDir as <digest>.json.
//
// Both builds compile offline through candidate: they download nothing
// (GOPROXY=off, -mod=readonly) and use the toolchain and module cache
// prepareBuild resolved. A build is an ordinary process of the user, so a
// committed link or replace directive that leaves the checkout reads the
// user's files; the suite's module may replace sibling modules in its own
// checkout. Run refuses a WorkDir that already has contents. The suite is
// checked out only after the candidate is built. Workspace settings and
// workspace package-loading output are discarded because they may contain
// operator secrets. Checkout, standalone dependency and toolchain operations,
// and evaluator protocol diagnostics stay in private evidence files; outward
// errors name those files without quoting their output.
// Compiler failures retain bounded build-output excerpts.
//
// Run starts its builds, the suite and the candidates the suite runs in the
// caller's process group and does not stop processes they leave behind: its
// caller must run it inside an owning group that it stops afterwards, as
// `hbay verify-run execute` does under its supervisor.
//
// Preparation runs with the process environment. environ is the
// environment the test program's own is derived from (ProgramEnv): the
// process environment plus the provider request settings the run was
// given, which the caller removed from its own process so preparation
// never sees them.
//
// Every error is a *StageError naming the step that failed and a summary
// safe to show builders; its Err may quote command output and stays with the
// evaluator. A suite that produces no decodable eval.Report is an error.
// Otherwise the report keeps the suite's evidence and exit status: a suite
// that ran and failed, or that exited abnormally after writing partial
// results, yields a report that does not pass.
func Run(ctx context.Context, p Plan, environ []string, buildCacheDir string) (Report, error) {
	prep, err := Prepare(ctx, p, buildCacheDir)
	if err != nil {
		return Report{}, err
	}
	return runPrepared(ctx, p, prep, environ)
}

// CandidateBinariesDir names the output folder Prepare owns inside the run's
// work directory. Run readers use the same location to identify the binaries
// retained after verification; callers supply the recorded absolute WorkDir.
func CandidateBinariesDir(workDir string) string {
	return filepath.Join(workDir, "candidate-bin")
}

// Prepare runs the trusted preparation Run starts with: it checks the plan,
// clones the candidate at its exact commit and builds its binaries into
// WorkDir/candidate-bin, then clones the suite at its exact commit and builds
// it into WorkDir/suite-bin, with the same build and evidence
// rules Run documents. Run calls it before describing and running the suite;
// a live CP3 target calls it before starting the suite in --live mode, so
// both build exactly the same way. The caller owns the process group, as for
// Run. buildCacheDir is required and retains compiled packages independently
// of WorkDir. It is a trusted compiler input: same-user code can change it
// and affect a later binary or test program without changing the selected source.
// Every error is a *StageError.
func Prepare(ctx context.Context, p Plan, buildCacheDir string) (Prepared, error) {
	if buildCacheDir == "" {
		return Prepared{}, stageError(StageCheckout, summaryPlan, errors.New("build cache directory is required"))
	}
	for name, commit := range map[string]string{"candidate": p.CandidateCommit, "suite": p.SuiteCommit} {
		if !IsCommitID(commit) {
			return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("%s commit %q is not a full commit ID", name, commit))
		}
	}
	for name, pkg := range p.Binaries {
		if err := ValidateArtifactName(name); err != nil {
			return Prepared{}, stageError(StageCheckout, summaryPlan, err)
		}
		if err := validatePackagePath(pkg); err != nil {
			return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("binary %q: %w", name, err))
		}
	}
	if err := validatePackagePath(p.SuiteCommand); err != nil {
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("suite command: %w", err))
	}
	if err := p.Tests.Check(); err != nil {
		return Prepared{}, stageError(StageCheckout, summaryPlan, err)
	}
	if p.WorkDir == "" {
		return Prepared{}, stageError(StageCheckout, summaryPlan, errors.New("work directory is required"))
	}
	workDir, err := filepath.Abs(p.WorkDir)
	if err != nil {
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("work directory: %w", err))
	}
	p.WorkDir = workDir
	switch entries, err := os.ReadDir(p.WorkDir); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("work directory: %w", err))
	case len(entries) > 0:
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("work directory %s must be missing or empty", p.WorkDir))
	}
	evidenceDir := filepath.Join(p.WorkDir, "evidence")
	candidateBinDir := CandidateBinariesDir(p.WorkDir)
	suiteBinDir := filepath.Join(p.WorkDir, "suite-bin")
	tmpDir := filepath.Join(p.WorkDir, "tmp")
	for _, dir := range []string{evidenceDir, candidateBinDir, suiteBinDir, tmpDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("create %s: %w", dir, err))
		}
	}
	r := &runner{evidenceDir: evidenceDir}
	binding := Binding{
		CandidateCommit: p.CandidateCommit,
		RequestRevision: p.RequestRevision,
		SuiteCommit:     p.SuiteCommit,
		Artifacts:       map[string]string{},
	}

	candidateDir := filepath.Join(p.WorkDir, "candidate")
	if err := r.checkout(ctx, "candidate", p.CandidateRepo, p.CandidateCommit, candidateDir); err != nil {
		return Prepared{}, stageError(StageCheckout, "Checking out the candidate commit failed.", err)
	}
	moduleDir, err := resolveInside(candidateDir, p.CandidateModule)
	if err != nil {
		return Prepared{}, stageError(StageCheckout, "The candidate commit does not contain the configured module.", fmt.Errorf("candidate module %q: %w", p.CandidateModule, err))
	}
	for name, pkg := range p.Binaries {
		if _, err := resolveInside(moduleDir, pkg); err != nil {
			return Prepared{}, stageError(StageCheckout, "The candidate commit does not contain a configured binary package.", fmt.Errorf("binary %q package %q: %w", name, pkg, err))
		}
	}
	toolchain, err := r.prepareBuild(ctx, "candidate", candidateDir, moduleDir, p.WorkDir,
		slices.Sorted(maps.Values(p.Binaries)))
	if err != nil {
		return Prepared{}, stageError(StageBuild, summaryCandidateBuild, err)
	}
	var artifactFlags []string
	for _, name := range slices.Sorted(maps.Keys(p.Binaries)) {
		out := filepath.Join(candidateBinDir, name)
		logFile := filepath.Join(r.evidenceDir, "build-"+name+".log")
		if err := candidate.Build(ctx, candidate.BuildSpec{
			SourceDir: candidateDir, ModuleDir: moduleDir, Package: p.Binaries[name], Out: out,
			GOROOT: toolchain.goroot, ModCache: toolchain.modCache, WorkDir: toolchain.workDir,
			BuildCacheDir: buildCacheDir, LogFile: logFile,
		}, (*exec.Cmd).Run); err != nil {
			// The private error carries the end of the compiler's output and
			// the log that holds all of it; the summary builders see does not.
			return Prepared{}, stageError(StageBuild, summaryCandidateBuild,
				fmt.Errorf("build candidate %s: %w%s", name, err, excerptFile(logFile, "build output")))
		}
		digest, err := sha256File(out)
		if err != nil {
			return Prepared{}, stageError(StageBuild, summaryCandidateBuild, err)
		}
		binding.Artifacts[name] = digest
		artifactFlags = append(artifactFlags, "--artifact", name+"="+out)
	}

	suiteDir := filepath.Join(p.WorkDir, "suite")
	if err := r.checkout(ctx, "suite", p.SuiteRepo, p.SuiteCommit, suiteDir); err != nil {
		return Prepared{}, stageError(StageCheckout, "Checking out the evaluator suite failed.", err)
	}
	suiteModuleDir, err := resolveInside(suiteDir, p.SuiteModule)
	if err != nil {
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("suite module %q: %w", p.SuiteModule, err))
	}
	if _, err := resolveInside(suiteModuleDir, p.SuiteCommand); err != nil {
		return Prepared{}, stageError(StageCheckout, summaryPlan, fmt.Errorf("suite command %q: %w", p.SuiteCommand, err))
	}
	// Written tests are an independent module. Stored evaluators retain the
	// checkout's workspace for lookup, download and compile alike.
	buildSuite := candidate.BuildModule
	var suiteToolchain buildToolchain
	if p.Tests == TestsWritten {
		suiteToolchain, err = r.prepareGoBuild(ctx, "suite", suiteModuleDir, p.WorkDir, "off", []string{p.SuiteCommand})
	} else {
		suiteToolchain, err = r.prepareBuild(ctx, "suite", suiteDir, suiteModuleDir, p.WorkDir, []string{p.SuiteCommand})
		buildSuite = candidate.Build
	}
	if err != nil {
		return Prepared{}, stageError(StageBuild, summarySuiteBuild, err)
	}
	// Keep the full checkout for both sources, including local replacements
	// into sibling modules, even when the written test module ignores go.work.
	suiteBin := filepath.Join(suiteBinDir, "suite")
	suiteLog := filepath.Join(r.evidenceDir, "build-suite.log")
	if err := buildSuite(ctx, candidate.BuildSpec{
		SourceDir: suiteDir, ModuleDir: suiteModuleDir, Package: p.SuiteCommand, Out: suiteBin,
		GOROOT: suiteToolchain.goroot, ModCache: suiteToolchain.modCache, WorkDir: suiteToolchain.workDir,
		BuildCacheDir: buildCacheDir, LogFile: suiteLog,
	}, (*exec.Cmd).Run); err != nil {
		// As for the candidate: compiler output goes only into the private error.
		return Prepared{}, stageError(StageBuild, summarySuiteBuild,
			fmt.Errorf("build suite: %w%s", err, excerptFile(suiteLog, "build output")))
	}
	return Prepared{
		Binding:       binding,
		SuiteBin:      suiteBin,
		ArtifactFlags: artifactFlags,
		EvidenceDir:   evidenceDir,
		TmpDir:        tmpDir,
		runner:        r,
	}, nil
}

// Error returns the step, the summary and the full error.
func (e *StageError) Error() string {
	return fmt.Sprintf("%s: %s %v", e.Stage, e.Summary, e.Err)
}

// Unwrap returns the full error.
func (e *StageError) Unwrap() error {
	return e.Err
}

// runPrepared is Run after Prepare: it describes and runs the suite prep
// built for p, both with the environment ProgramEnv gives p's test program
// from environ, then assembles the report and saves it in p.ReportDir.
func runPrepared(ctx context.Context, p Plan, prep Prepared, environ []string) (Report, error) {
	r, binding, suiteBin, artifactFlags := prep.runner, prep.Binding, prep.SuiteBin, prep.ArtifactFlags
	env := ProgramEnv(p, environ, prep.TmpDir)
	declared, err := r.describe(ctx, env, suiteBin)
	if err != nil {
		return Report{}, stageError(StageDescribe, "The evaluator suite did not describe its scenarios.", err)
	}
	binding.SuiteID, binding.Environment = declared.ID, declared.Environment
	evidence, exit, err := r.runSuite(ctx, env, suiteBin, artifactFlags)
	if err != nil {
		return Report{}, stageError(StageSuite, "The evaluator suite ended without a report.", err)
	}
	report, err := Assemble(binding, declared, evidence, exit)
	if err != nil {
		return Report{}, stageError(StageReport, "The evaluator suite's report does not match its declaration or the binaries it was given.", err)
	}
	if err := writeReport(p.ReportDir, report); err != nil {
		return Report{}, stageError(StageReport, "Saving the verification report failed.", err)
	}
	return report, nil
}

// resolveInside resolves rel, a path relative to the directory root, following
// every symbolic link in root and rel, and returns the resolved path. It fails
// when the path does not exist or is not root itself or inside it, so a
// committed link cannot point a build at source outside the commit the report
// names.
func resolveInside(root, rel string) (string, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(base, rel))
	if err != nil {
		return "", err
	}
	inner, err := filepath.Rel(base, dir)
	if err != nil || !filepath.IsLocal(inner) {
		return "", fmt.Errorf("resolves to %s, outside %s", dir, base)
	}
	return dir, nil
}

// validatePackagePath rejects a Go package argument that is not "." or a
// "./"-relative directory, such as a standard library import path or a go
// build flag, before it reaches `go build`.
func validatePackagePath(pkg string) error {
	if pkg != "." && !strings.HasPrefix(pkg, "./") {
		return fmt.Errorf("package path %q must be \".\" or start with \"./\"", pkg)
	}
	return nil
}

// ValidateArtifactName returns an error unless name can serve both as the
// artifact ID passed to the suite as --artifact name=path and as one file name
// inside WorkDir/candidate-bin and WorkDir/evidence: non-empty, not . or ..,
// and free of /, \, = and NUL.
func ValidateArtifactName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return fmt.Errorf("binary name %q is not a valid artifact name", name)
	case strings.ContainsAny(name, "/\\=\x00"):
		return fmt.Errorf("binary name %q must not contain /, \\, = or NUL", name)
	}
	return nil
}

// checkout clones repo into dir and checks out commit, detached. The source
// repository is only read; clone hooks and templates are disabled. Its git
// commands run without the variables that point git at another repository
// (gitenv.WithoutRedirects), so only repo and dir are ever used, whatever
// repository the process's own environment names. Every other variable
// stays, protocol, identity and credential settings among them, because a
// plan's repository may be remote (hbay verify -candidate-repo <url>).
func (r *runner) checkout(ctx context.Context, label, repo, commit, dir string) error {
	env := gitenv.WithoutRedirects(os.Environ())
	if err := r.run(ctx, "clone-"+label, "", env, "git", "-c", "core.hooksPath=/dev/null",
		"clone", "--quiet", "--no-checkout", "--template=", repo, dir); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	if err := r.run(ctx, "checkout-"+label, dir, env, "git", "-c", "core.hooksPath=/dev/null",
		"-c", "advice.detachedHead=false", "checkout", "--quiet", "--detach", commit+"^{commit}"); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	if err := r.run(ctx, "head-"+label, dir, env, "git", "rev-parse", "HEAD"); err != nil {
		return fmt.Errorf("check out %s: %w", label, err)
	}
	head, err := os.ReadFile(filepath.Join(r.evidenceDir, "head-"+label+".stdout"))
	if err != nil {
		return fmt.Errorf("read %s head: %w", label, err)
	}
	if got := strings.TrimSpace(string(head)); got != commit {
		return fmt.Errorf("check out %s: HEAD does not match the requested commit; diagnostic output is private in %s",
			label, filepath.Join(r.evidenceDir, "head-"+label+".stdout"))
	}
	return nil
}

// describe runs the suite's --describe with env and converts it into the
// declared scenario list.
func (r *runner) describe(ctx context.Context, env []string, suiteBin string) (Suite, error) {
	if err := r.run(ctx, "describe", "", env, suiteBin, "--describe"); err != nil {
		return Suite{}, fmt.Errorf("describe suite: %w", err)
	}
	var d suite.Declaration
	if err := decodeFile(filepath.Join(r.evidenceDir, "describe.stdout"), &d); err != nil {
		return Suite{}, fmt.Errorf("decode suite declaration: malformed output; diagnostic output is private in %s",
			filepath.Join(r.evidenceDir, "describe.stdout"))
	}
	s := Suite{ID: d.ID, Environment: d.Environment}
	for _, sc := range d.Scenarios {
		s.Scenarios = append(s.Scenarios, Scenario{ID: sc.ID, Holdout: slices.Contains(sc.Tags, holdoutTag)})
	}
	return s, nil
}

// runSuite runs the suite with env and returns its eval.Report and exit
// status. The suite and the candidates it starts run in the caller's process
// group, which stops and reaps them (hbay verify-run supervise owns that
// group).
// Cancellation sends SIGTERM to the suite so it can stop its candidates and
// report what it has; it is killed after suiteStopGrace. A report is decoded
// whatever the outcome; a canceled or abnormal end gets exit status -1 and
// never passes. Only a missing or undecodable report is an error.
func (r *runner) runSuite(ctx context.Context, env []string, suiteBin string, artifactFlags []string) (eval.Report, int, error) {
	stdout, stderr, err := r.outputs("suite")
	if err != nil {
		return eval.Report{}, 0, err
	}
	defer stdout.Close()
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, suiteBin, artifactFlags...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// Cancellation asks the suite to stop its candidates and report; it gets
	// suiteStopGrace before it is killed. The suite and whatever it starts
	// stay in the caller's process group, which owns their cleanup.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = suiteStopGrace
	runErr := cmd.Run()
	var report eval.Report
	decodeErr := decodeFile(filepath.Join(r.evidenceDir, "suite.stdout"), &report)
	exit := 0
	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		exit = exitErr.ExitCode()
	default:
		// Canceled or otherwise not a clean exit: whatever the suite reported
		// is kept, and the verification can never pass.
		exit = -1
	}
	if decodeErr != nil {
		if runErr != nil {
			return eval.Report{}, 0, fmt.Errorf("suite ended without a decodable report: %w; diagnostic output is private in %s and %s",
				runErr, stdout.Name(), stderr.Name())
		}
		return eval.Report{}, 0, fmt.Errorf("suite ended without a decodable report: malformed output; diagnostic output is private in %s and %s",
			stdout.Name(), stderr.Name())
	}
	return report, exit, nil
}

// run runs one command with its stdout and stderr in evidence files named by
// label. Only the fixed operation, wrapped process cause and evidence filename
// leave this owner: child output may contain operator credentials. env, when
// non-nil, replaces the inherited environment.
func (r *runner) run(ctx context.Context, label, dir string, env []string, name string, args ...string) error {
	stdout, stderr, err := r.outputs(label)
	if err != nil {
		return err
	}
	defer stdout.Close()
	defer stderr.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w; diagnostic output is private in %s", label, err, stderr.Name())
	}
	return nil
}

// outputs creates the evidence files for one command.
func (r *runner) outputs(label string) (*os.File, *os.File, error) {
	stdout, err := os.Create(filepath.Join(r.evidenceDir, label+".stdout"))
	if err != nil {
		return nil, nil, fmt.Errorf("create evidence file: %w", err)
	}
	stderr, err := os.Create(filepath.Join(r.evidenceDir, label+".stderr"))
	if err != nil {
		_ = stdout.Close()
		return nil, nil, fmt.Errorf("create evidence file: %w", err)
	}
	return stdout, stderr, nil
}

// prepareBuild selects the checkout's workspace and prepares exactly packages,
// the configured compile targets, with the operator's download settings.
// A plan with no candidate targets still resolves its toolchain but loads no
// workspace package. The following offline builds download nothing.
func (r *runner) prepareBuild(
	ctx context.Context, label, sourceDir, moduleDir, workDir string, packages []string,
) (buildToolchain, error) {
	goWork, err := candidate.GoWork(sourceDir, moduleDir)
	if err != nil {
		return buildToolchain{}, fmt.Errorf("find the %s build's workspace: %w", label, err)
	}
	return r.prepareGoBuild(ctx, label, moduleDir, workDir, goWork, packages)
}

// prepareGoBuild keeps standalone module downloads unchanged. In a workspace,
// Go 1.27.3's bare module download expands the module graph, including declared
// versions that a workspace's local modules override for package builds:
// https://go.dev/ref/mod#go-mod-download. Loading the configured packages with
// -deps instead fetches their compile dependencies, without unrelated packages
// or tests: https://pkg.go.dev/cmd/go@go1.27.3#hdr-List_packages_or_modules.
// This parses sources reached through local replacements, including outside
// the checkout, where module download only read go.mod. It compiles or executes
// no checked-out code.
func (r *runner) prepareGoBuild(
	ctx context.Context, label, moduleDir, workDir, goWork string, packages []string,
) (buildToolchain, error) {
	var toolchain buildToolchain
	if goWork == "off" {
		env := append(gitenv.WithoutRedirects(os.Environ()), "GOWORK=off")
		if err := r.run(ctx, "go-mod-download-"+label, moduleDir, env, "go", "mod", "download"); err != nil {
			return buildToolchain{}, fmt.Errorf("download %s modules: %w", label, err)
		}
		if err := r.run(ctx, "go-env-"+label, moduleDir, env, "go", "env", "GOROOT", "GOMODCACHE"); err != nil {
			return buildToolchain{}, fmt.Errorf("resolve %s toolchain: %w", label, err)
		}
		out, err := os.ReadFile(filepath.Join(r.evidenceDir, "go-env-"+label+".stdout"))
		if err != nil {
			return buildToolchain{}, fmt.Errorf("read toolchain: %w", err)
		}
		lines := strings.Fields(string(out))
		if len(lines) != 2 {
			return buildToolchain{}, fmt.Errorf("resolve %s toolchain: malformed Go settings output; diagnostic output is private in %s",
				label, filepath.Join(r.evidenceDir, "go-env-"+label+".stdout"))
		}
		toolchain.goroot, toolchain.modCache = lines[0], lines[1]
	} else {
		var env []string
		var err error
		toolchain, env, err = workspaceBuildEnvironment(ctx, label, moduleDir, goWork)
		if err != nil {
			return buildToolchain{}, err
		}
		if len(packages) > 0 {
			args := append([]string{"list", "-deps"}, packages...)
			cmd := exec.CommandContext(ctx, filepath.Join(toolchain.goroot, "bin", "go"), args...)
			cmd.Dir, cmd.Env = moduleDir, env
			// Like the written-test dependency owner, withhold network command
			// output: proxy and credential helpers may print operator secrets.
			cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
			if err := cmd.Run(); err != nil {
				return buildToolchain{}, fmt.Errorf("download %s modules: %w", label, err)
			}
		}
	}
	toolchain.workDir = filepath.Join(workDir, "build-home-"+label)
	if err := os.MkdirAll(toolchain.workDir, 0o700); err != nil {
		return buildToolchain{}, fmt.Errorf("create build home: %w", err)
	}
	return toolchain, nil
}

// workspaceBuildEnvironment resolves the operator's toolchain and effective
// download policy through Go itself. Credential-bearing JSON stays in memory:
// it is never runner evidence, a log or an error. The selected binary then uses
// the same native/default compiler selectors as candidate.Build, with workspaces
// on, cgo off and read-only manifests. HOME, XDG and credential helpers remain;
// GOENV is disabled only after its effective download settings are retained.
func workspaceBuildEnvironment(
	ctx context.Context, label, moduleDir, goWork string,
) (buildToolchain, []string, error) {
	keys := []string{
		"GOROOT", "GOMODCACHE", "GOPATH", "GOPROXY", "GOSUMDB", "GOPRIVATE",
		"GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOVCS", "GOAUTH", "GOTOOLCHAIN",
	}
	operatorEnv := append(gitenv.WithoutRedirects(os.Environ()), "GOWORK="+goWork)
	cmd := exec.CommandContext(ctx, "go", append([]string{"env", "-json"}, keys...)...)
	cmd.Dir, cmd.Env, cmd.Stderr = moduleDir, operatorEnv, io.Discard
	var output bytes.Buffer
	defer func() {
		clear(output.Bytes())
		output.Reset()
	}()
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return buildToolchain{}, nil, fmt.Errorf("resolve %s toolchain: %w", label, err)
	}
	var settings map[string]string
	decodeErr := json.Unmarshal(output.Bytes(), &settings)
	clear(output.Bytes())
	output.Reset()
	if decodeErr != nil || settings["GOROOT"] == "" || settings["GOMODCACHE"] == "" {
		return buildToolchain{}, nil, fmt.Errorf("resolve %s toolchain: invalid Go settings output", label)
	}
	// These selectors are absent from candidate.Build's explicit environment.
	// Removing them and GOENV lets this selected Go binary own their defaults;
	// an empty override alone would fall through to the persisted user file.
	env := gitenv.WithoutRedirects(operatorEnv,
		"GOENV", "GO111MODULE", "GOOS", "GOARCH", "GOAMD64", "GOARM", "GOARM64",
		"GO386", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOWASM",
		"GOEXPERIMENT", "GOFIPS140", "GODEBUG", "GOFLAGS", "CGO_ENABLED",
	)
	for _, key := range keys {
		if key != "GOTOOLCHAIN" {
			env = append(env, key+"="+settings[key])
		}
	}
	// The initial lookup honors toolchain policy. Pin its resolved binary now,
	// as compilation does, so package loading cannot select another toolchain.
	env = append(env, "GOENV=off", "CGO_ENABLED=0", "GOFLAGS=-mod=readonly", "GOTOOLCHAIN=local")
	return buildToolchain{goroot: settings["GOROOT"], modCache: settings["GOMODCACHE"]}, env, nil
}

// excerptFile returns the end of the evidence file at path, described as
// what, for an error message: a note of truncation, the file's path, and its
// last maxErrorExcerpt bytes. It returns "" when the file is missing, empty
// or unreadable, so an error never points at output that was not written.
func excerptFile(path, what string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	start := max(info.Size()-maxErrorExcerpt, 0)
	b := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(b, start); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	note := ""
	if start > 0 {
		note = " (last " + strconv.Itoa(maxErrorExcerpt) + " bytes)"
	}
	return "\n" + what + note + ", full output in " + path + ":\n" + strings.TrimSpace(string(b))
}

// decodeFile decodes one JSON value from a file.
func decodeFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(v)
}

// writeReport saves the report as <ReportDir>/<digest>.json.
func writeReport(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, r.Digest+".json"), b, 0o600)
}

// sha256File returns the lowercase hex SHA-256 of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stageError wraps err as the *StageError Run returns.
func stageError(stage Stage, summary string, err error) error {
	return &StageError{Stage: stage, Summary: summary, Err: err}
}
