// Command racecheck rejects production concurrency outside the packages that
// CI instruments with the race detector. It checks syntax across build tags;
// generated code and fixture/test programs do not establish production owners.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// main checks the repository rooted at the working directory.
func main() {
	if err := check(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// check reads the same package list that workflow test invocations use, then
// checks handwritten Go sources against their nearest owning module.
func check() (err error) {
	file, err := os.Open(".github/race-packages.txt")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	owners := make(map[string]bool)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		if scanner.Text() != name {
			return fmt.Errorf("race owner must not have surrounding whitespace: %q", scanner.Text())
		}
		if owners[name] {
			return fmt.Errorf("duplicate race owner %s", name)
		}
		owners[name] = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	var findings []error
	observed := make(map[string]bool)
	err = filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("cannot inspect symlinked Go source %s", path)
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		if ast.IsGenerated(f) || !concurrent(f) {
			return nil
		}
		name, err := packageName(filepath.Dir(path))
		if err != nil {
			return err
		}
		if !owners[name] {
			findings = append(findings, fmt.Errorf("%s owns concurrency: add %s to .github/race-packages.txt", path, name))
		}
		observed[name] = true
		return nil
	})
	for name := range owners {
		if !observed[name] {
			findings = append(findings, fmt.Errorf("race owner %s has no handwritten production concurrency", name))
		}
	}
	return errors.Join(err, errors.Join(findings...))
}

// concurrent recognizes concurrency syntax, not comments, strings or names of
// unrelated functions. Channel receives can appear without a local channel type.
func concurrent(file *ast.File) bool {
	for _, spec := range file.Imports {
		name, err := strconv.Unquote(spec.Path.Value)
		if err == nil && (name == "sync" || name == "sync/atomic" || strings.HasPrefix(name, "golang.org/x/sync/")) {
			return true
		}
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.GoStmt, *ast.ChanType, *ast.SendStmt, *ast.SelectStmt:
			found = true
		case *ast.UnaryExpr:
			found = found || value.Op == token.ARROW
		}
		return !found
	})
	return found
}

// packageName derives the import path from the nearest go.mod rather than
// assuming that examples and the Story module belong to the root module.
func packageName(dir string) (string, error) {
	for parent := dir; ; parent = filepath.Dir(parent) {
		path := filepath.Join(parent, "go.mod")
		raw, err := os.ReadFile(path)
		if err == nil {
			module, err := modfile.Parse(path, raw, nil)
			if err != nil {
				return "", err
			}
			if module.Module == nil {
				return "", fmt.Errorf("missing module declaration in %s", path)
			}
			relative, err := filepath.Rel(parent, dir)
			if err != nil {
				return "", err
			}
			if relative == "." {
				return module.Module.Mod.Path, nil
			}
			return module.Module.Mod.Path + "/" + filepath.ToSlash(relative), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if parent == "." {
			return "", fmt.Errorf("no owning module for %s", dir)
		}
	}
}
