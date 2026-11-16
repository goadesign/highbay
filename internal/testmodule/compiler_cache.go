// This file selects compiler storage at trusted test composition roots. Test
// scratch and deliberately cold/lifecycle controls keep their own directories.
package testmodule

import (
	"os"
	"path/filepath"
	"testing"
)

// CompilerCache returns CI's explicitly shared absolute compiler cache, or a
// test-owned cache outside CI. It never selects module storage or source files.
// Callers testing cache isolation or cleanup must choose their own t.TempDir
// instead. Runtime libraries do not read this test-only environment contract.
func CompilerCache(t testing.TB) string {
	t.Helper()
	cache := os.Getenv("SURLA_TEST_BUILD_CACHE")
	if cache == "" {
		return t.TempDir()
	}
	if !filepath.IsAbs(cache) {
		t.Fatalf("SURLA_TEST_BUILD_CACHE must be absolute: %q", cache)
	}
	return cache
}
