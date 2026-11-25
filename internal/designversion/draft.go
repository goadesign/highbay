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
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/goadesign/highbay/internal/designeval"
)

// maxModuleFileBytes bounds the read of the draft's go.mod and of its go.sum,
// files the guide writes, as Highbay bounds its other reads of a go.mod or
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
// go.mod; a path or file that is not valid UTF-8. And, naming the file and
// line of each: a design file Go cannot parse, and an import of a package of
// the draft's own module that the snapshot does not hold, either outside
// design/, such as <module>/internal/x, or in a folder the walk skips, such
// as <module>/design/testdata/x; <module>/design/sub is part of the design
// once the walk reads a file of it. The module is the one go.mod declares,
// read as the evaluator reads it. A go.mod that does not parse, or declares
// no module, leaves imports unchecked: the evaluator refuses that go.mod, so
// no evaluation of the snapshot succeeds.
//
// The design texts are in gofmt form, so an evaluation's diagnostics cite
// lines of that form, which differ from the lines of a draft file that is
// not in gofmt form.
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
	// The evaluator reads the module path with modfile.Parse, which reads a
	// block-form module statement that modfile.ModulePath does not. A go.mod
	// it cannot use is its to refuse, with its own message.
	var module string
	if parsed, err := modfile.Parse("go.mod", goMod, nil); err == nil && parsed.Module != nil {
		module = parsed.Module.Mod.Path
	}
	held := map[string]bool{}
	for rel := range design {
		held[path.Dir(rel)] = true
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
		if module != "" {
			problems = append(problems, importsNotHeld(fset, file, module, held)...)
		}
	}
	if err := errors.Join(problems...); err != nil {
		return nil, err
	}
	return files, nil
}

// importsNotHeld returns one error for each import in file, parsed into fset,
// of a package of module whose folder, relative to the module, is not in held,
// the folders that hold a file of the snapshot, naming the file, line and
// column of the import. The module's own root package is never held: the
// snapshot holds only folders under design/.
func importsNotHeld(fset *token.FileSet, file *ast.File, module string, held map[string]bool) []error {
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
		var folder string
		switch {
		case imported == module:
			folder = "."
		case strings.HasPrefix(imported, module+"/"):
			folder = strings.TrimPrefix(imported, module+"/")
		default:
			// A package of another module or of the standard library.
			continue
		}
		if !held[folder] {
			problems = append(problems, fmt.Errorf(
				"%s: imports %s, which the draft does not hold: of the project's packages, a design imports "+
					"only those under design/, outside hidden, vendor and testdata folders and nested modules",
				at, imported))
		}
	}
	return problems
}
