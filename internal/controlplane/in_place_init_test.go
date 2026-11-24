package controlplane

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
	"golang.org/x/mod/module"

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
	shopModule := "example.com/shop"
	result, err := cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{
		ModulePath: &shopModule, Dir: &empty,
	})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(opts.WorkspaceDir, "shop"), result.Dir)
	assert.NoDirExists(t, filepath.Join(opts.WorkspaceDir, ".git"))
	require.NoError(t, cp.Close())
	assert.Equal(t, result.Dir, newControlPlane(t, opts).currentWorkspaceDir())
}

// TestInitProjectDerivesDefaultModulePath verifies omitted module_path
// derivation across in-place and child directories, reserved/version folder
// names, and the 48-character length threshold.
func TestInitProjectDerivesDefaultModulePath(t *testing.T) {
	t.Parallel()
	dot := "."
	child := "billing-api"
	a47 := strings.Repeat("a", 47)
	a48 := strings.Repeat("a", 48)
	a49 := strings.Repeat("a", 49)
	v48 := "v" + strings.Repeat("9", 47)
	v48Want := "v" + strings.Repeat("9", 44) + "svc"
	cases := []struct {
		name        string
		wsName      string
		dir         *string
		wantService string
		wantModule  string
		wantInPlace bool
	}{
		{name: "both_omitted_in_place", wsName: "House-Market", dir: nil, wantService: "housemarket", wantModule: "example.com/housemarket", wantInPlace: true},
		{name: "dot_leading_digits", wsName: "2026-refunds", dir: &dot, wantService: "refunds", wantModule: "example.com/refunds", wantInPlace: true},
		{name: "dot_digits_only_fallback", wsName: "12345", dir: &dot, wantService: "app", wantModule: "example.com/app", wantInPlace: true},
		{name: "dot_go_keyword_suffix", wsName: "default", dir: &dot, wantService: "defaultsvc", wantModule: "example.com/defaultsvc", wantInPlace: true},
		{name: "dot_main_suffix", wsName: "main", dir: &dot, wantService: "mainsvc", wantModule: "example.com/mainsvc", wantInPlace: true},
		{name: "dot_v1_invalid_version_suffix", wsName: "v1", dir: &dot, wantService: "v1svc", wantModule: "example.com/v1svc", wantInPlace: true},
		{name: "dot_v2_major_version_suffix", wsName: "v2", dir: &dot, wantService: "v2svc", wantModule: "example.com/v2svc", wantInPlace: true},
		{name: "dot_windows_reserved_name", wsName: "aux", dir: &dot, wantService: "auxsvc", wantModule: "example.com/auxsvc", wantInPlace: true},
		{name: "len_47_below_limit", wsName: a47, dir: &dot, wantService: a47, wantModule: "example.com/" + a47, wantInPlace: true},
		{name: "len_48_at_limit", wsName: a48, dir: &dot, wantService: a48, wantModule: "example.com/" + a48, wantInPlace: true},
		{name: "len_49_clamped_to_48", wsName: a49, dir: &dot, wantService: a48, wantModule: "example.com/" + a48, wantInPlace: true},
		{name: "len_48_version_clamped_with_svc", wsName: v48, dir: &dot, wantService: v48Want, wantModule: "example.com/" + v48Want, wantInPlace: true},
		{name: "child_dir_derived", wsName: "workspace", dir: &child, wantService: "billingapi", wantModule: "example.com/billingapi", wantInPlace: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := realTempDir(t)
			ws := filepath.Join(root, tc.wsName)
			require.NoError(t, os.Mkdir(ws, 0o755))
			opts := inPlaceInitOptions(t)
			opts.WorkspaceDir = ws
			opts.NewGenerator = func() scaffold.Generator { return &fakeInitGenerator{pkg: tc.wantService} }
			cp := newControlPlane(t, opts)
			res, err := cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{Dir: tc.dir})
			require.NoError(t, err)
			assert.Equal(t, tc.wantService, res.ServiceName)
			assert.LessOrEqual(t, len(res.ServiceName), 48)
			assert.Equal(t, tc.wantModule, res.ModulePath)
			_, pathMajor, splitOK := module.SplitPathVersion(res.ModulePath)
			assert.True(t, splitOK)
			assert.Empty(t, pathMajor)
			wantDir := ws
			if !tc.wantInPlace {
				wantDir = filepath.Join(ws, *tc.dir)
			}
			assert.Equal(t, wantDir, res.Dir)
		})
	}
}

