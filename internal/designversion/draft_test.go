// This file tests reading the guide's draft as a refresh evaluates it.
package designversion

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/designeval"
)

// draftGoMod is the go.mod of every draft these tests read.
const draftGoMod = "module example.com/shop\n\ngo 1.27.3\n"

// TestReadDraftReadsGoModGoSumAndTheDesignWalkInGofmtForm reads a draft whose
// folders the design walk skips, and whose test file it leaves out, each
// holding text Go cannot parse, and checks that the snapshot holds exactly
// go.mod, go.sum and the other design files, in gofmt form.
func TestReadDraftReadsGoModGoSumAndTheDesignWalkInGofmtForm(t *testing.T) {
	const goSum = "goa.design/goa/v3 v3.35.0 h1:checksum=\n"
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod":                draftGoMod,
		"go.sum":                goSum,
		"main.go":               "package main\n",
		"design/design.go":      "package design\nvar   A=1\n",
		"design/sub/types.go":   "package sub\n\nvar B = 2\n",
		"design/design_test.go": "not Go",
		"design/.hidden/h.go":   "not Go",
		"design/vendor/v.go":    "not Go",
		"design/testdata/t.go":  "not Go",
		"design/nested/go.mod":  "module example.com/nested\n",
		"design/nested/n.go":    "not Go",
	})

	got, err := readDraft(t, dir)

	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"go.mod":              draftGoMod,
		"go.sum":              goSum,
		"design/design.go":    "package design\n\nvar A = 1\n",
		"design/sub/types.go": "package sub\n\nvar B = 2\n",
	}, got)
}

// TestReadDraftRefusesALinkAnUnparsableFileAndAnImportOutsideDesign reads
// drafts holding a link, a design file Go cannot parse, or an import of the
// project's own packages, and checks that each refusal names the file, and
// the line where Go reads one, while an import of a design subpackage is
// read.
func TestReadDraftRefusesALinkAnUnparsableFileAndAnImportOutsideDesign(t *testing.T) {
	cases := []struct {
		name    string
		files   map[string]string
		links   map[string]string
		wantErr string
		wantIs  error
	}{
		{
			name:    "link at a design file",
			files:   map[string]string{"design/a.go": "package design\n"},
			links:   map[string]string{"design/link.go": "a.go"},
			wantErr: filepath.Join("design", "link.go") + " is a symbolic link",
		},
		{
			name:    "linked design folder",
			files:   map[string]string{"real/a.go": "package design\n"},
			links:   map[string]string{"design": "real"},
			wantErr: "design is a symbolic link",
		},
		{
			name:   "linked go.mod",
			files:  map[string]string{"real.mod": draftGoMod, "design/a.go": "package design\n"},
			links:  map[string]string{"go.mod": "real.mod"},
			wantIs: designeval.ErrNotRegularFile,
		},
		{
			name: "design file Go cannot parse",
			files: map[string]string{
				"design/types.go": "package design\n\n<<<<<<< draft\nvar A = 10\n=======\nvar A = 11\n>>>>>>> project folder\n",
			},
			wantErr: filepath.Join("design", "types.go") + ":3:1: expected declaration, found '<<'",
		},
		{
			name:    "import of the project outside design",
			files:   map[string]string{"design/api.go": "package design\n\nimport \"example.com/shop/internal/x\"\n"},
			wantErr: filepath.Join("design", "api.go") + ":3:8: imports example.com/shop/internal/x, a package of the project outside design/",
		},
		{
			name:  "import of a design subpackage",
			files: map[string]string{"design/api.go": "package design\n\nimport \"example.com/shop/design/sub\"\n"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"go.mod": draftGoMod}
			for rel, text := range tc.files {
				files[rel] = text
			}
			if _, linked := tc.links["go.mod"]; linked {
				delete(files, "go.mod")
			}
			writeFiles(t, dir, files)
			for rel, target := range tc.links {
				require.NoError(t, os.Symlink(target, filepath.Join(dir, filepath.FromSlash(rel))))
			}

			got, err := readDraft(t, dir)

			switch {
			case tc.wantIs != nil:
				assert.ErrorIs(t, err, tc.wantIs)
				assert.ErrorContains(t, err, "go.mod")
			case tc.wantErr != "":
				assert.ErrorContains(t, err, filepath.Join(dir, tc.wantErr))
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.files["design/api.go"], got["design/api.go"])
			}
		})
	}
}

// readDraft opens dir as the draft's module folder, as a caller does, and
// reads it with ReadDraft.
func readDraft(t *testing.T, dir string) (map[string]string, error) {
	t.Helper()
	cleanDir, root, err := designeval.OpenModuleRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	return ReadDraft(cleanDir, root)
}

// writeFiles writes each text of files at its slash-separated path in dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o700))
		require.NoError(t, os.WriteFile(full, []byte(text), 0o600))
	}
}
