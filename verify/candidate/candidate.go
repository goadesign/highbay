// Package candidate builds and runs the code Highbay's coding agents write.
// Build, BuildTest and BuildModule compile a module offline to a staging path
// and publish the result as a regular file. Pin copies a binary into a read-only,
// digest-named store, and Exec, Run and Output start only a pinned binary
// whose bytes still match its digest, with an explicit environment and its own
// state directory as working directory, HOME and TMPDIR.
//
// These are ordinary processes of the user who runs Highbay: they can read and
// write the user's files, use the network and start children; nothing here
// confines them. The caller decides which process group owns them: Build,
// BuildTest, BuildModule, Run and Output run the prepared command with the
// caller's run function, and Exec starts it in the caller's own process group. A process
// that starts its own session or process group leaves that group, and no
// caller's cleanup stops it.
package candidate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goadesign/highbay/internal/abspath"
	"goa.design/goa-ai/eval"
)

type (
	// Artifact is one candidate binary pinned into verifier-owned, read-only
	// storage. Only Pin creates Artifacts, so an Artifact always names a copy
	// the candidate's builder never wrote and the verifier never rewrites.
	Artifact struct {
		// path is the absolute, symlink-free path of the pinned copy.
		path string
		// digest is the lowercase hex SHA-256 of the pinned copy's bytes.
		digest string
	}

	// Spec describes how to run one candidate process.
	Spec struct {
		// Args are the command-line arguments after the binary name.
		Args []string
		// Env is the complete environment as KEY=VALUE entries, after HOME
		// and TMPDIR; nothing is inherited from the caller.
		Env []string
		// StateDir is the process's working directory, HOME and TMPDIR. It
		// must exist.
		StateDir string
		// LogFile receives the process's diagnostics: its combined stdout and
		// stderr under Exec and Run, only its stderr under Output. It is owned
		// by the caller; Exec, Run and Output create or truncate it.
		LogFile string
	}

	// Process is a candidate process Exec started. Its output goes to
	// the caller's log file; the verifier keeps none of it in memory.
	Process struct {
		// cmd is the started command.
		cmd *exec.Cmd
		// logFile is the path of the process's output file.
		logFile string
		// done is closed after the process exited.
		done chan struct{}
		// waitErr is the process's exit error, valid after done is closed.
		waitErr error
	}

	// BuildSpec describes one offline compile of candidate source.
	BuildSpec struct {
		// SourceDir is the caller's source root, such as the candidate module
		// or a whole checkout whose modules replace each other. A go.work file
		// in ModuleDir, in SourceDir or in a folder between them applies to
		// Build and BuildTest; one above SourceDir does not (GoWork).
		// BuildModule ignores workspace files and retains the same source root.
		SourceDir string
		// ModuleDir is the directory of the module being built. It must
		// resolve inside SourceDir.
		ModuleDir string
		// Package is the package to build, relative to ModuleDir, such as
		// "./cmd/orders".
		Package string
		// Out is the output binary path; its directory must exist.
		Out string
		// GOROOT is the resolved Go toolchain root the build runs with.
		GOROOT string
		// ModCache is the already populated module cache; the build
		// downloads nothing into it.
		ModCache string
		// BuildCacheDir is the required Go build cache. It may be shared by
		// concurrent builds on one machine using a local filesystem. Build
		// creates it when missing; Go owns its entries and maintenance.
		BuildCacheDir string
		// WorkDir is an operation-owned writable directory for TMPDIR and the
		// staged output binary. It is separate from retained package storage.
		WorkDir string
		// LogFile receives build output and cache-initialization failures; it
		// must lie outside ModuleDir.
		LogFile string
	}

	// resolvedBuild is a BuildSpec whose directories resolveBuild has resolved to
	// symlink-free paths, with Out in its resolved output directory. Only
	// resolveBuild creates one.
	resolvedBuild struct {
		// spec holds the resolved paths; spec.Out is the absolute output file.
		spec BuildSpec
	}

	// limitedBuffer keeps a candidate's standard output for Output, up to a
	// fixed size. Only the copy into it writes; Output reads it after the
	// process and that copy have finished.
	limitedBuffer struct {
		// buf holds the bytes written so far.
		buf bytes.Buffer
		// limit is the most bytes buf may hold.
		limit int
		// overflow is set once a write would have exceeded limit.
		overflow bool
	}
)

