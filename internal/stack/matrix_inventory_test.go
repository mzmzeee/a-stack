package stack

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func matrixSourceFiles(t *testing.T, pattern string) []*ast.File {
	t.Helper()

	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}

	files := []*ast.File{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		files = append(files, file)
	}

	return files
}

// Recognize the router's URL.Path and its browserAsset local alias.
func matrixPathExpr(expr ast.Expr) bool {
	if name, ok := expr.(*ast.Ident); ok {
		return name.Name == "path"
	}

	path, ok := expr.(*ast.SelectorExpr)
	if !ok || path.Sel.Name != "Path" {
		return false
	}

	url, ok := path.X.(*ast.SelectorExpr)

	return ok && url.Sel.Name == "URL"
}

func matrixRouteSegment(expr ast.Expr) bool {
	index, ok := expr.(*ast.IndexExpr)
	if !ok {
		return false
	}

	name, named := index.X.(*ast.Ident)
	position, numbered := index.Index.(*ast.BasicLit)
	return named && name.Name == "parts" && numbered && position.Value == "1"
}

func matrixString(expr ast.Expr) (string, error) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", fmt.Errorf("unsupported expression %T: use a string literal or extend the inventory parser", expr)
	}

	return strconv.Unquote(lit.Value)
}

var matrixRoutePrefixes = []string{"/v1/session/transfer/"}

func matrixParseRoutes(files []*ast.File) ([]string, error) {
	found := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}

			recv, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}

			id, ok := recv.X.(*ast.Ident)
			if !ok || id.Name != "Service" {
				continue
			}

			var parseErr error
			add := func(expr ast.Expr, segment bool) {
				if parseErr != nil {
					return
				}

				value, err := matrixString(expr)
				if err != nil {
					parseErr = fmt.Errorf("unsupported route in %s: %w", fn.Name.Name, err)
					return
				}

				if segment {
					value = "/" + value + "/{workspace}/{session}"
				}

				if strings.HasPrefix(value, "/") {
					found[value] = true
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if parseErr != nil {
					return false
				}

				switch node := n.(type) {
				case *ast.SwitchStmt:
					if matrixPathExpr(node.Tag) {
						for _, stmt := range node.Body.List {
							for _, expr := range stmt.(*ast.CaseClause).List {
								add(expr, false)
							}
						}
					}
				case *ast.BinaryExpr:
					if node.Op == token.EQL || node.Op == token.NEQ {
						if matrixPathExpr(node.X) {
							add(node.Y, false)
						}

						if matrixPathExpr(node.Y) {
							add(node.X, false)
						}

						if matrixRouteSegment(node.X) {
							add(node.Y, true)
						}

						if matrixRouteSegment(node.Y) {
							add(node.X, true)
						}
					}
				case *ast.CallExpr:
					if len(node.Args) == 2 && matrixPathExpr(node.Args[0]) {
						if method, ok := node.Fun.(*ast.SelectorExpr); ok && method.Sel.Name == "HasPrefix" {
							prefix, err := matrixString(node.Args[1])
							if err != nil || !slices.Contains(matrixRoutePrefixes, prefix) {
								parseErr = fmt.Errorf("unsupported route prefix in %s: %q (%v)", fn.Name.Name, prefix, err)
							}
						}
					}
				}

				return true
			})

			if parseErr != nil {
				return nil, parseErr
			}
		}
	}

	return matrixNames(found), nil
}

