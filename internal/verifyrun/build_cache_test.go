// This file checks cache handoff admission before any persistent record or child.
package verifyrun

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
)

// TestExecutionLaunchRequiresBuildCache rejects absent cache input at every
// process boundary, even when there is no usable saved request yet.
func TestExecutionLaunchRequiresBuildCache(t *testing.T) {
	_, err := Start(t.TempDir(), Request{}, nil, nil, "")
	assert.ErrorContains(t, err, "build cache directory is required")
	_, err = LaunchLive(t.TempDir(), verify.Plan{}, "key", nil, nil, nil, "")
	assert.ErrorContains(t, err, "build cache directory is required")
	assert.ErrorContains(t, Supervise(t.TempDir(), nil, ""), "build cache directory is required")
}

// TestExecutionLaunchRejectsRelativeCacheBeforeAcceptance uses otherwise valid
// requests and a missing command: the cache error must precede filesystem or
// child setup, leaving no accepted record or allocated operation directory.
func TestExecutionLaunchRejectsRelativeCacheBeforeAcceptance(t *testing.T) {
	for _, cache := range []string{"cache", "./cache", filepath.Join("..", "cache")} {
		t.Run(cache, func(t *testing.T) {
			command := []string{filepath.Join(t.TempDir(), "missing-executable")}
			t.Run("verification", func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "runs")
				runID := NewRunID()
				pid, err := Start(root, Request{
					RunID: runID, Owner: "operator", AcceptedAt: time.Now().UTC(),
					Plan: verify.Plan{WorkDir: filepath.Join(root, runID, "work"), Tests: verify.TestsEvaluator},
				}, command, nil, cache)
				assert.ErrorContains(t, err, "build cache directory must be absolute")
				assert.Zero(t, pid)
				assert.NoDirExists(t, root, "invalid cache input must not allocate a run or write its request")
			})
			t.Run("live", func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "live")
				process, err := LaunchLive(root, evaluatorPlan, "live-key", command, nil, nil, cache)
				assert.ErrorContains(t, err, "build cache directory must be absolute")
				assert.Nil(t, process)
				assert.NoDirExists(t, root, "invalid cache input must not allocate a target or write its request")
			})
		})
	}
}