var (
	// ErrDigestMismatch reports that the binary on disk is not the pinned artifact.
	ErrDigestMismatch = errors.New("candidate binary does not match its pinned digest")

	// ErrOutputTooLarge reports a candidate whose standard output exceeded the
	// limit its Output caller set; the output read so far is discarded.
	ErrOutputTooLarge = errors.New("candidate standard output exceeds its limit")
)

// Pin copies the binary at src into store, the verifier's own artifact
// directory, as <store>/<sha256>/<name> with read-only permissions, and returns
// the pinned Artifact. The digest is computed over the bytes written to the
// pinned copy, so later changes to src cannot affect it. Pinning the same bytes
// twice reuses the existing copy after re-checking its digest.
func Pin(store, src string) (Artifact, error) {
	in, err := os.Open(src)
	if err != nil {
		return Artifact{}, fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(store, 0o700); err != nil {
		return Artifact{}, fmt.Errorf("create artifact store: %w", err)
	}
	tmp, err := os.CreateTemp(store, ".pin-*")
	if err != nil {
		return Artifact{}, fmt.Errorf("create pinned copy: %w", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), in); err != nil {
		_ = tmp.Close()
		return Artifact{}, fmt.Errorf("copy %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return Artifact{}, fmt.Errorf("close pinned copy: %w", err)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	dir := filepath.Join(store, digest)
	path := filepath.Join(dir, filepath.Base(src))
	if _, err := os.Stat(path); err == nil {
		got, err := fileDigest(path)
		if err != nil || got != digest {
			return Artifact{}, fmt.Errorf("%w: existing pinned copy %s", ErrDigestMismatch, path)
		}
		return resolvedArtifact(path, digest)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Artifact{}, fmt.Errorf("create pinned directory: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o500); err != nil {
		return Artifact{}, fmt.Errorf("protect pinned copy: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return Artifact{}, fmt.Errorf("place pinned copy: %w", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		return Artifact{}, fmt.Errorf("protect pinned directory: %w", err)
	}
	return resolvedArtifact(path, digest)
}

// RemoveStore deletes an artifact store created by Pin, restoring write
// permission on its read-only directories first. Callers remove the store
// once every process started from its artifacts has exited.
func RemoveStore(store string) error {
	err := filepath.WalkDir(store, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(path, 0o700)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("unprotect artifact store: %w", err)
	}
	if err := os.RemoveAll(store); err != nil {
		return fmt.Errorf("remove artifact store: %w", err)
	}
	return nil
}

// Digest returns the pinned binary's lowercase hex SHA-256.
func (a Artifact) Digest() string {
	return a.digest
}

// Evidence returns the record an evaluator suite attaches to a scenario result
// to state that the scenario ran this pinned binary. name is the binary's name
// in the verification plan: the name in `highbay verify -binary name=package`,
// which the suite receives as `--artifact name=path`. highbay verify accepts a
// scenario only when its result carries one such record for every bound
// binary, with the digest the verifier measured. For example, the binary bound
// as orders with digest 9f2c… yields
// eval.Artifact{Name: "candidate:orders", URI: "sha256:9f2c…"}.
func (a Artifact) Evidence(name string) eval.Artifact {
	return eval.Artifact{Name: "candidate:" + name, URI: "sha256:" + a.digest}
}

// Exec verifies the artifact's digest and starts it as described by spec, in
// the caller's process group, and returns at once. Relative paths resolve from
// the caller's working directory before the candidate changes into its state
// directory. The process is killed when ctx ends. Processes it starts stay in
// the caller's process group: a caller that must stop them owns that group, as
// a verification run's supervisor does.
func Exec(ctx context.Context, a Artifact, spec Spec) (*Process, error) {
	cmd, logFile, err := prepare(ctx, a, spec)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start candidate %s: %w", a.path, err), logFile.Close())
	}
	p := &Process{cmd: cmd, logFile: logFile.Name(), done: make(chan struct{})}
	go func() {
		p.waitErr = errors.Join(cmd.Wait(), logFile.Close())
		close(p.done)
	}()
	return p, nil
}

// Run runs the artifact to completion as described by spec, with its standard
// output and standard error in spec.LogFile, and returns its exit error. It
// admits the artifact as Exec does and returns every admission error before
// the candidate starts. run runs the prepared command: the caller decides which
// process group owns it and the processes it starts, as for Build. A caller
// without an owning group passes a runner that gives the command its own group
// and stops it.
func Run(ctx context.Context, a Artifact, spec Spec, run func(*exec.Cmd) error) error {
	cmd, logFile, err := prepare(ctx, a, spec)
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	return errors.Join(run(cmd), logFile.Close())
}

// Output runs the artifact to completion like Run and returns what it printed
// on standard output, so the caller reads a program's answer exactly instead
// of picking it out of a log: spec.LogFile receives only standard error.
// Standard output may hold at most maxBytes; one byte more stops the read and
// fails with ErrOutputTooLarge, which the candidate sees as a closed pipe. A
// candidate that exits unsuccessfully returns its standard output with the
// exit error, which wraps an *exec.ExitError. Every admission error is returned
// before the candidate starts. run runs the prepared command, as for Run.
//
// Output suits short programs whose answer fits in memory, such as an
// evaluator suite's --describe.
func Output(ctx context.Context, a Artifact, spec Spec, maxBytes int, run func(*exec.Cmd) error) ([]byte, error) {
	cmd, logFile, err := prepare(ctx, a, spec)
	if err != nil {
		return nil, err
	}
	out := &limitedBuffer{limit: maxBytes}
	cmd.Stdout, cmd.Stderr = out, logFile
	runErr := errors.Join(run(cmd), logFile.Close())
	if out.overflow {
		return nil, fmt.Errorf("%w of %d bytes", ErrOutputTooLarge, maxBytes)
	}
	return out.buf.Bytes(), runErr
}

// ExcerptFile returns at most maxBytes from the end of the log file at path,
// such as a Spec.LogFile after Exec, Run or Output, and whether earlier output
// was left out. It reads only those bytes, however large the file is, so a
// candidate that writes without limit to its log cannot make the caller hold
// it in memory.
func ExcerptFile(path string, maxBytes int64) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat log file: %w", err)
	}
	startOffset := max(info.Size()-maxBytes, 0)
	b := make([]byte, info.Size()-startOffset)
	if _, err := f.ReadAt(b, startOffset); err != nil && !errors.Is(err, io.EOF) {
		return "", false, fmt.Errorf("read log file: %w", err)
	}
	return string(b), startOffset > 0, nil
}

// Build compiles source offline. The Go toolchain may start its own compiler,
// assembler and linker. The fixed environment (buildEnv) keeps the build from
// downloading anything, switching toolchains, using cgo, reading the user's Go
// settings or applying a go.work file above SourceDir. The build is an
// ordinary process of the user: a directive in candidate source, such as an
// assembly #include of an outside file, a link or a replace directive that
// leaves the module, reads whatever the user can read. Build compiles to a
// fresh staging path inside WorkDir and publishes the verified regular file to
// spec.Out, so, while WorkDir holds no link into spec.Out's directory, the
// toolchain never writes there and only a regular file is published there.
//
// Build trusts GOROOT, ModCache and BuildCacheDir as compiler inputs. Go
// checks cached package existence and recorded size, not correspondence to
// selected source. Same-user code can plant a package that a later build links,
// just as it can change the module cache, toolchain or executables. A measured
// binary digest does not authenticate that source correspondence. Deleting the
// build cache forces recompilation but does not authenticate the other inputs.
// Build does not check that WorkDir is fresh; the caller owns its scratch lifetime.
//
// run runs the prepared build command to completion. The caller decides which
// process group owns the compiler, assembler and linker the build starts: a
// verification passes (*exec.Cmd).Run so they stay in the verification's
// group, which its supervisor stops; a caller without such a group passes a
// runner that gives the build its own group and stops it.
func Build(ctx context.Context, spec BuildSpec, run func(*exec.Cmd) error) error {
	b, err := resolveBuild(spec)
	if err != nil {
		return err
	}
	return b.build(ctx, workFile(b.spec.SourceDir, b.spec.ModuleDir), run)
}

// BuildModule compiles a standalone module offline with GOWORK=off, ignoring
// go.work files both inside the module and elsewhere in SourceDir. Highbay's
// report checker and written-test verification use it so the module's go.mod
// and go.sum own its dependencies. SourceDir remains the whole checkout, so
// local replacements can still read sibling modules. Path validation, fixed
// offline environment, staging, publication, runner lifetime and errors are
// otherwise the same as Build.
func BuildModule(ctx context.Context, spec BuildSpec, run func(*exec.Cmd) error) error {
	b, err := resolveBuild(spec)
	if err != nil {
		return err
	}
	return b.build(ctx, "off", run)
}

// BuildTest compiles the tests of one candidate package into a single test
// binary at spec.Out, with the same environment, staging and process-group
// contract as Build. It runs `go test -c -trimpath -vet=off`, so go vet
// findings do not fail it, and a compile error in the package or its tests
// fails it with the toolchain output in spec.LogFile. Test-only imports must
// already be in go.mod, go.sum and ModCache, as for any Build.
//
// spec.Package must name one package directory inside ModuleDir: "." or a
// path starting with "./" and containing no "..." pattern. Anything else, such
// as an import path, "all", or a value the go command would read as a flag
// such as -o or -c, is rejected before the toolchain runs.
//
// spec.Out must not exist when BuildTest is called. BuildTest compiles to a
// fresh staging path in WorkDir and publishes the result to spec.Out only when
// the toolchain produced a test binary, so other files written during the build
// cannot be mistaken for the test binary or overwritten by it. BuildTest
// returns true when it wrote the test binary, a regular file at spec.Out. A
// package without _test.go files is not an error: BuildTest returns false and a
// nil error and writes no test binary.
//
// The caller runs the result as a candidate: pin it with Pin and start it
// with Exec, Run or Output, passing test flags such as -test.run and
// -test.timeout in Spec.Args. The test binary is an ordinary process of the
// user: a test may start child processes, use the network and read the user's
// files.
func BuildTest(ctx context.Context, spec BuildSpec, run func(*exec.Cmd) error) (bool, error) {
	if pkg := spec.Package; (pkg != "." && !strings.HasPrefix(pkg, "./")) || !filepath.IsLocal(pkg) || strings.Contains(pkg, "...") {
		return false, fmt.Errorf("test build package %q is not one package path", spec.Package)
	}
	b, err := resolveBuild(spec)
	if err != nil {
		return false, err
	}
	pkgDir := filepath.Join(b.spec.ModuleDir, filepath.FromSlash(spec.Package))
	switch pkgInfo, err := os.Stat(pkgDir); {
	case err != nil:
		return false, fmt.Errorf("inspect test package directory %s: %w", spec.Package, err)
	case !pkgInfo.IsDir():
		return false, fmt.Errorf("test build package %q is not a directory", spec.Package)
	}
	out := b.spec.Out
	switch _, err := os.Lstat(out); {
	case err == nil:
		return false, fmt.Errorf("test binary path %s must not exist before the build", out)
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("inspect test binary path %s: %w", out, err)
	}
	stageDir, err := os.MkdirTemp(b.spec.WorkDir, ".highbay-stage-*")
	if err != nil {
		return false, fmt.Errorf("create test build staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stageOut := filepath.Join(stageDir, filepath.Base(out))
	if err := b.goRun(ctx, run, workFile(b.spec.SourceDir, b.spec.ModuleDir), stageOut, "test", "-c", "-trimpath", "-vet=off"); err != nil {
		return false, fmt.Errorf("test build of %s: %w", spec.Package, err)
	}
	return publishStagedTestBinary(stageOut, out)
}

// GoWork returns the GOWORK value Build and BuildTest give the build of the
// module at moduleDir from the source root sourceDir: the go.work file the go
// command finds searching upward from moduleDir when it lies in moduleDir, in
// sourceDir or in a folder between them, and "off" otherwise, so a go.work
// above the source root never applies. A caller that downloads the module's
// dependencies before the offline build gives the download this value, so
// both select the same modules. moduleDir must resolve inside sourceDir.
func GoWork(sourceDir, moduleDir string) (string, error) {
	src, err := abspath.Eval(sourceDir)
	if err != nil {
		return "", fmt.Errorf("resolve source directory: %w", err)
	}
	mod, err := abspath.Eval(moduleDir)
	if err != nil {
		return "", fmt.Errorf("resolve module directory: %w", err)
	}
	if err := checkInside(src, mod); err != nil {
		return "", err
	}
	return workFile(src, mod), nil
}

// Kill sends SIGKILL to the started process and waits for it to exit.
// Processes it started are not killed: they stay in the caller's process
// group, which the caller stops.
func (p *Process) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("kill candidate: %w", err)
	}
	<-p.done
	return nil
}

// Wait waits for the process to exit and returns its exit error.
func (p *Process) Wait() error {
	<-p.done
	return p.waitErr
}

// Done is closed once the process exited.
func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Excerpt returns at most maxBytes from the end of the process's output so
// far, and whether earlier output was left out.
func (p *Process) Excerpt(maxBytes int64) (string, bool, error) {
	return ExcerptFile(p.logFile, maxBytes)
}

// Write keeps p, or fails without keeping any of it when buf would then exceed
// limit. The failure stops the copy from the candidate's standard output.
func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.overflow = true
		return 0, ErrOutputTooLarge
	}
	return b.buf.Write(p)
}

