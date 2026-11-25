// This file derives the builder's current working folder and Go workspace
// instruction without changing the assignment or its files.
package controlplane

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/goadesign/highbay/verify/candidate"
)

// builderCopyFolder resolves the project repository root, the builder's
// working folder inside .highbay-build (<workspace>/.highbay-build/<prefix>), and
// the copy root (<workspace>/.highbay-build).
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

// builderWorkingFolder reports the builder's folder inside .highbay-build and
// whether GOWORK=off is required. A go.work within the builder copy root
// belongs to this folder; a workspace above that copy root must not control
// its Go commands. Callers hold workspaceMu and establish ownership before
// reading these filesystem facts.
func builderWorkingFolder(folder, root string) (string, bool, error) {
	if folder == "" {
		return "", false, errors.New("no builder working folder is bound")
	}
	goWork, err := candidate.GoWork(root, folder)
	if err != nil {
		return "", false, err
	}
	return folder, goWork == "off", nil
}
