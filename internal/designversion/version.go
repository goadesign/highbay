// Package designversion owns the versions of a design file, one of the Go
// files under a module's design/ folder that the guide drafts and the builder
// builds from. A version is a file's text in gofmt form (Canonical), named by
// the SHA-256 of that text (Digest), so texts that gofmt prints alike are one
// version: a change of layout, of import order within a group or of line
// endings makes no new version, while a formatter that changes more than
// gofmt does, such as goimports regrouping imports, does.
//
// Around that form the package reads the guide's draft as a refresh
// evaluates it (ReadDraft), merges a design change made outside Surla into
// the draft's text of one file (MergeFile), and finds the modules a builder's
// go.mod lacks to build a design as it was evaluated (Requirements,
// MissingModules). It keeps no state and writes no file but MergeFile's
// temporary ones.
package designversion

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
)

// Canonical returns src, the text of the Go source file at path, as gofmt
// prints it: the form in which a version of a design file is compared, saved
// and written. A text already in that form comes back unchanged. src must be
// a whole source file, starting with its package clause; a text Go cannot
// parse as one is refused with the parser's scanner.ErrorList, whose message
// names path, line and column, such as "design/types.go:3:1: expected
// declaration, found '<<'". path only names the file in that error.
func Canonical(path, src string) (string, error) {
	_, text, err := parseCanonical(token.NewFileSet(), path, src)
	return text, err
}

// Digest returns the name of a version: the lowercase hexadecimal SHA-256 of
// text, a design file's text in gofmt form, so two versions share a name only
// when their texts are equal.
func Digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// parseCanonical parses src, the text of the Go source file named name, as a
// whole file into fset and returns its syntax tree, whose positions are the
// lines and columns of src as written, together with src in gofmt form. A
// text Go cannot parse is the parser's scanner.ErrorList, naming name, line
// and column.
func parseCanonical(fset *token.FileSet, name, src string) (*ast.File, string, error) {
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, "", err
	}
	// format.Source prints a whole file exactly as gofmt does, imports sorted
	// and nothing simplified. It would also format a list of declarations or
	// statements without a package clause; the parse above refuses those.
	formatted, err := format.Source([]byte(src))
	if err != nil {
		return nil, "", fmt.Errorf("format %s: %w", name, err)
	}
	return file, string(formatted), nil
}
