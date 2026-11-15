// This file checks the verification and approval entry gates without executing
// candidate programs. Real Git branches supply the immutable build evidence.
package controlplane

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
)

// TestBuilderCopyV1VerifyEntry checks that both test sources require the open
// build and its read builder copy before accepting a commit from that build.
func TestBuilderCopyV1VerifyEntry(t *testing.T) {
	for _, stored := range []bool{false, true} {
		profile := "written tests"
		if stored {
			profile = "stored evaluator"
		}
		t.Run(profile, func(t *testing.T) {
			for _, scenario := range []string{"valid", "base", "off branch", "copy off branch", "index lock", "unknown commit", "unread", "no build", "dropped", "foreign work", "copy busy"} {
				t.Run(scenario, func(t *testing.T) {
					cp, card, build, candidate, builder := builderCopyV1Fixture(t, stored)
					workspace := cp.currentWorkspaceDir()
					want := ""
					var lock string
					var beforeLock []byte
					switch scenario {
					case "base":
						candidate, want = build.Base, "holds no builder work"
					case "unknown commit":
						candidate, want = strings.Repeat("f", 40), "is not in the project's repository"
					case "off branch":
						productionBuildGit(t, builder, "reset", "--hard", build.Base)
						want = "is not on surla/build-1"
					case "copy off branch":
						productionBuildGit(t, builder, "checkout", "-q", "-b", "agent-work")
						want = "A worker's working copy is not on its build's own branch"
					case "index lock":
						lock = filepath.Join(productionBuildGit(t, builder, "rev-parse", "--absolute-git-dir"), "index.lock")
						require.NoError(t, os.WriteFile(lock, nil, 0o600))
						beforeLock = readBuildFile(t, cp, card.ChangeID)
						want = "A worker's working copy is not on its build's own branch"
					case "unread":
						build.Builder.Read = false
						require.NoError(t, cp.cards.saveOpenBuild(build))
						want = "get_builder_assignment"
					case "no build":
						require.NoError(t, os.Remove(filepath.Join(cp.cards.dir, openBuildRecordFile)))
						want = "no build"
					case "dropped":
						_, err := cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
							ChangeID: card.ChangeID, StartedAt: build.StartedAt.Format(time.RFC3339Nano),
						})
						require.NoError(t, err)
						want = "The user dropped change " + card.ChangeID
					case "foreign work":
						productionBuildGit(t, workspace, "checkout", "-q", "-b", "surla/build-2", build.Base)
						writeWorkspaceFiles(t, workspace, map[string]string{"foreign.go": "package shop\n"})
						other := commitAllForLayoutTest(t, workspace)
						productionBuildGit(t, builder, "merge", "--no-edit", other)
						candidate = productionBuildGit(t, builder, "rev-parse", "HEAD")
						want = "merges work the user has not taken"
					case "copy busy":
						cp.builderCopyTurn <- struct{}{}
						defer func() { <-cp.builderCopyTurn }()
						want = "working copy"
					}
					result, err := cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), &gensurla.VerifyChangePayload{
						ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate,
					})
					if want == "" {
						require.NoError(t, err)
						assert.Equal(t, candidate, result.CandidateCommit)
						return
					}
					require.Error(t, err)
					assert.Contains(t, strings.ToLower(err.Error()), strings.ToLower(want))
					if scenario == "copy off branch" {
						var refusal *gensurla.CopyOffBranch
						require.ErrorAs(t, err, &refusal)
						assert.Contains(t, refusal.Message, "Your working copy is on branch 'agent-work'")
					}
					if scenario == "index lock" {
						var refusal *gensurla.CopyOffBranch
						require.ErrorAs(t, err, &refusal)
						assert.Equal(t, "none", refusal.Operation)
						require.NotNil(t, refusal.Branch)
						assert.Equal(t, "surla/build-1", *refusal.Branch)
						assert.Contains(t, refusal.Message, "Git's index lock (")
						assert.Contains(t, refusal.Message, lock)
						assert.Equal(t, beforeLock, readBuildFile(t, cp, card.ChangeID), "a locked copy changes no build state")
					}
					if scenario == "dropped" {
						assertRefusalSays(t, err, "no_build", "stop work on it; make no more edits or commits for it")
					}
					if scenario == "foreign work" {
						assert.ErrorContains(t, err, "Make surla/build-1 and your working copy hold neither")
						assert.ErrorContains(t, err, "Make your own work again as a new commit on surla/build-1")
					}
					assert.Nil(t, result)
					entries, readErr := os.ReadDir(cp.runsDir)
					if !os.IsNotExist(readErr) {
						require.NoError(t, readErr)
						assert.Empty(t, entries, "refused input must not start a run")
					}
					if scenario == "index lock" {
						require.NoError(t, os.Remove(lock))
						started, err := cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), &gensurla.VerifyChangePayload{
							ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate,
						})
						require.NoError(t, err)
						assert.Equal(t, candidate, started.CandidateCommit)
					}
				})
			}
		})
	}
}

