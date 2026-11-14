// This file reads the modules a go.mod requires and finds those a builder's
// go.mod lacks to build a design as it was evaluated.
package designversion

import (
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

// Requirements returns the modules goMod, the text of a go.mod file,
// requires, each module path with its version, from every require directive,
// direct or indirect. A module required twice counts at the higher version,
// the one Go selects. A text Go cannot parse as a go.mod is refused with
// modfile's error, which names go.mod and the line.
func Requirements(goMod string) (map[string]string, error) {
	file, err := modfile.Parse("go.mod", []byte(goMod), nil)
	if err != nil {
		return nil, err
	}
	required := make(map[string]string, len(file.Require))
	for _, r := range file.Require {
		if version, ok := required[r.Mod.Path]; !ok || semver.Compare(r.Mod.Version, version) > 0 {
			required[r.Mod.Path] = r.Mod.Version
		}
	}
	return required, nil
}

// MissingModules returns, in module path order, the modules a builder's
// go.mod lacks to build design at the versions it was evaluated with, or
// newer ones. design maps each design file's path to its text; saved and
// builder are the Requirements of the revision's saved go.mod and of the
// builder's go.mod. For each package a design file imports, the module that
// provides it is the module saved requires whose path is the longest prefix
// of the import path, element by element; it is returned at saved's version
// when builder does not require it or requires an older version. Imports of
// the standard library, whose first path element has no dot, and of packages
// no module of saved provides, such as the design's own, name nothing. A
// design file Go cannot parse is refused with its file and line.
func MissingModules(design, saved, builder map[string]string) ([]module.Version, error) {
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
				needed[mod] = saved[mod]
			}
		}
	}
	var missing []module.Version
	for _, mod := range slices.Sorted(maps.Keys(needed)) {
		if have, ok := builder[mod]; !ok || semver.Compare(have, needed[mod]) < 0 {
			missing = append(missing, module.Version{Path: mod, Version: needed[mod]})
		}
	}
	return missing, nil
}

// providingModule returns the module of required whose path is the longest
// prefix of imported, a package path, element by element: imported itself,
// then each shorter prefix ending before a slash. It reports false for a
// package of the standard library, whose first path element has no dot, as
// the go command decides, and for a package no module of required provides.
func providingModule(required map[string]string, imported string) (string, bool) {
	first, _, _ := strings.Cut(imported, "/")
	if !strings.Contains(first, ".") {
		return "", false
	}
	for prefix := imported; prefix != "."; prefix = path.Dir(prefix) {
		if _, ok := required[prefix]; ok {
			return prefix, true
		}
	}
	return "", false
}