// prepare verifies the artifact, resolves spec's state directory and log file,
// opens the log, and returns the command that runs the artifact with spec's
// arguments, its state directory as working directory, and HOME, TMPDIR and
// spec.Env as its whole environment. The caller wires the command's output,
// starts or runs it, and closes the log.
func prepare(ctx context.Context, a Artifact, spec Spec) (*exec.Cmd, *os.File, error) {
	if a.path == "" {
		return nil, nil, errors.New("artifact was not pinned")
	}
	digest, err := fileDigest(a.path)
	if err != nil {
		return nil, nil, err
	}
	if digest != a.digest {
		return nil, nil, fmt.Errorf("%w: %s is %s, pinned %s", ErrDigestMismatch, a.path, digest, a.digest)
	}
	stateDir, err := abspath.Eval(spec.StateDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve state directory: %w", err)
	}
	logPath, err := abspath.EvalMissing(spec.LogFile)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve log file %q: %w", spec.LogFile, err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}
	cmd := exec.CommandContext(ctx, a.path, spec.Args...)
	cmd.Dir = stateDir
	cmd.Env = append([]string{"HOME=" + stateDir, "TMPDIR=" + stateDir}, spec.Env...)
	return cmd, logFile, nil
}

// resolveBuild resolves every directory in spec to its symlink-free path and
// places Out in the resolved output directory, so the build, its log and its
// diagnostics name physical paths.
func resolveBuild(spec BuildSpec) (resolvedBuild, error) {
	if spec.BuildCacheDir == "" {
		return resolvedBuild{}, errors.New("build cache directory is required")
	}
	cache, err := abspath.EvalMissing(spec.BuildCacheDir)
	if err != nil {
		return resolvedBuild{}, fmt.Errorf("resolve build cache directory: %w", err)
	}
	spec.BuildCacheDir = cache
	paths := map[string]*string{
		"source": &spec.SourceDir, "module": &spec.ModuleDir, "goroot": &spec.GOROOT,
		"modcache": &spec.ModCache, "work": &spec.WorkDir,
	}
	for name, p := range paths {
		resolved, err := abspath.Eval(*p)
		if err != nil {
			return resolvedBuild{}, fmt.Errorf("resolve %s directory: %w", name, err)
		}
		*p = resolved
	}
	if err := checkInside(spec.SourceDir, spec.ModuleDir); err != nil {
		return resolvedBuild{}, err
	}
	if spec.Out == "" {
		return resolvedBuild{}, fmt.Errorf("resolve output directory: %w", os.ErrNotExist)
	}
	absOut, err := filepath.Abs(spec.Out)
	if err != nil {
		return resolvedBuild{}, fmt.Errorf("resolve output directory: %w", err)
	}
	outDir, err := abspath.Eval(filepath.Dir(absOut))
	if err != nil {
		return resolvedBuild{}, fmt.Errorf("resolve output directory: %w", err)
	}
	spec.Out = filepath.Join(outDir, filepath.Base(absOut))
	return resolvedBuild{spec: spec}, nil
}

