package controlplane

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/gitenv"
	"github.com/goadesign/surla/internal/scaffold"
)

// TestInPlaceInitPublishesGitLastAndPreservesFiles uses real Git to confirm
// the starter history remains readable from the original workspace after
// restart. The existing scaffold tests cover compilation and execution.
func TestInPlaceInitPublishesGitLastAndPreservesFiles(t *testing.T) {
	t.Parallel()
	opts := inPlaceInitOptions(t)
	for _, name := range []string{".claude", ".codex", ".cursor", ".gemini", ".agents", ".vscode"} {
		dir := filepath.Join(opts.WorkspaceDir, name)
		require.NoError(t, os.Mkdir(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "settings"), []byte(name), 0o600))
	}
	for _, name := range []string{".mcp.json", ".DS_Store"} {
		require.NoError(t, os.WriteFile(filepath.Join(opts.WorkspaceDir, name), []byte(name), 0o600))
	}
	opts.InitRunner = func(ctx context.Context, dir, logPath string, args ...string) error {
		if args[0] != "git" {
			return fakeInitRunner(ctx, dir, logPath, args...)
		}
		out, err := inPlaceTestGit(ctx, dir, args[1:]...).CombinedOutput()
		if err != nil {
			return err
		}
		return os.WriteFile(logPath, out, 0o600)
	}
	cp := newControlPlane(t, opts)
	var pending workspaceFile
	var moves int
	cp.initStepHook = func(step string) {
		switch step {
		case "after_wal":
			pending = pendingInPlaceRecord(t, cp)
		case "after_in_place_move":
			moves++
			if moves < len(pending.Entries) {
				assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
			}
		}
	}
	result, err := initInPlaceForTest(cp)
	require.NoError(t, err)
	assert.Equal(t, opts.WorkspaceDir, result.Dir)
	assert.Equal(t, "shop", result.ServiceName)
	assert.Equal(t, len(pending.Entries), moves)
	require.NotEmpty(t, pending.Entries)
	assert.Equal(t, ".git", pending.Entries[len(pending.Entries)-1].Name)
	assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, "shop"))
	assert.NoDirExists(t, filepath.Join(pending.StageDir, pending.Staging))
	require.NoError(t, cp.Close())

	reopened := newControlPlane(t, opts)
	root, dev, ino := reopened.currentWorkspaceTarget()
	assert.Equal(t, result.Dir, root)
	assert.Equal(t, pending.WSDev, dev)
	assert.Equal(t, pending.WSIno, ino)
	wf, ok, err := reopened.readWorkspaceRecord()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, workspaceFile{Dir: ".", InPlace: true, WSDev: dev, WSIno: ino}, wf)
	head, err := inPlaceTestGit(t.Context(), root, "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	assert.Equal(t, result.Commit, strings.TrimSpace(string(head)))
	tracked, err := inPlaceTestGit(t.Context(), root, "ls-files").Output()
	require.NoError(t, err)
	assert.Contains(t, string(tracked), "go.mod\n")
	for _, name := range []string{
		".claude/settings", ".codex/settings", ".cursor/settings", ".gemini/settings", ".agents/settings",
		".vscode/settings", ".mcp.json", ".DS_Store",
	} {
		content, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		assert.Equal(t, strings.TrimSuffix(name, "/settings"), string(content))
		assert.NotContains(t, string(tracked), name)
	}
	_, err = initInPlaceForTest(reopened)
	assert.True(t, isConflictError(err), "%v", err)
	require.NoError(t, reopened.Close())

	// A new directory at the old path cannot inherit the stored binding.
	require.NoError(t, os.Rename(root, root+"-saved"))
	require.NoError(t, os.Mkdir(root, 0o755))
	_, err = New(opts)
	require.ErrorContains(t, err, "workspace no longer matches its recorded (dev, ino) identity")
	assert.DirExists(t, filepath.Join(root+"-saved", ".git"))
	assert.NoDirExists(t, filepath.Join(root, ".git"))
}

