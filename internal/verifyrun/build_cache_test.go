// This file checks cache handoff admission before any persistent record or child.
package verifyrun

import (
	"testing"

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
