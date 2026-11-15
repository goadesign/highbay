// This file regenerates the application or public local eval/ module through
// the server's held workspace and the same publisher used by surla gen.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/designversion"
	"github.com/goadesign/surla/internal/genpublish"
	"github.com/goadesign/surla/internal/shquote"
)

// GenerateCode writes the open build's agreed design in the builder's copy,
// compiles an isolated design snapshot, and publishes the generated files.
// It holds the workspace lock, builder turn and root handles through publication
// and cleanup, so a new assignment cannot move the copy while it still writes.
// An error means gen/ was not replaced; after replacement, auxiliary failures
// are warnings in the successful result. It never builds application code or
// runs the module's tests.
func (c *ControlPlane) GenerateCode(
	ctx context.Context, p *gensurla.GenerateCodePayload,
) (res *gensurla.GenerateCodeResult, retErr error) {
	if _, ok := PrincipalFromContext(ctx); !ok {
		return nil, errors.New("authenticated principal is required")
	}
	ctx, span := c.tracer("surla/controlplane").Start(ctx, "surla.controlplane.generate_code")
	defer span.End()
	target := "candidate"
	if p.Target != nil {
		target = *p.Target
	}
	span.SetAttributes(attribute.String("surla.generation.target", target))

	c.workspaceMu.Lock()
	defer c.workspaceMu.Unlock()
	genCtx, cancel := context.WithCancel(ctx)
	stopOnServerClose := context.AfterFunc(c.serverCtx, cancel)
	defer func() {
		stopOnServerClose()
		cancel()
	}()
	// A server that already closed must be refused synchronously, even when
	// AfterFunc has not yet delivered its cancellation callback.
	if err := errors.Join(genCtx.Err(), c.serverCtx.Err()); err != nil {
		return nil, failGenerateFault(span, "code generation was canceled; call again on a running server", err)
	}
	if c.activeWorkspaceDir == "" {
		span.AddEvent("generation_refused", trace.WithAttributes(attribute.String("reason", "no_workspace")))
		return nil, gensurla.MakeNotImplemented(errors.New("no workspace is bound; start Surla from the local project directory"))
	}
	select {
	case c.builderCopyTurn <- struct{}{}:
	default:
		return nil, gensurla.MakeWorkingCopyBusy(errors.New("The builder's working copy is in use; call generate_code again when the current operation finishes."))
	}
	defer func() { <-c.builderCopyTurn }()
	_, project, err := openModuleRootWithIdentity(
		c.activeWorkspaceDir, c.boundWorkspaceDev, c.boundWorkspaceIno,
	)
	if err != nil {
		span.AddEvent("generation_refused", trace.WithAttributes(attribute.String("reason", "workspace_changed")))
		return nil, err
	}
	roots := []*os.Root{project}
	defer func() {
		var closeErr error
		for i := len(roots) - 1; i >= 0; i-- {
			closeErr = errors.Join(closeErr, roots[i].Close())
		}
		if closeErr != nil {
			if res == nil {
				retErr = failGenerateFault(span, "closing the module directory failed", errors.Join(retErr, closeErr))
			} else {
				span.RecordError(closeErr)
				res.Warnings = append(res.Warnings, "gen/ was updated; closing a module directory failed; inspect the server trace for details")
			}
		}
	}()
	ready, err := c.builderCopyReady(genCtx, c.activeWorkspaceDir, "generate_code")
	if err != nil {
		return nil, recordBuildError(span, err, "read builder working copy")
	}
	if !ready.build.inForce {
		return nil, gensurla.MakeConflict(errors.New("The open build is for an earlier agreed revision; ask the user to build the current agreed revision on the views page."))
	}
	moduleDir, workspace, err := openBuildGenerationRoot(project, ready.folder)
	if err != nil {
		return nil, gensurla.MakeUnprocessable(fmt.Errorf("open the builder's module: %w", err))
	}
	roots = append(roots, workspace)
	module := workspace
	var agreed map[string]string
	if target == "eval" {
		module, err = openGenerationEvalModule(workspace)
		if err != nil {
			span.AddEvent("generation_refused", trace.WithAttributes(attribute.String("reason", "invalid_eval_module")))
			return nil, gensurla.MakeUnprocessable(err)
		}
		roots = append(roots, module)
		moduleDir = filepath.Join(moduleDir, "eval")
	} else {
		agreed, err = decodeDesignSource(ready.build.file.DesignSource)
		if err != nil {
			return nil, failGenerateFault(span, "the build's saved design could not be read", err)
		}
		if err := writeBuildDesign(genCtx, module, agreed); err != nil {
			return nil, gensurla.MakeUnprocessable(fmt.Errorf("write the agreed design in your working folder: %w", err))
		}
	}
	if err := reportProgress(genCtx, 0, 3, "Reading the local "+target+" module"); err != nil {
		return nil, failGenerateFault(span, "could not report code-generation progress; call again", err)
	}
	files, err := designeval.ReadModuleSnapshotFromRoot(moduleDir, module)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read local generation module")
		return nil, gensurla.MakeUnprocessable(fmt.Errorf("could not read the local %s module: %w", target, err))
	}
	if _, ok := files["go.mod"]; !ok {
		return nil, gensurla.MakeUnprocessable(fmt.Errorf("the local %s module has no go.mod; create or restore it before generating code", target))
	}
	if target != "eval" {
		if err := buildGenerationModules(agreed, ready.build.file.GoMod, files["go.mod"], ready.folder, ready.goworkOff); err != nil {
			return nil, recordBuildError(span, err, "compare generation modules")
		}
	}
	if err := reportProgress(genCtx, 1, 3, "Compiling the design snapshot and generating code"); err != nil {
		return nil, failGenerateFault(span, "could not report code-generation progress; call again", err)
	}
	generated, err := c.evaluator.Generate(genCtx, files)
	if err != nil {
		message := designeval.GenerationDiagnostic(err) + "\nCorrect any reported local design or dependency problem, or ask the server operator to address compiler setup, storage, or dependency access. Then call generate_code again on the same server."
		return nil, failGenerateFault(span, message, err)
	}
	if err := reportProgress(genCtx, 2, 3, "Publishing generated files"); err != nil {
		return nil, failGenerateFault(span, "could not report code-generation progress; call again", err)
	}
	if err := c.serverCtx.Err(); err != nil {
		return nil, failGenerateFault(span, "code generation was canceled; call again on a running server", err)
	}
	published, err := genpublish.Publish(genCtx, module, generated)
	if err != nil {
		return nil, failGenerateFault(span, "generated files could not be published; inspect the server trace for details", err)
	}
	res = &gensurla.GenerateCodeResult{
		Target: target, FilesWritten: published.WrittenFiles, Warnings: published.Warnings,
	}
	// The service requires both lists even when publication returned no entries.
	if len(res.FilesWritten) == 0 {
		res.FilesWritten = []string{}
	}
	if len(res.Warnings) == 0 {
		res.Warnings = []string{}
	}
	span.AddEvent("generated_files_published", trace.WithAttributes(attribute.Int("files_written", len(res.FilesWritten))))
	if err := reportProgress(genCtx, 3, 3, "Generated files updated; application build and tests still need to run"); err != nil {
		span.RecordError(err)
		res.Warnings = append(res.Warnings, "gen/ was updated; the final progress notification could not be delivered")
	}
	return res, nil
}