// TestInPlaceInitConflictsBeforeWAL covers the root preflight, including a
// destination that appears during generation and a generated name that
// collides with an otherwise allowed entry.
func TestInPlaceInitConflictsBeforeWAL(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		".git", ".gitignore", "README.md", "AGENTS_QUICKSTART.md", ".surla-work", "go.mod", "notes.txt", ".env",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := inPlaceInitOptions(t)
			opts.InitRunner = func(context.Context, string, string, ...string) error {
				t.Fatal("the preflight should refuse before staging")
				return nil
			}
			cp := newControlPlane(t, opts)
			existing := filepath.Join(opts.WorkspaceDir, name)
			// A dangling Git metadata link also counts as a collision.
			require.NoError(t, os.Symlink("untouched", existing))
			_, err := initInPlaceForTest(cp)
			assert.True(t, isConflictError(err), "%v", err)
			require.ErrorContains(t, err, name)
			target, err := os.Readlink(existing)
			require.NoError(t, err)
			assert.Equal(t, "untouched", target)
			assert.NoFileExists(t, filepath.Join(opts.CardsDir, workspaceRecordFile))
		})
	}
	for _, name := range []string{"notes.txt", ".mcp.json"} {
		t.Run("during_generation_"+name, func(t *testing.T) {
			t.Parallel()
			opts := inPlaceInitOptions(t)
			run := opts.InitRunner
			opts.InitRunner = func(ctx context.Context, dir, logPath string, args ...string) error {
				if args[0] == "git" && slices.Contains(args, "rev-parse") {
					require.NoError(t, os.WriteFile(filepath.Join(opts.WorkspaceDir, name), []byte("user data"), 0o600))
					if name == ".mcp.json" {
						require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("generated"), 0o600))
					}
				}
				return run(ctx, dir, logPath, args...)
			}
			cp := newControlPlane(t, opts)
			_, err := initInPlaceForTest(cp)
			assert.True(t, isConflictError(err), "%v", err)
			assert.ErrorContains(t, err, name)
			got, err := os.ReadFile(filepath.Join(opts.WorkspaceDir, name))
			require.NoError(t, err)
			assert.Equal(t, "user data", string(got))
			assert.NoFileExists(t, filepath.Join(opts.CardsDir, workspaceRecordFile))
			staging, err := os.ReadDir(opts.InitStageDir)
			require.NoError(t, err)
			assert.Empty(t, staging)
		})
	}
}

// TestInPlaceInitRecoversAfterInterruption stops publication either after the
// first rename or after staging cleanup. Restart must accept only manifest
// entries that still match their recorded identities.
func TestInPlaceInitRecoversAfterInterruption(t *testing.T) {
	t.Parallel()
	for _, point := range []string{"after_in_place_move", "after_rename", "after_in_place_stage_removal"} {
		t.Run(point, func(t *testing.T) {
			t.Parallel()
			opts := inPlaceInitOptions(t)
			cp := newControlPlane(t, opts)
			cp.initStepHook = func(step string) {
				if step == point {
					panic(point)
				}
			}
			require.PanicsWithValue(t, point, func() {
				_, err := initInPlaceForTest(cp)
				assert.NoError(t, err)
			})
			wf := pendingInPlaceRecord(t, cp)
			_, err := os.Lstat(filepath.Join(opts.WorkspaceDir, wf.Entries[0].Name))
			require.NoError(t, err)
			if point == "after_in_place_move" {
				assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
			}
			require.NoError(t, cp.Close())

			reopened := newControlPlane(t, opts)
			assert.Equal(t, opts.WorkspaceDir, reopened.currentWorkspaceDir())
			assert.DirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
			assert.FileExists(t, filepath.Join(opts.WorkspaceDir, "go.mod"))
			assert.NoDirExists(t, filepath.Join(wf.StageDir, wf.Staging))
			final, ok, err := reopened.readWorkspaceRecord()
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, workspaceFile{Dir: ".", InPlace: true, WSDev: wf.WSDev, WSIno: wf.WSIno}, final)
		})
	}
}

// TestInPlaceInitRejectsForeignEntry interrupts publication after one move,
// then replaces either that destination or a still-pending destination.
// Recovery refuses both, leaves the foreign entry alone, and can proceed
// once the exact owned state returns.
func TestInPlaceInitRejectsForeignEntry(t *testing.T) {
	t.Parallel()
	for _, replacePublished := range []bool{true, false} {
		name := "pending_destination"
		if replacePublished {
			name = "published_destination"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := inPlaceInitOptions(t)
			cp := newControlPlane(t, opts)
			var changed, saved string
			cp.initStepHook = func(step string) {
				if step != "after_in_place_move" || changed != "" {
					return
				}
				wf := pendingInPlaceRecord(t, cp)
				if replacePublished {
					changed = filepath.Join(opts.WorkspaceDir, wf.Entries[0].Name)
					saved = filepath.Join(opts.InitStageDir, "saved")
					require.NoError(t, os.Rename(changed, saved))
				} else {
					changed = filepath.Join(opts.WorkspaceDir, wf.Entries[1].Name)
				}
				require.NoError(t, os.Symlink("foreign", changed))
			}
			_, err := initInPlaceForTest(cp)
			require.ErrorContains(t, err, "restart Surla")
			pending := pendingInPlaceRecord(t, cp)
			require.NoError(t, cp.Close())

			_, err = New(opts)
			require.ErrorContains(t, err, "no longer matches its recorded identity")
			assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
			assert.DirExists(t, filepath.Join(pending.StageDir, pending.Staging))
			dest, err := os.Readlink(changed)
			require.NoError(t, err)
			assert.Equal(t, "foreign", dest)
			require.NoError(t, os.Remove(changed))
			if replacePublished {
				require.NoError(t, os.Rename(saved, changed))
			}
			reopened := newControlPlane(t, opts)
			assert.Equal(t, opts.WorkspaceDir, reopened.currentWorkspaceDir())
			assert.DirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
		})
	}
}

