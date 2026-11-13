// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is a source-structure guard for the controller-lifetime convention, not
// lifecycle coverage. Runtime request-isolation and resource contract tests
// separately exercise the delegated operations and their effects.
func TestResourceLifecycleDelegationUsesLocalControllerLiterals(t *testing.T) {
	paths, err := filepath.Glob("*/*.go")
	require.NoError(t, err)
	resources, methods := 0, 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		require.NoError(t, err)
		var controller, wrapper string
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "New") {
				continue
			}
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			result, ok := fn.Type.Results.List[0].Type.(*ast.SelectorExpr)
			if !ok || result.Sel.Name != "InferredResource" {
				continue
			}
			controller = strings.TrimPrefix(fn.Name.Name, "New")
			require.Len(t, fn.Body.List, 1, path)
			ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
			require.True(t, ok, path)
			literal, ok := ret.Results[0].(*ast.CompositeLit)
			require.True(t, ok, path)
			name, ok := literal.Type.(*ast.Ident)
			require.True(t, ok, path)
			wrapper = name.Name
		}
		if controller == "" {
			continue
		}
		resources++
		found := map[string]bool{}
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			receiver, ok := fn.Recv.List[0].Type.(*ast.Ident)
			if !ok || receiver.Name != wrapper {
				continue
			}
			switch fn.Name.Name {
			case "Check", "Diff", "Create", "Read", "Update", "Delete":
			default:
				continue
			}
			found[fn.Name.Name] = true
			delegations := 0
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				method, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || method.Sel.Name != fn.Name.Name {
					return true
				}
				inferred, ok := method.X.(*ast.CallExpr)
				if !ok {
					return true
				}
				factory, ok := inferred.Fun.(*ast.SelectorExpr)
				if !ok || factory.Sel.Name != "Resource" {
					return true
				}
				pkg, ok := factory.X.(*ast.Ident)
				require.True(t, ok, path)
				require.Equal(t, "infer", pkg.Name, path)
				require.Len(t, inferred.Args, 1, path)
				value := localControllerAllocation(t, inferred.Args[0], path)
				name, ok := value.Type.(*ast.Ident)
				require.True(t, ok, path)
				require.Equal(t, controller, name.Name, path)
				for _, element := range value.Elts {
					field, ok := element.(*ast.KeyValueExpr)
					require.True(t, ok, path)
					_, ok = field.Key.(*ast.Ident)
					require.True(t, ok, path)
					switch operation := field.Value.(type) {
					case *ast.UnaryExpr:
						localControllerAllocation(t, operation, path)
					case *ast.Ident:
						require.NotNil(t, operation.Obj, path)
						assignment, ok := operation.Obj.Decl.(*ast.AssignStmt)
						require.True(t, ok, "operation must be allocated in this method: %s", path)
						require.Equal(t, token.DEFINE, assignment.Tok, path)
						require.Len(t, assignment.Rhs, 1, path)
						localControllerAllocation(t, assignment.Rhs[0], path)
					default:
						t.Fatalf("operation must not come from shared wrapper state: %s", path)
					}
				}
				delegations++
				return true
			})
			require.Equal(t, 1, delegations, "%s.%s must delegate through one fresh controller", controller, fn.Name.Name)
			methods++
		}
		require.Len(t, found, 6, "%s must not inherit lifecycle delegation from the registered schema instance", controller)
	}
	require.Equal(t, len(loadSchema(t).Resources), resources)
	require.Equal(t, resources*6, methods)
}

func localControllerAllocation(t *testing.T, expression ast.Expr, path string) *ast.CompositeLit {
	t.Helper()
	address, ok := expression.(*ast.UnaryExpr)
	require.True(t, ok, "expected an invocation-local allocation in %s", path)
	require.Equal(t, token.AND, address.Op, path)
	literal, ok := address.X.(*ast.CompositeLit)
	require.True(t, ok, "expected a fresh struct literal in %s", path)
	return literal
}
