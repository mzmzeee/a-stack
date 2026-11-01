package stack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

func TestMatrixSyntaxContract(t *testing.T) {
	const commands = `package main
 var lifecycleCommands=[]string{"session inspect"}
 var taskCommands=[]string{"task show"}
 var mailCommands=[]string{"mail read"}
 var helpGroups=[]helpGroup{{"Mail",[]helpRow{{"mail read","",""}}}}
 `
	for _, tc := range []struct {
		name, source, want string
		route              bool
	}{
		{"unknown prefix", `package stack
 func(s *Service) dispatch(){ if strings.HasPrefix(r.URL.Path,"/v1/review-extra/") { return } }
 `, "unsupported route prefix", true},
		{"append in init", commands + `func init(){mailCommands=append(mailCommands,"mail extra")}`, "write to command source", false},
		{"element write", commands + `func init(){mailCommands[0]="mail extra"}`, "write to command source", false},
		{"copy write", commands + `func init(){copy(mailCommands,[]string{"mail extra"})}`, "write to command source", false},
		{"clear write", commands + `func init(){clear(mailCommands)}`, "write to command source", false},
		{"new classifier group", commands + `var reviewCommands=[]string{"review extra"}
 func isMailCommand(command string)bool { for _,c:=range reviewCommands {if c==command{return true}}; return false }`, "unsupported command source", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "probe.go", tc.source, 0)
			if err != nil {
				t.Fatal(err)
			}

			if tc.route {
				_, err = matrixParseRoutes([]*ast.File{file})
			} else {
				_, err = matrixParseCommands([]*ast.File{file})
			}

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}

	t.Run("exact trailing slash and known prefix", func(t *testing.T) {
		file, err := parser.ParseFile(token.NewFileSet(), "probe.go", `package stack
 func(s *Service) dispatch(){ if r.URL.Path=="/v1/exact/" {return}; if strings.HasPrefix(r.URL.Path,"/v1/session/transfer/"){return} }
 `, 0)
		if err != nil {
			t.Fatal(err)
		}

		routes, err := matrixParseRoutes([]*ast.File{file})
		if err != nil || !slices.Equal(routes, []string{"/v1/exact/"}) {
			t.Fatalf("exact endpoint lost or known grouping prefix treated as endpoint: %v %v", routes, err)
		}
	})
}
