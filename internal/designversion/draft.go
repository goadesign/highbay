// This file reads the guide's draft, the module folder where the guide writes
// the next revision's design, as a refresh evaluates it.
package designversion

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/goadesign/surla/internal/designeval"
)

// maxModuleFileBytes bounds the read of the draft's go.mod and of its go.sum,
// files the guide writes, as Surla bounds its other reads of a go.mod or
// go.sum an agent can write: the tests check's dependency lists and the
// project's test layout.
const maxModuleFileBytes = 1 << 20

// ReadDraft reads the guide's draft, the module folder opened as root, as a
// refresh evaluates it, and returns it as a design snapshot keyed by
// slash-separated path relative to the folder: go.mod, go.sum when the folder
// has one, and the design files that designeval.ReadDesignFilesFromRoot's
// walk of design/ finds (hidden, vendor, testdata and nested-module folders
// skipped, test files left out), each in gofmt form (Canonical). Nothing else
// in the folder is read. cleanDir is the folder's path; it only names files in
// errors.
//
// It refuses, naming the file: a link at go.mod, go.sum or design/, or
// anywhere the walk goes; a go.mod or go.sum that is not a regular file or
// holds more than 1 MiB; a design/ that is not a folder; a draft with no
// go.mod, or whose go.mod declares no module path; a path or file that is not
// valid UTF-8. And, naming the file and line of each: a design file Go cannot
// parse, and an import of a package of the draft's own module outside
// design/, such as <module>/internal/x, which the draft does not hold, while
// <module>/design/sub is part of the design. The module is the one the
// draft's go.mod declares, so the one an evaluation of the snapshot loads.
func ReadDraft(cleanDir string, root *os.Root) (map[string]string, error) {
	goMod, err := designeval.ReadRegularFile(root, "go.mod", maxModuleFileBytes)
	if err != nil {
		return nil, fmt.Errorf("draft %s: %w", cleanDir, err)
	}
	files := map[string]string{"go.mod": string(goMod)}
	goSum, err := designeval.ReadRegularFile(root, "go.sum", maxModuleFileBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A module without go.sum is the evaluator's to refuse, as it
		// refuses every snapshot without one.
	case err != nil:
		return nil, fmt.Errorf("draft %s: %w", cleanDir, err)
	default:
		files["go.sum"] = string(goSum)
	}
	design, err := designeval.ReadDesignFilesFromRoot(cleanDir, root)
	if err != nil {
		return nil, err
	}
	maps.Copy(files, design)
	if err := designeval.ValidateUTF8Snapshot(files); err != nil {
		return nil, fmt.Errorf("draft %s: %w", cleanDir, err)
	}
	module := modfile.ModulePath(goMod)
	if module == "" {
		return nil, fmt.Errorf("draft %s: go.mod declares no module path", cleanDir)
	}
	// Every file is checked before refusing, so one refusal names every file
	// and import the guide has to correct.
	var problems []error
	fset := token.NewFileSet()
	for _, rel := range slices.Sorted(maps.Keys(design)) {
		file, text, err := parseCanonical(fset, filepath.Join(cleanDir, filepath.FromSlash(rel)), design[rel])
		if err != nil {
			problems = append(problems, err)
			continue
		}
		files[rel] = text
		problems = append(problems, importsOutsideDesign(fset, file, module)...)
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return files, nil
}

// importsOutsideDesign returns one error for each import in file, parsed into
// fset, of a package of module that is not module's design/ package or one
// under it, naming the file, line and column of the import.
func importsOutsideDesign(fset *token.FileSet, file *ast.File, module string) []error {
	design := module + "/design"
	var problems []error
	for _, spec := range file.Imports {
		at := fset.Position(spec.Path.Pos())
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			// The parser accepts only import paths that unquote, so this is
			// a parser defect, reported rather than passed over.
			problems = append(problems, fmt.Errorf("%s: import path %s: %w", at, spec.Path.Value, err))
			continue
		}
		inModule := imported == module || strings.HasPrefix(imported, module+"/")
		inDesign := imported == design || strings.HasPrefix(imported, design+"/")
		if inModule && !inDesign {
			problems = append(problems, fmt.Errorf(
				"%s: imports %s, a package of the project outside design/, which the draft does not hold: "+
					"import only packages under %s and other modules' packages", at, imported, design))
		}
	}
	return problems
}
