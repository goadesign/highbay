// Package designeval tests Goa/Goa-AI design evaluation and JSON Schema
// extraction.
package designeval

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/testmodule"
)

// ordersDSL declares the orders API and its get_order method, the design
// both tests here evaluate after their own init.
const ordersDSL = `
var _ = API("orders", func() {
	HTTP(func() {
		Path("/api/v1")
	})
})

var _ = Service("orders", func() {
	HTTP(func() {
		Path("/orders")
	})
	Method("get_order", func() {
		Payload(func() {
			Attribute("id", String, func() {
				MinLength(3)
			})
			Required("id")
		})
		Result(String)
		HTTP(func() {
			GET("/{id}")
			Response(StatusOK)
		})
	})
})
`

// TestEvaluateRunsTheDesignWithItsOwnEnvironmentAndBuildsJSONSchemas evaluates
// a design whose init panics when it sees a variable set in this test's own
// environment: the evaluation succeeds, because the design program gets only
// the environment Surla gives it, and the method's payload schema is built.
func TestEvaluateRunsTheDesignWithItsOwnEnvironmentAndBuildsJSONSchemas(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	t.Setenv("SURLA_DESIGNEVAL_TEST_SECRET", "must not reach the design")
	design := `package custom_pkg

import (
	"os"
	. "goa.design/goa/v3/dsl"
)

func init() {
	if os.Getenv("SURLA_DESIGNEVAL_TEST_SECRET") != "" {
		panic("the design program inherited the evaluator's environment")
	}
}
` + ordersDSL
	res, err := testEvaluator.Evaluate(t.Context(), testmodule.Snapshot(t, "", design))
	require.NoError(t, err)
	assert.Equal(t, "orders", res.SystemName)
	assert.NotEmpty(t, res.Declarations["Generated:gen/http/orders/server/paths.go"],
		"the actual generated HTTP path source is compared")
	assert.Contains(t, res.Declarations["Service:orders/Method:get_order"], `"minLength":3`)
}

// TestEvaluateStopsTheProcessesADesignStarts evaluates a design whose init
// starts a long-running child and records its process ID: once Evaluate
// returns, the child no longer exists, because the design program runs in its
// own process group, which the evaluation stops.
func TestEvaluateStopsTheProcessesADesignStarts(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	design := `package design

import (
	"os"
	"os/exec"
	"strconv"

	. "goa.design/goa/v3/dsl"
)

func init() {
	child := exec.Command("/bin/sleep", "300")
	if err := child.Start(); err != nil {
		panic(err)
	}
	if err := os.WriteFile(` + strconv.Quote(pidFile) + `, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		panic(err)
	}
}
` + ordersDSL
	_, err := testEvaluator.Evaluate(t.Context(), testmodule.Snapshot(t, "", design))
	require.NoError(t, err)
	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err, "the design's init ran")
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	assert.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "the design's child was stopped with its group")
	if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		assert.NoError(t, syscall.Kill(pid, syscall.SIGKILL))
	}
}
