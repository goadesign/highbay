// This file checks provenance ordering, recovery state and immutable design text.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// codecMadeFrom supplies fresh maps, including distinct versions entered together.
func codecMadeFrom() madeFromRecord {
	return madeFromRecord{
		Write: 3, Start: madeFromStart{Write: 1, ProjectCommit: strings.Repeat("a", 40)},
		Entries: map[string][]madeFromVersion{"design/types.go": {
			{Version: strings.Repeat("a", 64), Entered: 1},
			{Version: strings.Repeat("b", 64), Entered: 1},
			{Version: absentDesignVersion, Entered: 3},
		}},
		Taken: map[string]madeFromTake{"design/types.go": {Version: absentDesignVersion, Entered: 3, ProjectCommit: strings.Repeat("a", 40)}},
	}
}

// TestMadeFromRecordStrictDecode permits interrupted additions/deletions while
// refusing contradictory entry order, versions, drop identities and write bounds.
func TestMadeFromRecordStrictDecode(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*madeFromRecord)
		valid bool
	}{
		{name: "same-write versions are valid", valid: true},
		{name: "original folder identity", valid: true, edit: func(r *madeFromRecord) {
			r.Start.FolderVersions = map[string]string{"design/types.go": strings.Repeat("b", 64)}
		}},
		{name: "invalid original folder path", edit: func(r *madeFromRecord) {
			r.Start.FolderVersions = map[string]string{"../outside": absentDesignVersion}
		}},
		{name: "invalid original folder version", edit: func(r *madeFromRecord) {
			r.Start.FolderVersions = map[string]string{"design/types.go": "bad"}
		}},
		{name: "invalid take commit", edit: func(r *madeFromRecord) {
			take := r.Taken["design/types.go"]
			take.ProjectCommit = "HEAD"
			r.Taken["design/types.go"] = take
		}},
		{name: "empty take after starting commit", edit: func(r *madeFromRecord) {
			take := r.Taken["design/types.go"]
			take.ProjectCommit = ""
			r.Taken["design/types.go"] = take
		}},
		{name: "empty take before first commit", valid: true, edit: func(r *madeFromRecord) {
			r.Start.ProjectCommit = ""
			take := r.Taken["design/types.go"]
			take.ProjectCommit = ""
			r.Taken["design/types.go"] = take
		}},
		{name: "invalid pending commit", edit: func(r *madeFromRecord) {
			r.Pending = &pendingDraftWrite{File: "design/types.go", OutsideVersion: absentDesignVersion, Before: absentDesignVersion, Result: absentDesignVersion, ProjectCommit: "HEAD"}
		}},
		{name: "take survives later revision re-entry", valid: true, edit: func(r *madeFromRecord) {
			r.Write = 4
			r.Entries["design/types.go"][2].Entered = 4
			r.Taken["design/types.go"] = madeFromTake{Version: absentDesignVersion, Entered: 2, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "empty initial history", valid: true, edit: func(r *madeFromRecord) { r.Entries, r.Taken = nil, nil }},
		{name: "pending addition not yet entered", valid: true, edit: func(r *madeFromRecord) {
			r.Pending = &pendingDraftWrite{File: "design/new.go", OutsideVersion: strings.Repeat("c", 64), Before: absentDesignVersion, Result: strings.Repeat("d", 64), ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "pending deletion", valid: true, edit: func(r *madeFromRecord) {
			r.Pending = &pendingDraftWrite{File: "design/types.go", OutsideVersion: absentDesignVersion, Before: strings.Repeat("b", 64), Result: absentDesignVersion, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "dropped before branches", valid: true, edit: func(r *madeFromRecord) {
			r.Start.Dropped = &droppedBuild{ChangeID: "refund", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 2}
		}},
		{name: "zero start", edit: func(r *madeFromRecord) { r.Start.Write = 0 }},
		{name: "start after write", edit: func(r *madeFromRecord) { r.Write = 0 }},
		{name: "entry after write", edit: func(r *madeFromRecord) { r.Write = 2 }},
		{name: "decreasing entries", edit: func(r *madeFromRecord) { r.Entries["design/types.go"][1].Entered = 0 }},
		{name: "duplicate version", edit: func(r *madeFromRecord) { r.Entries["design/types.go"][1].Version = strings.Repeat("a", 64) }},
		{name: "invalid version", edit: func(r *madeFromRecord) { r.Entries["design/types.go"][0].Version = "unknown" }},
		{name: "invalid project commit", edit: func(r *madeFromRecord) { r.Start.ProjectCommit = "HEAD" }},
		{name: "empty file history", edit: func(r *madeFromRecord) { r.Entries["design/types.go"] = nil }},
		{name: "path traversal", edit: func(r *madeFromRecord) { r.Entries["design/../secret.go"] = r.Entries["design/types.go"] }},
		{name: "test file", edit: func(r *madeFromRecord) { r.Entries["design/types_test.go"] = r.Entries["design/types.go"] }},
		{name: "testdata", edit: func(r *madeFromRecord) { r.Entries["design/testdata/types.go"] = r.Entries["design/types.go"] }},
		{name: "taken not entered", edit: func(r *madeFromRecord) {
			r.Taken["design/types.go"] = madeFromTake{Version: strings.Repeat("c", 64), Entered: 2, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "taken after last version entry", edit: func(r *madeFromRecord) {
			r.Taken["design/types.go"] = madeFromTake{Version: strings.Repeat("b", 64), Entered: 2, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "taken after write", edit: func(r *madeFromRecord) {
			r.Taken["design/types.go"] = madeFromTake{Version: absentDesignVersion, Entered: 4, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "invalid pending path", edit: func(r *madeFromRecord) {
			r.Pending = &pendingDraftWrite{File: "../x.go", OutsideVersion: absentDesignVersion, Before: absentDesignVersion, Result: absentDesignVersion, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "invalid pending before", edit: func(r *madeFromRecord) {
			r.Pending = &pendingDraftWrite{File: "design/x.go", OutsideVersion: absentDesignVersion, Before: "", Result: absentDesignVersion, ProjectCommit: strings.Repeat("a", 40)}
		}},
		{name: "drop lacks number", edit: func(r *madeFromRecord) {
			r.Start.Dropped = &droppedBuild{ChangeID: "refund", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
		}},
		{name: "drop foreign branch", edit: func(r *madeFromRecord) {
			r.Start.Dropped = &droppedBuild{ChangeID: "refund", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 2, BranchTips: map[string]string{"surla/build-1": strings.Repeat("a", 40)}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := codecMadeFrom()
			if tc.edit != nil {
				tc.edit(&saved)
			}
			raw, err := json.Marshal(saved)
			require.NoError(t, err)
			_, err = decodeMadeFrom(raw)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
	valid, err := json.Marshal(codecMadeFrom())
	require.NoError(t, err)
	for _, raw := range [][]byte{
		[]byte(strings.TrimSuffix(string(valid), "}") + `,"unknown":true}`),
		[]byte(strings.Replace(string(valid), `"start":{`, `"start":{"unknown":true,`, 1)),
		append(append([]byte{}, valid...), []byte(" null")...),
	} {
		_, err := decodeMadeFrom(raw)
		assert.Error(t, err)
	}
}

// TestMadeFromRecordReloadAndFailedSave proves storage, not merely JSON readback.
func TestMadeFromRecordReloadAndFailedSave(t *testing.T) {
	dir := t.TempDir()
	store, err := openCardStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.close()) })
	_, found, err := store.loadMadeFrom()
	require.NoError(t, err)
	assert.False(t, found)
	saved := codecMadeFrom()
	saved.Start.Dropped = &droppedBuild{ChangeID: "refund", Number: 2,
		StartedAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 2*60*60)),
		BranchTips: map[string]string{"surla/build-2": strings.Repeat("a", 40)}}
	require.NoError(t, store.saveMadeFrom(saved))
	assert.NotEqual(t, time.UTC, saved.Start.Dropped.StartedAt.Location(), "saving must not mutate its caller's snapshot")
	guard := filepath.Join(t.TempDir(), "guard")
	require.NoError(t, os.WriteFile(guard, []byte("untouched"), 0o600))
	require.NoError(t, os.Symlink(guard, filepath.Join(store.dir, madeFromStagingFile)))
	replacement := saved
	replacement.Write++
	assert.Error(t, store.saveMadeFrom(replacement))
	guardText, err := os.ReadFile(guard)
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(guardText))
	require.NoError(t, store.close())
	reopened, err := openCardStore(dir)
	require.NoError(t, err)
	defer func() { assert.NoError(t, reopened.close()) }()
	actual, found, err := reopened.loadMadeFrom()
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, saved.Write, actual.Write)
	assert.True(t, saved.Start.Dropped.StartedAt.Equal(actual.Start.Dropped.StartedAt))
	assert.Equal(t, time.UTC, actual.Start.Dropped.StartedAt.Location())
	assert.Equal(t, saved.Entries, actual.Entries)
	assert.Equal(t, saved.Taken, actual.Taken)
}

// TestDesignVersionStorage verifies canonical/hash identity across reopen,
// immutable corruption handling and absence distinct from a missing required blob.
func TestDesignVersionStorage(t *testing.T) {
	dir := t.TempDir()
	store, err := openCardStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.close()) })
	text := "package design\n\ntype Value string\n"
	version, err := store.saveDesignVersion(text)
	require.NoError(t, err)
	assert.Equal(t, designversion.Digest(text), version)
	again, err := store.saveDesignVersion(text)
	require.NoError(t, err)
	assert.Equal(t, version, again)
	_, err = store.saveDesignVersion("package design;type Value string")
	assert.ErrorContains(t, err, "gofmt")
	_, err = store.saveDesignVersion("not Go source")
	assert.Error(t, err)
	_, present, err := store.loadDesignVersion(absentDesignVersion)
	require.NoError(t, err)
	assert.False(t, present)
	_, _, err = store.loadDesignVersion(strings.Repeat("e", 64))
	assert.ErrorIs(t, err, os.ErrNotExist)
	_, _, err = store.loadDesignVersion("../outside")
	assert.Error(t, err)
	require.NoError(t, store.close())
	reopened, err := openCardStore(dir)
	require.NoError(t, err)
	defer func() { assert.NoError(t, reopened.close()) }()
	actual, present, err := reopened.loadDesignVersion(version)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, text, actual)
	require.NoError(t, os.WriteFile(filepath.Join(dir, designVersionsDirectory, version),
		[]byte("package design\n\ntype Different bool\n"), 0o600))
	_, _, err = reopened.loadDesignVersion(version)
	assert.ErrorContains(t, err, "digest")
	_, err = reopened.saveDesignVersion(text)
	assert.Error(t, err, "a corrupt existing blob must not be overwritten implicitly")
}

// TestDesignVersionRefusesLinks keeps both directory and text paths inside
// the owner, including failed staging writes that leave existing text intact.
func TestDesignVersionRefusesLinks(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		store, err := openCardStore(t.TempDir())
		require.NoError(t, err)
		defer func() { assert.NoError(t, store.close()) }()
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(store.dir, designVersionsDirectory)))
		_, err = store.saveDesignVersion("package design\n")
		assert.Error(t, err)
		entries, err := os.ReadDir(outside)
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
	t.Run("blob and staging", func(t *testing.T) {
		store, err := openCardStore(t.TempDir())
		require.NoError(t, err)
		defer func() { assert.NoError(t, store.close()) }()
		text := "package design\n"
		version, err := store.saveDesignVersion(text)
		require.NoError(t, err)
		outside := filepath.Join(t.TempDir(), "guard")
		require.NoError(t, os.WriteFile(outside, []byte(text), 0o600))
		linkedVersion := strings.Repeat("f", 64)
		require.NoError(t, os.Symlink(outside, filepath.Join(store.dir, designVersionsDirectory, linkedVersion)))
		_, _, err = store.loadDesignVersion(linkedVersion)
		assert.Error(t, err)
		require.NoError(t, os.Symlink(outside, filepath.Join(store.dir, designVersionsDirectory, designVersionStagingFile)))
		_, err = store.saveDesignVersion("package design\n\ntype Added string\n")
		assert.Error(t, err)
		actual, found, err := store.loadDesignVersion(version)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, text, actual)
		guard, err := os.ReadFile(outside)
		require.NoError(t, err)
		assert.Equal(t, text, string(guard))
	})
}

// TestDesignVersionAndRecordFIFOStaging bounds each write in a separate trusted
// test process, so a blocking-open regression cannot leave a stuck goroutine.
// A reader distinguishes nonblocking-open alone from rejecting the descriptor
// before any record or text can be sent into the pipe.
func TestDesignVersionAndRecordFIFOStaging(t *testing.T) {
	for _, kind := range []string{"build", "provenance", "version"} {
		for _, reader := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/without reader", true: "/with reader"}[reader], func(t *testing.T) {
				dir := t.TempDir()
				store, err := openCardStore(dir)
				require.NoError(t, err)
				build, provenance := codecOpenBuild(), codecMadeFrom()
				text := "package design\n"
				require.NoError(t, store.saveOpenBuild(build))
				require.NoError(t, store.saveMadeFrom(provenance))
				version, err := store.saveDesignVersion(text)
				require.NoError(t, err)
				require.NoError(t, store.close())
				staging := map[string]string{
					"build":      filepath.Join(dir, openBuildStagingFile),
					"provenance": filepath.Join(dir, madeFromStagingFile),
					"version":    filepath.Join(dir, designVersionsDirectory, designVersionStagingFile),
				}[kind]
				require.NoError(t, unix.Mkfifo(staging, 0o600))
				var readPipe *os.File
				if reader {
					fd, err := unix.Open(staging, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
					require.NoError(t, err)
					readPipe = os.NewFile(uintptr(fd), "staging-reader")
					defer func() { assert.NoError(t, readPipe.Close()) }()
				}
				executable, err := os.Executable()
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, executable, "-test.run=^TestRecordStagingFIFOHelper$")
				command.Env = append(os.Environ(), "SURLA_TEST_RECORD_STAGING_DIR="+dir,
					"SURLA_TEST_RECORD_STAGING_KIND="+kind)
				output, err := command.CombinedOutput()
				require.NoError(t, ctx.Err(), "FIFO staging write blocked: %s", output)
				require.NoError(t, err, "FIFO staging helper failed: %s", output)
				if readPipe != nil {
					data := make([]byte, 4096)
					count, err := unix.Read(int(readPipe.Fd()), data)
					assert.True(t, err == nil || errors.Is(err, unix.EAGAIN), "read staging pipe: %v", err)
					assert.Zero(t, count, "staging data escaped into a FIFO before rejection")
				}
				reopened, err := openCardStore(dir)
				require.NoError(t, err)
				defer func() { assert.NoError(t, reopened.close()) }()
				actualBuild, found, err := reopened.loadOpenBuild()
				require.NoError(t, err)
				assert.True(t, found)
				assert.Equal(t, build.Number, actualBuild.Number)
				actualProvenance, found, err := reopened.loadMadeFrom()
				require.NoError(t, err)
				assert.True(t, found)
				assert.Equal(t, provenance.Write, actualProvenance.Write)
				actualText, found, err := reopened.loadDesignVersion(version)
				require.NoError(t, err)
				assert.True(t, found)
				assert.Equal(t, text, actualText)
			})
		}
	}
}

// TestRecordStagingFIFOHelper invokes only the selected store write. Its parent
// supplies and owns the directory, deadline, FIFO readers and cleanup.
func TestRecordStagingFIFOHelper(t *testing.T) {
	dir := os.Getenv("SURLA_TEST_RECORD_STAGING_DIR")
	if dir == "" {
		t.Skip("runs only as the bounded FIFO staging subprocess")
	}
	store, err := openCardStore(dir)
	require.NoError(t, err)
	defer func() { assert.NoError(t, store.close()) }()
	switch os.Getenv("SURLA_TEST_RECORD_STAGING_KIND") {
	case "build":
		build := codecOpenBuild()
		build.Number++
		err = store.saveOpenBuild(build)
	case "provenance":
		provenance := codecMadeFrom()
		provenance.Write++
		err = store.saveMadeFrom(provenance)
	case "version":
		_, err = store.saveDesignVersion("package design\n\ntype Added string\n")
	default:
		t.Fatal("unknown FIFO staging helper operation")
	}
	assert.Error(t, err, "non-regular staging must be refused")
}