func matrixParseCommands(files []*ast.File) ([]string, error) {
	sources := map[string][]string{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}

			for _, spec := range gen.Specs {
				v, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for i, name := range v.Names {
					switch name.Name {
					case "lifecycleCommands", "taskCommands", "mailCommands", "helpGroups":
						if _, exists := sources[name.Name]; exists {
							return nil, fmt.Errorf("duplicate command source %s", name.Name)
						}

						if i >= len(v.Values) {
							return nil, fmt.Errorf("%s has no explicit initializer", name.Name)
						}

						entries, err := matrixCommandTable(v.Values[i], name.Name == "helpGroups")
						if err != nil {
							return nil, fmt.Errorf("%s: %w", name.Name, err)
						}

						sources[name.Name] = entries
					}
				}
			}
		}
	}

	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			classifier := strings.HasPrefix(fn.Name.Name, "is") && strings.HasSuffix(fn.Name.Name, "Command")
			for node := range ast.Preorder(fn.Body) {
				if loop, ok := node.(*ast.RangeStmt); ok && classifier {
					source, named := loop.X.(*ast.Ident)
					if !named || len(sources[source.Name]) == 0 {
						return nil, fmt.Errorf("unsupported command source in %s", fn.Name.Name)
					}
				}

				var writes []ast.Expr
				switch node := node.(type) {
				case *ast.AssignStmt:
					writes = node.Lhs
				case *ast.CallExpr:
					if builtin, ok := node.Fun.(*ast.Ident); ok && len(node.Args) > 0 && (builtin.Name == "copy" || builtin.Name == "clear") {
						writes = node.Args[:1]
					}
				}

				for _, lhs := range writes {
					for target := range ast.Preorder(lhs) {
						if id, ok := target.(*ast.Ident); ok && sources[id.Name] != nil {
							return nil, fmt.Errorf("write to command source %s outside declaration", id.Name)
						}
					}
				}
			}
		}
	}

	// Validate each source before taking the union. Help is documentation, not
	// evidence that any dispatch table was discovered or understood.
	found := map[string]bool{}
	for _, name := range []string{"lifecycleCommands", "taskCommands", "mailCommands", "helpGroups"} {
		if len(sources[name]) == 0 {
			return nil, fmt.Errorf("missing or empty command source %s", name)
		}

		for _, command := range sources[name] {
			found[command] = true
		}
	}

	return matrixNames(found), nil
}

func matrixCommandTable(expr ast.Expr, help bool) ([]string, error) {
	table, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("unsupported command table %T", expr)
	}

	commands := []string{}
	for _, entry := range table.Elts {
		if !help {
			command, err := matrixString(entry)
			if err != nil {
				return nil, err
			}

			if command == "" {
				return nil, fmt.Errorf("empty dispatch command")
			}

			commands = append(commands, command)
			continue
		}

		group, ok := entry.(*ast.CompositeLit)
		if !ok || len(group.Elts) != 2 {
			return nil, fmt.Errorf("unsupported help group")
		}

		rows, ok := group.Elts[1].(*ast.CompositeLit)
		if !ok {
			return nil, fmt.Errorf("unsupported help rows")
		}

		for _, entry := range rows.Elts {
			row, ok := entry.(*ast.CompositeLit)
			if !ok || len(row.Elts) != 3 {
				return nil, fmt.Errorf("unsupported help row")
			}

			command, err := matrixString(row.Elts[0])
			if err != nil {
				return nil, err
			}

			if command != "" {
				commands = append(commands, command)
			}
		}
	}

	return commands, nil
}

func matrixNames(found map[string]bool) []string { return slices.Sorted(maps.Keys(found)) }

func TestMatrixInventoryRejectsPartialParse(t *testing.T) {
	const commands = `package main
 var lifecycleCommands=[]string{"session inspect"}
 var taskCommands=[]string{"task show"}
 var mailCommands=[]string{"mail read"}
 var helpGroups=[]helpGroup{{"Tasks",[]helpRow{{"session inspect","",""},{"task show","",""},{"mail read","",""}}}}
 `
	for _, tc := range []struct {
		name, source, want string
		routes             bool
	}{
		{"constant route", `package stack
 const extra="/v1/extra"
 func(s *Service) dispatch(){switch r.URL.Path {case extra,"/v1/health":}}
 `, "unsupported route", true},
		{"constant command", strings.Replace(commands, `var mailCommands=[]string{"mail read"}`, `const extra="mail extra"; var mailCommands=[]string{"mail read",extra}`, 1), "mailCommands", false},
		// Help still supplies mail read: it must not mask the missing dispatch table.
		{"missing mail source", strings.Replace(commands, `var mailCommands=[]string{"mail read"}`, "", 1), "mailCommands", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "probe.go", tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}

			if tc.routes {
				_, err = matrixParseRoutes([]*ast.File{file})
			} else {
				_, err = matrixParseCommands([]*ast.File{file})
			}

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want explicit %s rejection, got %v", tc.want, err)
			}
		})
	}

	// A small complete inventory remains accepted; the guard is not simply
	// rejecting every fixture or depending on the repository's current row count.
	file, err := parser.ParseFile(token.NewFileSet(), "valid.go", commands, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, err := matrixParseCommands([]*ast.File{file})
	if err != nil || fmt.Sprint(got) != "[mail read session inspect task show]" {
		t.Fatalf("complete tables: %v %v", got, err)
	}
}