// TestInitProjectWithoutAVersionNamesTheRestart checks the refusal of a server
// started from a surla build without a module version. The server keeps that
// version until it stops, and a new surla refuses to attach to an older server,
// so the step must stop the server and restart the coding agent before the
// retry; the guide cannot pass surla init's -surla-version flag.
func TestInitProjectWithoutAVersionNamesTheRestart(t *testing.T) {
	opts := inPlaceInitOptions(t)
	opts.SurlaVersion = ""
	cp, err := New(opts)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cp.Close()) })
	root := "."
	_, err = cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{Dir: &root})
	var refusal *goa.ServiceError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "not_implemented", refusal.Name)
	for _, step := range []string{
		"install surla from a tagged or pseudo-versioned module release",
		"stop this project's server with surla stop in the project folder",
		"restart your coding agent so its Surla connection starts the new surla",
		"then call init_project again",
	} {
		assert.Contains(t, refusal.Message, step)
	}
	assert.NotContains(t, refusal.Message, "-surla-version")
	entries, err := os.ReadDir(opts.WorkspaceDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused init writes nothing")
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
	shopModule := "example.com/shop"
	return cp.InitProject(inPlaceTestGuide(), &gensurla.InitProjectPayload{
		ModulePath: &shopModule, Dir: &root,
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

// TestInitProjectReportsItsPhaseOnTheChangeList follows the change list while
// init_project runs: each command the starter preparation runs happens under
// a phase the list names, the list's version changes with the phase so an
// open page learns of it, and the phase is gone when the initialization ends,
// whether it succeeded or failed.
func TestInitProjectReportsItsPhaseOnTheChangeList(t *testing.T) {
	t.Parallel()
	user := WithPrincipal(context.Background(), Principal{ID: "alice", CallerID: "session", Kind: CallerUser})
	for _, outcome := range []string{"succeeds", "fails"} {
		t.Run(outcome, func(t *testing.T) {
			opts := inPlaceInitOptions(t)
			var cp *ControlPlane
			var phases, versions []string
			run := opts.InitRunner
			opts.InitRunner = func(ctx context.Context, dir, logPath string, args ...string) error {
				list, err := cp.ListChanges(user)
				if err != nil {
					return err
				}
				if list.InitializingPhase == nil {
					return errors.New("a preparation command ran without a phase on the change list")
				}
				if len(phases) == 0 || phases[len(phases)-1] != *list.InitializingPhase {
					phases = append(phases, *list.InitializingPhase)
					versions = append(versions, list.Version)
				}
				if outcome == "fails" && len(phases) == 2 {
					return errors.New("the generator's toolchain is missing")
				}
				return run(ctx, dir, logPath, args...)
			}
			cp = newControlPlane(t, opts)
			before, err := cp.ListChanges(user)
			require.NoError(t, err)
			assert.Nil(t, before.InitializingPhase)

			_, err = initInPlaceForTest(cp)
			if outcome == "fails" {
				require.Error(t, err)
				assert.Equal(t, []string{"Resolving the service's Go modules", "Building and testing the service"}, phases)
			} else {
				require.NoError(t, err)
				assert.Equal(t, []string{
					"Resolving the service's Go modules", "Building and testing the service",
					"Resolving the test module's Go modules", "Building and testing the test module",
					"Committing the starter project",
				}, phases, "generation runs through the generator, not a command")
			}
			seen := map[string]bool{before.Version: true}
			for _, version := range versions {
				assert.False(t, seen[version], "each phase gives the list a new version")
				seen[version] = true
			}
			after, err := cp.ListChanges(user)
			require.NoError(t, err)
			assert.Nil(t, after.InitializingPhase)
		})
	}
}
