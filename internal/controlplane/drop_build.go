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
	if _, err := c.ownedTeam(ctx); err != nil {
		return nil, err
	}
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
		return nil, gensurla.MakeNotFound(errors.New("There is no open build to drop."))
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
		return nil, gensurla.MakeNotFound(errors.New("There is no open build to drop."))
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
	// Record shares c.mu for publication. Capture its actual numbered tips
	// here, once, so no accepted commit can fall between this snapshot and
	// the durable Drop that retains it after a ref is moved or deleted.
	tips, err := buildBranchTips(work, repo, current.Number)
	if err != nil {
		c.mu.Unlock()
		c.draftMu.Unlock()
		return nil, err
	}
	branches, err := keptBuildBranches(work, repo, current.Base, tips)
	if err != nil {
		c.mu.Unlock()
		c.draftMu.Unlock()
		return nil, err
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

	return &gensurla.ChangeBuild{ChangeID: current.ChangeID, Build: gensurla.NewBuildStarted(startedBuild(heldBuild{file: current, inForce: buildInForce(current, record), storedEvaluator: c.storedEvaluator()}, gensurla.NewBuildTakingDropped(&gensurla.BuildDropped{KeptBranches: branches})))}, nil
}

// buildBranchTips captures only the numbered build's existing refs, including
// tips at or behind the base, for later cross-build checks.
func buildBranchTips(ctx context.Context, repo string, number int) (map[string]string, error) {
	tips := make(map[string]string)
	for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
		branch := role.Branch(number)
		tip, err := runGitRead(ctx, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
		if err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				continue
			}
			return nil, err
		}
		tips[branch] = strings.TrimSpace(string(tip))
	}
	return tips, nil
}

// keptBuildBranches reports current refs with work outside the saved base.
func keptBuildBranches(ctx context.Context, repo, base string, tips map[string]string) ([]string, error) {
	branches := make([]string, 0, len(tips))
	for branch, tip := range tips {
		withinBase, err := buildCommitInHistory(ctx, repo, tip, base)
		if err != nil {
			return nil, err
		}
		if !withinBase {
			branches = append(branches, branch)
		}
	}
	slices.Sort(branches)
	return branches, nil
}
