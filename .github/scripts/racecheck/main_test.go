// These tests keep CI concurrency ownership tied to Go syntax.
package main

import (
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentUsesSyntax preserves all declared concurrency forms while
// avoiding false owners caused by documentation and ordinary variable names.
func TestConcurrentUsesSyntax(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         bool
	}{
		{"goroutine", "package p; func f(){ go f() }", true},
		{"channel type", "package p; var c chan int", true},
		{"channel receive", "package p; func f(){ _ = <-other() }", true},
		{"select", "package p; func f(){ select{} }", true},
		{"sync alias", "package p; import lock \"sync\"; var m lock.Mutex", true},
		{"atomic", "package p; import \"sync/atomic\"; var n atomic.Int64", true},
		{"errgroup", "package p; import \"golang.org/x/sync/errgroup\"; var g errgroup.Group", true},
		{"words", "package p; // go f() and chan int\nvar sync = \"chan go sync\"", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "source.go", tc.source, parser.ParseComments)
			require.NoError(t, err)
			assert.Equal(t, tc.want, concurrent(file))
		})
	}
}