// TestBuilderCopyV1HoldsTurnThroughPreflight pauses real read-only Git at the
// readiness and candidate-history boundaries. A concurrent assignment read
// must refuse immediately, and verification must release the turn on success.
func TestBuilderCopyV1HoldsTurnThroughPreflight(t *testing.T) {
	for _, phase := range []struct{ name, argument string }{
		{name: "readiness", argument: "--show-prefix"},
		{name: "candidate history", argument: "for-each-ref"},
	} {
		t.Run(phase.name, func(t *testing.T) {
			cp, card, _, candidate, _ := builderCopyV1Fixture(t, true)
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			// Reuse the FIFO gate as a read-only Git synchronization point;
			// this test starts no check helper or candidate program.
			gate := newPublishGate(t)
			bin := t.TempDir()
			script := "#!/bin/sh\nfor arg do\n" +
				"if [ \"$arg\" = \"$SURLA_V1_PAUSE_ARGUMENT\" ] && mkdir \"$SURLA_V1_GIT_GATE/claimed\" 2>/dev/null; then\n" +
				"exec 3<> \"$SURLA_V1_GIT_GATE/release\"\n: > \"$SURLA_V1_GIT_GATE/in-write\"\n" +
				"dd bs=1 count=1 <&3 >/dev/null 2>&1\nexec 3>&-\nfi\ndone\n" +
				"exec \"$SURLA_V1_REAL_GIT\" \"$@\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
			t.Setenv("SURLA_V1_PAUSE_ARGUMENT", phase.argument)
			t.Setenv("SURLA_V1_GIT_GATE", gate.dir)
			t.Setenv("SURLA_V1_REAL_GIT", realGit)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			ctx, cancel := context.WithTimeout(asCaller("alice@example.com", CallerBuilder), 10*time.Second)
			defer cancel()
			defer func() { assert.NoError(t, gate.release()) }()
			finished := make(chan error, 1)
			go func() {
				_, err := cp.VerifyChange(ctx, &gensurla.VerifyChangePayload{
					ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate,
				})
				finished <- err
			}()
			select {
			case <-gate.held:
			case <-ctx.Done():
				t.Fatal("verification did not reach the Git preflight boundary")
			}
			assert.Len(t, cp.builderCopyTurn, 1)
			_, err = cp.GetBuilderAssignment(ctx, &gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
			assertRefusalSays(t, err, "working_copy_busy", "call get_builder_assignment again")
			require.NoError(t, gate.release())
			select {
			case err := <-finished:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("verification did not finish after releasing Git")
			}
			assert.Empty(t, cp.builderCopyTurn)
		})
	}
}

// TestBuilderCopyV1ApprovalRechecksReportCommit checks the immutable report C,
// even when today's builder tip changes, and rejects replacement builds that
// retain the same revision and suite during unlocked report reads.
func TestBuilderCopyV1ApprovalRechecksReportCommit(t *testing.T) {
	for _, stored := range []bool{false, true} {
		for _, scenario := range []string{"valid older report", "reset branch", "deleted branch", "replacement build", "dropped build", "no build"} {
			profile := "written/"
			if stored {
				profile = "stored/"
			}
			t.Run(profile+scenario, func(t *testing.T) {
				cp, card, build, candidate, builder := builderCopyV1Fixture(t, stored)
				suite := build.TestWriter.Commit
				if stored {
					suite = verifierRuns(cp.verifier).Plan.SuiteCommit
				}
				target := &promotionTargetStub{
					artifacts: map[string]string{"shop": "digest"},
					loading:   make(chan struct{}), release: make(chan struct{}),
				}
				target.report = passingReport(t, card.RequestRevision, candidate, reportSpec{
					SuiteID: "shop", SuiteCommit: suite, Environment: "test", Artifacts: target.artifacts,
				})
				cp.promotionTarget = target
				if scenario == "no build" {
					require.NoError(t, os.Remove(filepath.Join(cp.cards.dir, openBuildRecordFile)))
					target.loading = nil
				}
				finished := make(chan error, 1)
				go func() {
					_, err := cp.ApproveMerge(asCaller("alice@example.com", CallerUser), &gensurla.ApproveMergePayload{
						ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
						CandidateCommit: candidate, ReportDigest: target.report.Digest,
					})
					finished <- err
				}()
				if scenario != "no build" {
					select {
					case <-target.loading:
					case <-time.After(5 * time.Second):
						t.Fatal("approval never reached the report owner")
					}
					workspace := cp.currentWorkspaceDir()
					switch scenario {
					case "valid older report":
						writeWorkspaceFiles(t, builder, map[string]string{"later.go": "package shop\n"})
						commitAllForLayoutTest(t, builder)
					case "reset branch":
						productionBuildGit(t, builder, "reset", "--hard", build.Base)
					case "deleted branch":
						productionBuildGit(t, builder, "checkout", "--detach", "-q")
						productionBuildGit(t, workspace, "branch", "-D", "surla/build-1")
					case "replacement build":
						build.StartedAt = build.StartedAt.Add(time.Second)
						require.NoError(t, cp.cards.saveOpenBuild(build))
					case "dropped build":
						_, err := cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
							ChangeID: card.ChangeID, StartedAt: build.StartedAt.Format(time.RFC3339Nano),
						})
						require.NoError(t, err)
					}
					close(target.release)
				}
				select {
				case err := <-finished:
					if scenario == "valid older report" {
						assert.NoError(t, err)
					} else {
						assert.Error(t, err)
						if scenario == "dropped build" {
							assertRefusalSays(t, err, "no_build", "stop work on it; make no more edits or commits for it")
						}
						if scenario == "reset branch" || scenario == "deleted branch" {
							assert.ErrorContains(t, err, "Ask the builder")
							assert.ErrorContains(t, err, "call verify_change again; then approve that run")
						}
						record := loadMetadataRecord(t, cp, card.ChangeID)
						assert.Nil(t, record.card.Approval)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("approval did not finish after releasing the report")
				}
			})
		}
	}
}

