// Command modprime populates GOMODCACHE with the committed test module profiles
// and the synthetic test module closures and sum.golang.org verification tiles
// before CI saves the module archive.
package main

import (
	"fmt"
	"os"

	"github.com/goadesign/surla/internal/testmodule"
)

// main primes the active toolchain's module cache and exits non-zero on error.
func main() {
	if err := testmodule.PrimeCache(); err != nil {
		fmt.Fprintln(os.Stderr, "modprime:", err)
		os.Exit(1)
	}
}
