// This file reads what a go.mod states about modules and finds the modules a
// builder's go.mod lacks to build a design as it was evaluated.
package designversion

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// GoMod is what a go.mod file states about modules: the module it declares
// and the modules it requires.
type GoMod struct {
	// Module is the path of the module the file declares, never empty.
	Module string
	// Require maps the path of each module the file requires, directly or
	// indirectly, to its version. A module required twice is at the higher
	// version, the one Go selects.
	Require map[string]string
}

// Requirements reads goMod, the text of a go.mod file, as the go command
// does, and returns the module it declares and the modules it requires. A
// text Go cannot parse as a go.mod is refused with modfile's error, which
// names go.mod and the line, and so is one that declares no module.
func Requirements(goMod string) (GoMod, error) {
	file, err := modfile.Parse("go.mod", []byte(goMod), nil)
	if err != nil {
		return GoMod{}, err
	}
	if file.Module == nil || file.Module.Mod.Path == "" {
		return GoMod{}, errors.New("go.mod declares no module path")
	}
	required := make(map[string]string, len(file.Require))
	for _, r := range file.Require {
		if version, ok := required[r.Mod.Path]; !ok || semver.Compare(r.Mod.Version, version) > 0 {
			required[r.Mod.Path] = r.Mod.Version
		}
	}
	return GoMod{Module: file.Module.Mod.Path, Require: required}, nil
}

// MissingModules returns, in module path order, the modules the builder's
// go.mod lacks to build design at the versions it was evaluated with, or
// newer ones. design maps each design file's path to its text; saved and
// builder are what Requirements reads from the revision's saved go.mod and
// from the builder's go.mod. The module that provides a package a design file
// imports is, of saved's own module and the modules saved requires, the one
// whose path is the longest prefix of the import path, element by element.
// When that is a required module, it is returned at saved's version if
// builder does not require it or requires an older version. Imports of the
// standard library, whose first path element has no dot, of saved's own
// module, such as the design's own packages, and of packages no module of
// saved provides name nothing. A design file Go cannot parse is refused with
// its file and line.
func MissingModules(design map[string]string, saved, builder GoMod) ([]module.Version, error) {
	needed := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range slices.Sorted(maps.Keys(design)) {
		file, err := parser.ParseFile(fset, name, design[name], parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				// The parser accepts only import paths that unquote, so this
				// is a parser defect, reported rather than passed over.
				return nil, fmt.Errorf("%s: import path %s: %w", fset.Position(spec.Path.Pos()), spec.Path.Value, err)
			}
			if mod, ok := providingModule(saved, imported); ok {
				needed[mod] = saved.Require[mod]
			}
		}
	}
	var missing []module.Version
	for _, mod := range slices.Sorted(maps.Keys(needed)) {
		if have, ok := builder.Require[mod]; !ok || semver.Compare(have, needed[mod]) < 0 {
			missing = append(missing, module.Version{Path: mod, Version: needed[mod]})
		}
	}
	return missing, nil
}

// providingModule returns the module mod requires whose path is the longest
// prefix of imported, a package path, element by element: imported itself,
// then each shorter prefix ending before a slash. It reports false for a
// package of the standard library, whose first path element has no dot, as
// the go command decides; for a package of mod's own module, when its path is
// the longer prefix; and for a package no module of mod provides.
func providingModule(mod GoMod, imported string) (string, bool) {
	first, _, _ := strings.Cut(imported, "/")
	if !strings.Contains(first, ".") {
		return "", false
	}
	for prefix := imported; prefix != "."; prefix = path.Dir(prefix) {
		if prefix == mod.Module {
			return "", false
		}
		if _, ok := mod.Require[prefix]; ok {
			return prefix, true
		}
	}
	return "", false
}
