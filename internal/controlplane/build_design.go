// This file writes a build's agreed design into the builder's module while
// preserving files outside the design snapshot's ownership.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/goadesign/surla/internal/designeval"
	"golang.org/x/sys/unix"
)

// writeBuildDesign synchronizes only the non-test Go files included by the
// design walk. The caller holds the builder turn and the opened module for
// the entire generation. Files are replaced atomically, without following
// links or modifying hard-linked aliases. A failure can leave an already
// written part of the agreed design; calling again finishes the same work.
// Test files, testdata, nested modules and other excluded files are retained.
func writeBuildDesign(ctx context.Context, root *os.Root, design map[string]string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	for name := range design {
		if !validProvenancePath(name) {
			return fmt.Errorf("saved design file %q is outside the design walk", name)
		}
	}
	current, err := designeval.ReadDesignFilesFromRoot(root.Name(), root)
	if err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	base := int(dir.Fd())
	for _, name := range slices.Sorted(maps.Keys(design)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if text, found := current[name]; found && text == design[name] {
			continue
		}
		parent, err := openBuildDesignParent(base, path.Dir(name))
		if err != nil {
			return fmt.Errorf("open agreed design directory for %s: %w", name, err)
		}
		_, exists, readErr := readAtNoFollow(parent, path.Base(name))
		if readErr != nil {
			return errors.Join(readErr, unix.Close(parent))
		}
		_, writeErr := writeAtomic(parent, path.Base(name), []byte(design[name]), !exists)
		if err := errors.Join(writeErr, unix.Close(parent)); err != nil {
			return fmt.Errorf("write agreed design file %s: %w", name, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(current)) {
		if _, kept := design[name]; kept {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		parent, err := openDirAt(base, path.Dir(name))
		if err != nil {
			return fmt.Errorf("open obsolete design directory for %s: %w", name, err)
		}
		_, exists, readErr := readAtNoFollow(parent, path.Base(name))
		if readErr != nil {
			return errors.Join(readErr, unix.Close(parent))
		}
		if exists {
			if err := unix.Unlinkat(parent, path.Base(name), 0); err != nil {
				return errors.Join(fmt.Errorf("remove obsolete design file %s: %w", name, err), unix.Close(parent))
			}
		}
		if err := unix.Close(parent); err != nil {
			return err
		}
	}
	return nil
}

// openBuildDesignParent creates missing design directories through held,
// no-follow descriptors. A nested module owns its contents independently of
// the agreed design, so an existing go.mod below design refuses the write.
// The caller owns the returned descriptor; base is left open.
func openBuildDesignParent(base int, rel string) (int, error) {
	fd, err := openDirAt(base, ".")
	if err != nil {
		return -1, err
	}
	for i, part := range strings.Split(rel, "/") {
		next, err := unix.Openat(fd, part, openDirFlags, 0)
		if errors.Is(err, unix.ENOENT) {
			if err := unix.Mkdirat(fd, part, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
				return -1, errors.Join(err, unix.Close(fd))
			}
			next, err = unix.Openat(fd, part, openDirFlags, 0)
		}
		if err := errors.Join(err, unix.Close(fd)); err != nil {
			return -1, errors.Join(err, closeFD(next))
		}
		fd = next
		if i > 0 {
			var info unix.Stat_t
			err := unix.Fstatat(fd, "go.mod", &info, unix.AT_SYMLINK_NOFOLLOW)
			if err == nil {
				return -1, errors.Join(fmt.Errorf("%s is a nested module; its files are outside the agreed design", rel), unix.Close(fd))
			}
			if !errors.Is(err, unix.ENOENT) {
				return -1, errors.Join(err, unix.Close(fd))
			}
		}
	}
	return fd, nil
}
