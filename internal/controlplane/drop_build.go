// This file owns the user's durable decision to drop an open build.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
)

// buildDropped identifies the one start named by the current provenance generation.
func buildDropped(record madeFromRecord, build buildFile) bool {
	d := record.Start.Dropped
	return d != nil && d.ChangeID == build.ChangeID && d.StartedAt.Equal(build.StartedAt)
}

// DropBuild records a new provenance generation without changing any Git ref or draft.
func (c *ControlPlane) DropBuild(ctx context.Context, p *gensurla.DropBuildPayload) (*gensurla.ChangeBuild, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.mu.RLock()
	_, err := c.ownedCard(principal, p.ChangeID)
	saved, found, loadErr := c.cards.loadOpenBuild()
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if loadErr != nil {
		return nil, loadErr
	}
	if !found {
		return nil, gensurla.MakeConflict(errors.New("There is no open build to drop."))
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	workspace, _, _ := c.currentWorkspaceTarget()
	repo, _, err := resolveStarterRepo(work, workspace)
	if err != nil {
		return nil, err
	}
	head, err := runGitRead(work, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	tips := make(map[string]string)
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		branch := role.Branch(saved.Number)
		tip, e := runGitRead(work, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
		if e != nil {
			var exit *exec.ExitError
			if errors.As(e, &exit) && exit.ExitCode() == 1 {
				continue
			}
			return nil, e
		}
		commit := strings.TrimSpace(string(tip))
		if commit == saved.Base {
			continue
		}
		withinBase, err := buildCommitInHistory(work, repo, commit, saved.Base)
		if err != nil {
			return nil, err
		}
		if !withinBase {
			tips[branch] = commit
		}
	}
	c.draftMu.Lock()
	c.mu.Lock()
	record, err := c.ownedCard(principal, p.ChangeID)
	current, currentFound, loadErr := c.cards.loadOpenBuild()
	provenance, exists, provErr := c.cards.loadMadeFrom()
	if err == nil {
		err = loadErr
	}
	if err == nil {
		err = provErr
	}
	if err != nil {
		c.mu.Unlock()
		c.draftMu.Unlock()
		return nil, err
	}
	if !currentFound {
		c.mu.Unlock()
		c.draftMu.Unlock()
		return nil, gensurla.MakeConflict(errors.New("There is no open build to drop."))
	}
	if current.ChangeID != saved.ChangeID || !current.StartedAt.Equal(saved.StartedAt) || p.ChangeID != current.ChangeID || p.StartedAt != current.StartedAt.UTC().Format(time.RFC3339Nano) {
		currentCard, cardErr := c.ownedCard(principal, current.ChangeID)
		c.mu.Unlock()
		c.draftMu.Unlock()
		if cardErr != nil {
			return nil, cardErr
		}
		progress, readErr := c.startedBuild(work, principal.ID, heldBuild{file: current, inForce: buildInForce(current, currentCard), storedEvaluator: c.storedEvaluator()})
		if readErr != nil {
			return nil, readErr
		}
		return nil, &gensurla.OpenBuildRefusal{Name: "build_changed", Message: "The open build changed. Review the current build before dropping it.", ChangeID: current.ChangeID, Build: progress}
	}
	if exists && buildDropped(provenance, current) {
		c.mu.Unlock()
		c.draftMu.Unlock()
		return nil, gensurla.MakeConflict(errors.New("This build was already dropped."))
	}
	// A legacy or freshly evaluated revision can precede the first saved list.
	// Its generation still has to precede the new drop generation.
	provenance.Write = max(provenance.Write, current.MadeFrom.Start.Write) + 1
	provenance.Start = madeFromStart{Write: provenance.Write, ProjectCommit: strings.TrimSpace(string(head)), Dropped: &droppedBuild{ChangeID: current.ChangeID, StartedAt: current.StartedAt, Number: current.Number, BranchTips: tips}}
	provenance.Entries = nil
	provenance.Taken = nil
	provenance.Pending = nil
	err = c.cards.saveMadeFrom(provenance)
	c.mu.Unlock()
	c.draftMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("save dropped build: %w", err)
	}
	branches := make([]string, 0, len(tips))
	for branch := range tips {
		branches = append(branches, branch)
	}
	slices.Sort(branches)
	return &gensurla.ChangeBuild{ChangeID: current.ChangeID, Build: gensurla.NewBuildStarted(startedBuild(heldBuild{file: current, inForce: buildInForce(current, record), storedEvaluator: c.storedEvaluator()}, gensurla.NewBuildTakingDropped(&gensurla.BuildDropped{KeptBranches: branches})))}, nil
}