// TestInPlaceInitRejectsReplacedStagingDirectory verifies recovery refuses a
// symbolic link at the exact stage path even if it points to the owned tree.
func TestInPlaceInitRejectsReplacedStagingDirectory(t *testing.T) {
	t.Parallel()
	opts := inPlaceInitOptions(t)
	cp := newControlPlane(t, opts)
	cp.initStepHook = func(step string) {
		if step == "after_wal" {
			panic(step)
		}
	}
	require.PanicsWithValue(t, "after_wal", func() {
		_, err := initInPlaceForTest(cp)
		assert.NoError(t, err)
	})
	wf := pendingInPlaceRecord(t, cp)
	require.NoError(t, cp.Close())

	stage := filepath.Join(wf.StageDir, wf.Staging)
	saved := filepath.Join(wf.StageDir, "saved")
	require.NoError(t, os.Rename(stage, saved))
	require.NoError(t, os.Symlink(saved, stage))
	_, err := New(opts)
	require.ErrorContains(t, err, "open in-place staging directory")
	got, err := os.Readlink(stage)
	require.NoError(t, err)
	assert.Equal(t, saved, got)
	assert.FileExists(t, filepath.Join(saved, "go.mod"))
	assert.NoFileExists(t, filepath.Join(opts.WorkspaceDir, "go.mod"))

	require.NoError(t, os.Remove(stage))
	require.NoError(t, os.Rename(saved, stage))
	assert.Equal(t, opts.WorkspaceDir, newControlPlane(t, opts).currentWorkspaceDir())
	assert.DirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
}

// TestInitProjectEmptyDirStillUsesChild covers an explicit empty parameter.
// The existing workspace WAL tests verify the legacy on-disk record.
func TestInitProjectEmptyDirStillUsesChild(t *testing.T) {
	t.Parallel()
	opts := inPlaceInitOptions(t)
	cp := newControlPlane(t, opts)
	empty := ""
	result, err := cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{
		ModulePath: "example.com/shop", Dir: &empty,
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(opts.WorkspaceDir, "shop"), result.Dir)
	assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
	require.NoError(t, cp.Close())
	assert.Equal(t, result.Dir, newControlPlane(t, opts).currentWorkspaceDir())
}

// inPlaceInitOptions reuses the scaffold's fake command and generator, and
// creates an empty Git-directory snapshot for replay tests.
func inPlaceInitOptions(t *testing.T) Options {
	t.Helper()
	root := realTempDir(t)
	ws := filepath.Join(root, "workspace")
	require.NoError(t, os.Mkdir(ws, 0o755))
	return Options{
		Team:         testTeamBinding(),
		CardsDir:     filepath.Join(root, "cards"),
		WorkspaceDir: ws,
		InitStageDir: filepath.Join(root, "staging"),
		SurlaVersion: testSurlaVersion,
		NewGenerator: func() scaffold.Generator { return &fakeInitGenerator{pkg: "shop"} },
		InitRunner: func(ctx context.Context, dir, logPath string, args ...string) error {
			if args[0] == "git" && slices.Contains(args, "init") {
				if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
					return err
				}
			}
			return fakeInitRunner(ctx, dir, logPath, args...)
		},
	}
}

// inPlaceTestGit isolates fixture commands from local Git identity and signing
// settings while using the same Git flags as the scaffold in its runner.
func inPlaceTestGit(ctx context.Context, dir string, args ...string) *exec.Cmd {
	args = append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(gitenv.WithoutRedirects(os.Environ(),
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Surla Test", "GIT_AUTHOR_EMAIL=author@example.com",
		"GIT_COMMITTER_NAME=Surla Test", "GIT_COMMITTER_EMAIL=author@example.com")
	return cmd
}

// inPlaceTestGuide supplies the principal that owns workspace initialization.
func inPlaceTestGuide() context.Context {
	return WithPrincipal(context.Background(), Principal{ID: "alice", CallerID: "guide", Kind: CallerGuide})
}

// initInPlaceForTest calls the service entry point with a root destination.
func initInPlaceForTest(cp *ControlPlane) (*gensurla.InitProjectResult, error) {
	root := "."
	return cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{
		ModulePath: "example.com/shop", Dir: &root,
	})
}

// pendingInPlaceRecord inspects the actual durable record at a fault seam.
func pendingInPlaceRecord(t *testing.T, cp *ControlPlane) workspaceFile {
	t.Helper()
	wf, present, err := cp.readWorkspaceRecord()
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, wf.InPlace)
	require.NotEmpty(t, wf.Staging)
	require.Greater(t, len(wf.Entries), 2)
	return wf
}
