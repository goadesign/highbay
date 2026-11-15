// This file compares the required modules and effective replacements a
// builder needs to build a design as it was evaluated.
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
// and the modules it requires and replaces.
type GoMod struct {
	// Module is the path of the module the file declares, never empty.
	Module string
	// Require maps the path of each module the file requires, directly or
	// indirectly, to its version. A module required twice is at the higher
	// version, the one Go selects.
	Require map[string]string
	// replacements retains modfile's parsed directives; source preserves their
	// original spelling for builder diagnostics, including block entries.
	replacements []*modfile.Replace
	source       string
}

// Requirements reads goMod, the text of a go.mod file, as the go command
// does, and returns the module it declares and the modules it requires and
// replaces. Replacement directives retain their source for diagnostics. A
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
	return GoMod{
		Module:       file.Module.Mod.Path,
		Require:      required,
		replacements: file.Replace,
		source:       goMod,
	}, nil
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
// saved provides name nothing. For imported required modules it also returns
// replacement findings in module path order. Each file's selected requirement
// determines its effective replacement: an exact version selector takes
// precedence over a wildcard. Matching targets (path and version) are accepted
// regardless of selector or spelling. Findings quote original source and ask
// for equivalent targets at the builder's selected version, without rewriting
// either manifest. Builder replacements are checked at the version selected
// after any returned missing-module upgrade, so the two findings agree. A
// design file Go cannot parse is refused with its file and line; expected
// dependency differences are findings, not errors.
func MissingModules(
	design map[string]string, saved, builder GoMod,
) ([]module.Version, []string, error) {
	needed := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range slices.Sorted(maps.Keys(design)) {
		file, err := parser.ParseFile(fset, name, design[name], parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		for _, spec := range file.Imports {
			imported, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				// The parser accepts only import paths that unquote, so this
				// is a parser defect, reported rather than passed over.
				return nil, nil, fmt.Errorf("%s: import path %s: %w", fset.Position(spec.Path.Pos()), spec.Path.Value, err)
			}
			if mod, ok := providingModule(saved, imported); ok {
				needed[mod] = saved.Require[mod]
			}
		}
	}
	var missing []module.Version
	var findings []string
	for _, mod := range slices.Sorted(maps.Keys(needed)) {
		version, ok := builder.Require[mod]
		if !ok || semver.Compare(version, needed[mod]) < 0 {
			missing = append(missing, module.Version{Path: mod, Version: needed[mod]})
			version = needed[mod]
		}
		want := effectiveReplacement(saved, mod, needed[mod])
		have := effectiveReplacement(builder, mod, version)
		if want == nil && have == nil || want != nil && have != nil && want.New == have.New {
			continue
		}
		findings = append(findings, replacementFinding(saved, builder, mod, version, want, have))
	}
	return missing, findings, nil
}

// effectiveReplacement selects the directive Go applies to name at version.
// The caller supplies the selected requirement, including a requested upgrade.
// Other version selectors do not apply; exact wins over wildcard in either order.
func effectiveReplacement(mod GoMod, name, version string) *modfile.Replace {
	var wildcard *modfile.Replace
	for _, replacement := range mod.replacements {
		if replacement.Old.Path != name {
			continue
		}
		if replacement.Old.Version == version {
			return replacement
		}
		if replacement.Old.Version == "" {
			wildcard = replacement
		}
	}
	return wildcard
}

// replacementFinding tells the builder which effective target differs and
// quotes the original directive, rather than suggesting a version-specific
// saved selector be copied when the builder requires a different version.
func replacementFinding(
	saved, builder GoMod, name, version string, want, have *modfile.Replace,
) string {
	if want == nil {
		return fmt.Sprintf("After any requested requirement upgrade, builder go.mod applies this replacement to %s@%s, but saved go.mod has no effective replacement for %s@%s:\n%s\nRemove or adjust the builder replacement so this module uses its required version without replacement.",
			name, version, name, saved.Require[name], replacementSource(builder, have))
	}
	actual := fmt.Sprintf("Builder go.mod has no effective replacement for %s@%s.", name, version)
	if have != nil {
		actual = fmt.Sprintf("Builder go.mod applies a different replacement to %s@%s:\n%s",
			name, version, replacementSource(builder, have))
	}
	return fmt.Sprintf("Saved go.mod replacement for %s@%s:\n%s\n%s\nApply an equivalent replacement target %s to %s@%s, the builder's selected requirement after any requested upgrade.",
		name, saved.Require[name], replacementSource(saved, want), actual, want.New.String(), name, version)
}

// replacementSource returns the complete original line using modfile's byte
// positions, preserving indentation, quoting and suffix comments. Block
// entries are labeled because they omit the replace keyword themselves.
func replacementSource(mod GoMod, replacement *modfile.Replace) string {
	start, end := replacement.Syntax.Span()
	first := strings.LastIndex(mod.source[:start.Byte], "\n") + 1
	last := len(mod.source)
	if newline := strings.IndexByte(mod.source[end.Byte:], '\n'); newline >= 0 {
		last = end.Byte + newline
	}
	line := mod.source[first:last]
	if replacement.Syntax.InBlock {
		return "Replacement entry inside a replace block:\n" + line
	}
	return line
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