// TestBuilderCopyV1ForeignWorkText preserves L1's bounded file evidence and
// the refusal of a merge that changes no files, quoting untrusted names.
func TestBuilderCopyV1ForeignWorkText(t *testing.T) {
	for _, files := range [][]string{nil, {"a'b.go", "line\nbreak.go", "c.go", "d.go", "e.go"}} {
		check := testwriter.CommitCheck{OnBranch: true, FromBase: true,
			Foreign: []testwriter.ForeignWork{{MergeBase: "foreign", Branch: "surla/build-2", Files: files, FileCount: len(files) * 2}},
		}
		for _, reader := range []struct {
			refuse func(string, string, string, testwriter.CommitCheck) error
			goal   string
			remake string
			next   string
		}{
			{
				refuse: candidateBuildRefusal,
				goal:   "Make surla/build-1 and your working copy hold neither the other builds' commits nor their changes to the named files.",
				remake: "Make your own work again as a new commit on surla/build-1 from base base",
				next:   "then call verify_change again",
			},
			{
				refuse: approvalBuildRefusal,
				goal:   "Ask the builder to make surla/build-1 and its working copy hold neither the other builds' commits nor their changes to the named files",
				remake: "make its own work again as a new commit on surla/build-1 from base base",
				next:   "call verify_change again; then approve that run",
			},
		} {
			err := reader.refuse("candidate", "base", "surla/build-1", check)
			require.Error(t, err)
			var refusal *goa.ServiceError
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, "build_branch_mismatch", refusal.Name)
			assert.ErrorContains(t, err, "commit foreign on surla/build-2 merges work the user has not taken")
			assert.ErrorContains(t, err, reader.goal)
			assert.ErrorContains(t, err, reader.remake)
			assert.ErrorContains(t, err, reader.next)
			if len(files) == 0 {
				assert.NotContains(t, err.Error(), "0 files")
			} else {
				assert.ErrorContains(t, err, "10 files")
				assert.ErrorContains(t, err, testwriter.ShownName(files[0]))
				assert.ErrorContains(t, err, "a name that is not shown")
				assert.NotContains(t, err.Error(), "line\nbreak.go")
			}
		}
	}
}

// builderCopyV1Fixture supplies a real current build, readable builder copy and
// immutable committed candidate. Its supervisor exits without building code.
func builderCopyV1Fixture(t *testing.T, stored bool) (
	*ControlPlane, *gensurla.ChangeCardResult, buildFile, string, string,
) {
	t.Helper()
	cp, card, cfg := verificationSetup(t)
	cp.team = &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}}
	workspace := cp.currentWorkspaceDir()
	writeStarterAt(t, workspace, "example.com/shop")
	commitAllForLayoutTest(t, workspace)
	card = attachBuildSnapshot(t, cp, card.ChangeID)
	build := fixtureOpenBuild(t, cp, card, buildRolesBuilderOnly)
	build.Builder.Read = true
	if !stored {
		cp.verifier = writtenTestVerification{runs: cfg}
		build.Roles = buildRolesBuilderAndTestWriter
		build.TestWriter = testWriterAssignment{State: testsAccepted, Read: true, Commit: build.Base, CheckedAt: time.Now().UTC()}
	}
	require.NoError(t, cp.cards.saveOpenBuild(build))
	builder := filepath.Join(workspace, testwriter.BuilderRole.Folder)
	productionBuildGit(t, workspace, "worktree", "add", "-q", "-b", "surla/build-1", builder, build.Base)
	productionBuildGit(t, workspace, "branch", "surla/tests-1", build.Base)
	writeBuilderDesignFixture(t, builder, build.DesignSource)
	writeWorkspaceFiles(t, builder, map[string]string{"implementation.go": "package shop\n"})
	candidate := commitAllForLayoutTest(t, builder)
	return cp, card, build, candidate, builder
}
