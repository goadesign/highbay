// This file derives the builder's current working folder and Go workspace
// instruction without changing the assignment or its files.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goadesign/surla/internal/testwriter"
)

// builderCopyFolder resolves the project repository root, the builder's
// working folder inside .surla-build (<workspace>/.surla-build/<prefix>), and
// the copy root (<workspace>/.surla-build).
func builderCopyFolder(
	ctx context.Context, workspace string,
) (repo, folder, copyRoot string, err error) {
	if workspace == "" {
		return "", "", "", errors.New("no builder working folder is bound")
	}
	repo, prefix, err := resolveStarterRepo(ctx, workspace)
	if err != nil {
		return "", "", "", err
	}
	copyRoot = filepath.Join(workspace, testwriter.BuilderRole.Folder)
	folder = filepath.Join(copyRoot, filepath.FromSlash(prefix))
	return repo, folder, copyRoot, nil
}

// builderWorkingFolder reports the builder's folder inside .surla-build and
// whether GOWORK=off is required. A go.work within the builder copy root
// belongs to this folder; a workspace above that copy root must not control
// its Go commands. Callers hold workspaceMu and establish ownership before
// reading these filesystem facts.
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