// publishStagedOutput opens stagedOut with O_RDONLY|O_NOFOLLOW|O_CLOEXEC,
// verifies via fstat on the open descriptor that it is a regular file, and
// atomically publishes its bytes to outPath.
func publishStagedOutput(stagedOut, outPath string) error {
	_, err := publishStagedOutputAllowMissing(stagedOut, outPath, false)
	return err
}

// publishStagedTestBinary opens stagedOut with O_RDONLY|O_NOFOLLOW|O_CLOEXEC,
// returns (false, nil) when stagedOut was not created (a package without tests),
// and otherwise publishes the verified regular file to outPath without
// replacing an existing file at outPath.
func publishStagedTestBinary(stagedOut, outPath string) (bool, error) {
	return publishStagedOutputAllowMissing(stagedOut, outPath, true)
}

// publishStagedOutputAllowMissing publishes stagedOut to outPath. When
// allowMissing is true, a missing stagedOut returns (false, nil) and
// publishing refuses to overwrite an existing destination file.
func publishStagedOutputAllowMissing(stagedOut, outPath string, allowMissing bool) (bool, error) {
	fd, err := syscall.Open(stagedOut, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if allowMissing && errors.Is(err, syscall.ENOENT) {
			return false, nil
		}
		return false, fmt.Errorf("open staged build output: %w", err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		syscall.Close(fd)
		return false, fmt.Errorf("stat staged build output: %w", err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		syscall.Close(fd)
		return false, fmt.Errorf("staged build output %s is not a regular file (mode %#o)", outPath, st.Mode)
	}
	srcFile := os.NewFile(uintptr(fd), stagedOut)
	defer srcFile.Close()

	tmp, err := os.CreateTemp(filepath.Dir(outPath), ".highbay-out-*")
	if err != nil {
		return false, fmt.Errorf("create output binary %s: %w", outPath, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := io.Copy(tmp, srcFile); err != nil {
		tmp.Close()
		return false, fmt.Errorf("write output binary %s: %w", outPath, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return false, fmt.Errorf("chmod output binary %s: %w", outPath, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close output binary %s: %w", outPath, err)
	}
	if allowMissing {
		if err := os.Link(tmpName, outPath); err != nil {
			return false, fmt.Errorf("publish test binary %s: %w", outPath, err)
		}
		return true, nil
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return false, fmt.Errorf("publish output binary %s: %w", outPath, err)
	}
	return true, nil
}

// build owns the shared compile, staging and publication of Build and
// BuildModule. goWork is the fixed-off or checkout-selected value of its entry.
func (b resolvedBuild) build(ctx context.Context, goWork string, run func(*exec.Cmd) error) error {
	stageDir, err := os.MkdirTemp(b.spec.WorkDir, ".highbay-stage-*")
	if err != nil {
		return fmt.Errorf("create build staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stageOut := filepath.Join(stageDir, filepath.Base(b.spec.Out))
	if err := b.goRun(ctx, run, goWork, stageOut, "build", "-trimpath"); err != nil {
		return fmt.Errorf("build of %s: %w", b.spec.Package, err)
	}
	if err := publishStagedOutput(stageOut, b.spec.Out); err != nil {
		return fmt.Errorf("build of %s: %w", b.spec.Package, err)
	}
	return nil
}

// goRun runs `go <args> -o <outPath> <Package>` from ModuleDir with the
// toolchain's output in LogFile. It returns run's error unwrapped, an "open
// build log" error when LogFile cannot be opened, or an error when opening
// LogFile resolves to the same file as spec.Out. The fixed environment keeps
// the build offline (GOPROXY=off), on the selected toolchain (GOTOOLCHAIN=local),
// unable to change go.mod (-mod=readonly), with cgo off as Highbay's builds have
// always had it (turning it on would link C code into binaries that never had
// it), with the entry-selected workspace or fixed off for BuildModule, and
// with TMPDIR in WorkDir, GOCACHE in BuildCacheDir and no HOME, so os.UserConfigDir leaves Go
// telemetry disabled without starting its detached child process. A build gets
// -buildvcs=false, so it never runs git and stamps no version-control facts.
func (b resolvedBuild) goRun(
	ctx context.Context,
	run func(*exec.Cmd) error,
	goWork, outPath string,
	args ...string,
) error {
	spec := b.spec
	_, outStatErr := os.Lstat(spec.Out)
	logFile, err := os.OpenFile(spec.LogFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open build log: %w", err)
	}
	defer logFile.Close()
	logInfo, err := logFile.Stat()
	if err != nil {
		return fmt.Errorf("stat build log: %w", err)
	}
	if outInfo, err := os.Stat(spec.Out); err == nil && os.SameFile(logInfo, outInfo) {
		aliasErr := fmt.Errorf("build log %s must not be the output binary %s", spec.LogFile, spec.Out)
		if errors.Is(outStatErr, os.ErrNotExist) {
			if rmErr := os.Remove(spec.Out); rmErr != nil {
				return errors.Join(aliasErr, rmErr)
			}
		}
		return aliasErr
	}
	// Highbay owns the cache directory's private mode, including recreation
	// after an idle deletion. Go owns only its entries and maintenance.
	if err := os.MkdirAll(spec.BuildCacheDir, 0o700); err != nil {
		cacheErr := fmt.Errorf("create build cache directory: %w", err)
		_, logErr := fmt.Fprintln(logFile, cacheErr)
		return errors.Join(cacheErr, logErr)
	}
	goArgs := append([]string{}, args...)
	if args[0] == "build" {
		goArgs = append(goArgs, "-buildvcs=false")
	}
	goArgs = append(goArgs, "-o", outPath, spec.Package)
	cmd := exec.CommandContext(ctx, filepath.Join(spec.GOROOT, "bin", "go"), goArgs...)
	cmd.Dir = spec.ModuleDir
	cmd.Env = b.buildEnv(goWork)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	return run(cmd)
}

// buildEnv fixes the offline build environment with its entry's workspace.
func (b resolvedBuild) buildEnv(goWork string) []string {
	spec := b.spec
	return []string{
		"PATH=" + filepath.Join(spec.GOROOT, "bin") + ":/usr/bin:/bin",
		"TMPDIR=" + spec.WorkDir,
		"GOROOT=" + spec.GOROOT,
		"GOCACHE=" + spec.BuildCacheDir,
		"GOMODCACHE=" + spec.ModCache,
		"GOTOOLCHAIN=local",
		"GOWORK=" + goWork,
		"GOFLAGS=-mod=readonly",
		"GOPROXY=off",
		"GONOSUMDB=*",
		"CGO_ENABLED=0",
	}
}

// checkInside returns an error unless the resolved moduleDir is sourceDir or
// lies inside it.
func checkInside(sourceDir, moduleDir string) error {
	if rel, err := filepath.Rel(sourceDir, moduleDir); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("module directory %s is outside source directory %s", moduleDir, sourceDir)
	}
	return nil
}

// workFile returns the go.work file the go command would find searching upward
// from the resolved moduleDir, stopping after the resolved sourceDir, which
// contains it, or "off" when that search finds none. Like the go command, it
// takes only a go.work that exists and is not a directory.
func workFile(sourceDir, moduleDir string) string {
	for dir := moduleDir; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, "go.work")
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
		if dir == sourceDir || filepath.Dir(dir) == dir {
			return "off"
		}
	}
}

// fileDigest returns the lowercase hex SHA-256 of the file's bytes.
func fileDigest(path string) (string, error) {
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

// resolvedArtifact returns the Artifact for a pinned copy with an absolute,
// symlink-free path, so a caller changing directories cannot change the binary
// the process executes.
func resolvedArtifact(path, digest string) (Artifact, error) {
	resolved, err := abspath.Eval(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("resolve pinned copy: %w", err)
	}
	return Artifact{path: resolved, digest: digest}, nil
}
