// This file validates the retained dependency input at server and helper ingress.
// The cache lives outside operation storage so recovery cannot remove it.
package controlplane

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/goadesign/highbay/internal/abspath"
)

// validateCheckModuleCacheDir admits an existing physical, private directory
// owned by the effective user. excluded contains source and independently
// cleaned storage roots; neither may contain the other. The server validates
// configuration once, and the helper validates its transient process input
// before allocating scratch or contacting Docker.
func validateCheckModuleCacheDir(dir string, excluded ...string) error {
	physical, err := abspath.Eval(dir)
	if err != nil {
		return fmt.Errorf("resolve check module cache directory %s: %w", dir, err)
	}
	if !filepath.IsAbs(dir) || physical != dir {
		return fmt.Errorf("check module cache directory %s must be an absolute physical path", dir)
	}
	fd, err := unix.Open(dir, openDirFlags, 0)
	if err != nil {
		return fmt.Errorf("open check module cache directory %s: %w", dir, err)
	}
	var st unix.Stat_t
	err = errors.Join(unix.Fstat(fd, &st), unix.Close(fd))
	if err != nil {
		return fmt.Errorf("stat check module cache directory %s: %w", dir, err)
	}
	if st.Uid != uint32(os.Geteuid()) || st.Mode&0o777 != 0o700 {
		return fmt.Errorf("check module cache directory %s must be owned by effective uid %d with mode 0700", dir, os.Geteuid())
	}
	if err := unix.Faccessat(unix.AT_FDCWD, dir, unix.R_OK|unix.W_OK|unix.X_OK, unix.AT_EACCESS); err != nil {
		return fmt.Errorf("check module cache directory %s is not accessible: %w", dir, err)
	}
	for _, root := range excluded {
		if root == "" {
			continue
		}
		physicalRoot, err := abspath.EvalMissing(root)
		if err != nil {
			return fmt.Errorf("resolve check module cache exclusion %s: %w", root, err)
		}
		inside, err := filepath.Rel(physicalRoot, dir)
		if err != nil {
			return err
		}
		contains, err := filepath.Rel(dir, physicalRoot)
		if err != nil {
			return err
		}
		if filepath.IsLocal(inside) || filepath.IsLocal(contains) {
			return fmt.Errorf("check module cache directory %s must be separate from source and operation storage %s", dir, physicalRoot)
		}
	}
	return nil
}
