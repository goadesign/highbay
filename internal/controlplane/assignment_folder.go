// This file derives the builder's current working folder and Go workspace
// instruction without changing the assignment or its files.
package controlplane

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// builderWorkingFolder reports the folder currently used for builder
// registration. Until the copy implementation lands, it is the bound project
// folder. A go.work within the accepted source root belongs to this folder;
// a workspace above that root must not control its Go commands. Callers hold
// workspaceMu and establish ownership before reading these filesystem facts.
func builderWorkingFolder(folder, root string) (string, bool, error) {
	if folder == "" {
		return "", false, errors.New("no builder working folder is bound")
	}
	for dir := folder; ; dir = filepath.Dir(dir) {
		_, err := os.Stat(filepath.Join(dir, "go.work"))
		if err == nil {
			return folder, false, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("read builder Go workspace: %w", err)
		}
		if dir == root {
			return folder, true, nil
		}
		if filepath.Dir(dir) == dir {
			return "", false, errors.New("builder folder is outside the accepted source root")
		}
	}
}