// openBuildGenerationRoot walks from the held project without following any
// directory link, then binds the module root to that exact opened directory.
// The returned root is retained through generation; a nested copy path cannot
// redirect writes into the user's project or another directory.
func openBuildGenerationRoot(
	project *os.Root, folder string,
) (dir string, root *os.Root, retErr error) {
	rel, err := filepath.Rel(project.Name(), folder)
	if err != nil || !fs.ValidPath(filepath.ToSlash(rel)) {
		return "", nil, fmt.Errorf("the builder's module is outside the bound project: %s", folder)
	}
	parent, err := project.Open(".")
	if err != nil {
		return "", nil, err
	}
	fd, openErr := openDirAt(int(parent.Fd()), filepath.ToSlash(rel))
	if err := errors.Join(openErr, parent.Close()); err != nil {
		return "", nil, errors.Join(err, closeFD(fd))
	}
	checked := os.NewFile(uintptr(fd), folder)
	defer func() {
		retErr = errors.Join(retErr, checked.Close())
		if retErr != nil && root != nil {
			retErr = errors.Join(retErr, root.Close())
			root = nil
		}
	}()
	before, err := checked.Stat()
	if err != nil {
		return "", nil, err
	}
	dir, root, err = designeval.OpenModuleRoot(folder)
	if err != nil {
		return "", nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return "", root, errors.Join(errors.New("the builder's module changed while opening it; call generate_code again"), err)
	}
	return dir, root, nil
}

// openGenerationEvalModule opens only the public eval/ child of the held
// workspace. The opened handle must identify the same non-symlink directory
// inspected through that parent, so replacement cannot redirect generation.
func openGenerationEvalModule(workspace *os.Root) (*os.Root, error) {
	info, err := workspace.Lstat("eval")
	if err != nil {
		return nil, fmt.Errorf("the local eval/ module is unavailable; create or restore its directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("the local eval/ module must be a real directory, not a symbolic link or file")
	}
	root, err := workspace.OpenRoot("eval")
	if err != nil {
		return nil, fmt.Errorf("open the local eval/ module: %w", err)
	}
	held, err := root.Stat(".")
	if err != nil || !os.SameFile(info, held) {
		return nil, errors.Join(errors.New("the local eval/ module changed while opening it; restore it and call again"), err, root.Close())
	}
	return root, nil
}

// buildGenerationModules returns builder-owned dependency corrections without
// changing either manifest. The saved module is a constructed build invariant;
// malformed builder files remain actionable user input.
func buildGenerationModules(
	design map[string]string, savedText, builderText, folder string, goworkOff bool,
) error {
	saved, err := designversion.Requirements(savedText)
	if err != nil {
		return fmt.Errorf("read the build's saved module: %w", err)
	}
	builder, err := designversion.Requirements(builderText)
	if err != nil {
		return gensurla.MakeUnprocessable(fmt.Errorf("correct go.mod in your working folder: %w", err))
	}
	missing, replacements, err := designversion.MissingModules(design, saved, builder)
	if err != nil {
		return fmt.Errorf("read the build's design imports: %w", err)
	}
	if len(missing) == 0 && len(replacements) == 0 {
		return nil
	}
	prefix := ""
	if goworkOff {
		prefix = "GOWORK=off "
	}
	steps := replacements
	for _, required := range missing {
		steps = append(steps, prefix+"go get "+shquote.Quote(required.String()))
	}
	steps = append(steps, prefix+"go mod tidy")
	return gensurla.MakeMissingModules(fmt.Errorf("The agreed design was written in %s; gen/ was not changed. Correct the dependencies in that working folder, then call generate_code again:\n%s",
		shquote.Quote(folder), strings.Join(steps, "\n")))
}

// failGenerateFault records an operation's raw failure on its span and returns
// the owner-selected recovery message as a server fault. The caller chooses
// which diagnostic details to include.
func failGenerateFault(span trace.Span, message string, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, message)
	return goa.Fault("%s", message)
}
