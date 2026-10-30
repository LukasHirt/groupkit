// SPDX-FileCopyrightText: 2026 groupkit contributors
//
// SPDX-License-Identifier: Apache-2.0

package groupkit_test

import (
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// publicPackages returns the directories of the importable packages: every
// directory with Go files, except internal packages, examples and test data.
func publicPackages(t *testing.T) []string {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != "." && (strings.HasPrefix(name, ".") || name == "internal" || name == "examples" ||
			name == "testdata" || name == "docs") {
			return filepath.SkipDir
		}
		goFiles, err := filepath.Glob(filepath.Join(path, "*.go"))
		if err != nil {
			return err
		}
		for _, f := range goFiles {
			if !strings.HasSuffix(f, "_test.go") {
				dirs = append(dirs, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) < 10 {
		t.Fatalf("found only %d public packages: %v", len(dirs), dirs)
	}
	return dirs
}

// loadPackage parses the non-test Go files of dir and returns the package
// documentation.
func loadPackage(t *testing.T, dir string) *doc.Package {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("%s: no Go files", dir)
	}
	pkg, err := doc.NewFromFiles(fset, files, "github.com/LukasHirt/groupkit/"+filepath.ToSlash(dir))
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	return pkg
}

// startsWithName reports whether a doc comment begins with the identifier.
func startsWithName(comment, name string) bool {
	comment = strings.TrimSpace(comment)
	rest, ok := strings.CutPrefix(comment, name)
	if !ok {
		return false
	}
	return rest == "" || !isIdentRune(rest[0])
}

func isIdentRune(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// commentText returns the text of a doc or line comment group, or "".
func commentText(groups ...*ast.CommentGroup) string {
	for _, g := range groups {
		if g != nil && strings.TrimSpace(g.Text()) != "" {
			return g.Text()
		}
	}
	return ""
}

// checkValues checks the exported constants or variables of one group of
// declarations. A declaration needs a comment that starts with its name. A
// group of several names may instead have one group comment.
func checkValues(t *testing.T, where string, values []*doc.Value) {
	t.Helper()
	for _, v := range values {
		decl := v.Decl
		grouped := decl.Lparen.IsValid()
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				if !id.IsExported() {
					continue
				}
				own := commentText(vs.Doc, vs.Comment)
				switch {
				case own != "":
					if vs.Doc != nil && !startsWithName(own, id.Name) && len(vs.Names) == 1 {
						t.Errorf("%s: the comment of %s must start with its name", where, id.Name)
					}
				case grouped && strings.TrimSpace(v.Doc) != "":
					// The group comment covers the names in the group.
				case !grouped && strings.TrimSpace(v.Doc) != "":
					if !startsWithName(v.Doc, id.Name) {
						t.Errorf("%s: the comment of %s must start with its name", where, id.Name)
					}
				default:
					t.Errorf("%s: %s has no doc comment", where, id.Name)
				}
			}
		}
	}
}

func checkFunc(t *testing.T, where, name string, f *doc.Func) {
	t.Helper()
	switch {
	case strings.TrimSpace(f.Doc) == "":
		t.Errorf("%s: %s has no doc comment", where, name)
	case !startsWithName(f.Doc, f.Name):
		t.Errorf("%s: the comment of %s must start with %q", where, name, f.Name)
	}
}

// checkFields requires a comment on every exported field of an exported
// struct and every method of an exported interface.
func checkFields(t *testing.T, where string, ts *ast.TypeSpec) {
	t.Helper()
	var fields *ast.FieldList
	switch ty := ts.Type.(type) {
	case *ast.StructType:
		fields = ty.Fields
	case *ast.InterfaceType:
		fields = ty.Methods
	}
	if fields == nil {
		return
	}
	for _, f := range fields.List {
		if len(f.Names) == 0 {
			continue // embedded
		}
		for _, id := range f.Names {
			if id.IsExported() && commentText(f.Doc, f.Comment) == "" {
				t.Errorf("%s: %s.%s has no comment", where, ts.Name.Name, id.Name)
			}
		}
	}
}

func TestExportedIdentifiersAreDocumented(t *testing.T) {
	for _, dir := range publicPackages(t) {
		pkg := loadPackage(t, dir)
		where := dir
		if wantPrefix := "Package " + pkg.Name; !strings.HasPrefix(strings.TrimSpace(pkg.Doc), wantPrefix) {
			t.Errorf("%s: the package comment must start with %q", where, wantPrefix)
		}
		checkValues(t, where, pkg.Consts)
		checkValues(t, where, pkg.Vars)
		for _, f := range pkg.Funcs {
			checkFunc(t, where, f.Name, f)
		}
		for _, ty := range pkg.Types {
			switch {
			case strings.TrimSpace(ty.Doc) == "":
				t.Errorf("%s: type %s has no doc comment", where, ty.Name)
			case !startsWithName(ty.Doc, ty.Name):
				t.Errorf("%s: the comment of type %s must start with its name", where, ty.Name)
			}
			for _, spec := range ty.Decl.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.Name == ty.Name {
					checkFields(t, where, ts)
				}
			}
			checkValues(t, where, ty.Consts)
			checkValues(t, where, ty.Vars)
			for _, f := range ty.Funcs {
				checkFunc(t, where, f.Name, f)
			}
			for _, m := range ty.Methods {
				checkFunc(t, where, ty.Name+"."+m.Name, m)
			}
		}
	}
}
