// Command modprime populates GOMODCACHE with the committed test module profiles
// and the synthetic test module closures and sum.golang.org verification tiles
// for the ordinary CI partitions before compiler-inputs saves the module
// archive. Real starter-template initialization (highbay init / scaffold.Init
// with scaffold.RunCommand) exercises networked first-run resolution and stays
// owned by the non-PR Full / starter and Full / generation jobs.
package main

import (
	"fmt"
	"os"

	"github.com/goadesign/highbay/internal/testmodule"
)

// main primes the active toolchain's module cache and exits non-zero on error.
func main() {
	if err := testmodule.PrimeCache(); err != nil {
		fmt.Fprintln(os.Stderr, "modprime:", err)
		os.Exit(1)
	}
}
