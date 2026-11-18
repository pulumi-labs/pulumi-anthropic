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
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/archive"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
)

// These tests guard whole classes of defect across every resource, rather than one
// resource at a time, so that a new resource cannot quietly reintroduce them.

// This source-policy guard prevents reintroducing initialization checkpoint
// markers or processing without an explicit decision to support their lifecycle.
// Resource tests separately assert ordinary errors and successful-state mapping.
func TestProviderSourcesDoNotUseInitializationCheckpoints(t *testing.T) {
	require.NoError(t, filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				switch selector.Sel.Name {
				case "ResourceInitFailedError", "InitializationFailed", "PartialState":
					t.Errorf("%s uses initialization checkpoint API %s", path, selector.Sel.Name)
				}
			}
			return true
		})
		return nil
	}))
}

type schemaProperty struct {
	Type             string          `json:"type"`
	Ref              string          `json:"$ref"`
	Items            *schemaProperty `json:"items"`
	Enum             []struct{ Value any }
	Description      string `json:"description"`
	Secret           bool   `json:"secret"`
	ReplaceOnChanges bool   `json:"replaceOnChanges"`
}

type schemaObject struct {
	Description     string                    `json:"description"`
	Properties      map[string]schemaProperty `json:"properties"`
	InputProperties map[string]schemaProperty `json:"inputProperties"`
	Required        []string                  `json:"required"`
	RequiredInputs  []string                  `json:"requiredInputs"`
	Enum            []struct {
		Value any `json:"value"`
	} `json:"enum"`
}

type schemaDoc struct {
	Resources map[string]schemaObject `json:"resources"`
	Types     map[string]schemaObject `json:"types"`
	Functions map[string]struct {
		Description string       `json:"description"`
		Inputs      schemaObject `json:"inputs"`
		Outputs     schemaObject `json:"outputs"`
	} `json:"functions"`
}

func loadSchema(t *testing.T) schemaDoc {
	var doc schemaDoc
	require.NoError(t, json.Unmarshal(schema(t), &doc))
	return doc
}

func typeOf(doc schemaDoc, prop schemaProperty) (schemaObject, bool) {
	obj, ok := doc.Types[strings.TrimPrefix(prop.Ref, "#/types/")]
	return obj, ok && prop.Ref != ""
}

// replacePath is a path to a replaceOnChanges input, with a value to change it from and
// to.
type replacePath struct {
	path     []string
	old, new property.Value
}

// replacePaths finds every replaceOnChanges input of a resource, at any depth through
// nested object types.
func replacePaths(doc schemaDoc, props map[string]schemaProperty, prefix []string) []replacePath {
	var paths []replacePath
	for name, prop := range props {
		path := append(append([]string(nil), prefix...), name)
		if prop.ReplaceOnChanges {
			if old, new, ok := twoValues(doc, prop); ok {
				paths = append(paths, replacePath{path, old, new})
			}
		}
		if obj, ok := typeOf(doc, prop); ok && obj.Properties != nil {
			paths = append(paths, replacePaths(doc, obj.Properties, path)...)
		}
	}
	return paths
}

// twoValues returns two distinct values a property can take.
func twoValues(doc schemaDoc, prop schemaProperty) (property.Value, property.Value, bool) {
	if obj, ok := typeOf(doc, prop); ok && len(obj.Enum) >= 2 {
		return property.New(obj.Enum[0].Value.(string)), property.New(obj.Enum[1].Value.(string)), true
	}
	switch prop.Type {
	case "string":
		return property.New("a"), property.New("b"), true
	case "integer", "number":
		return property.New(1.0), property.New(2.0), true
	case "boolean":
		return property.New(false), property.New(true), true
	case "array":
		if prop.Items != nil && prop.Items.Type == "string" {
			return property.New([]property.Value{property.New("a")}),
				property.New([]property.Value{property.New("b")}), true
		}
	}
	return property.Value{}, property.Value{}, false
}

// skeleton builds the smallest valid value of an object: every required property set,
// recursively, to an arbitrary value.
func skeleton(doc schemaDoc, props map[string]schemaProperty, required []string) property.Map {
	m := map[string]property.Value{}
	for _, name := range required {
		m[name] = skeletonValue(doc, props[name])
	}
	return property.NewMap(m)
}

func skeletonValue(doc schemaDoc, prop schemaProperty) property.Value {
	if obj, ok := typeOf(doc, prop); ok && obj.Properties != nil {
		return property.New(skeleton(doc, obj.Properties, obj.Required))
	}
	if prop.Ref == "pulumi.json#/Archive" {
		a, _ := archive.FromAssets(map[string]any{})
		return property.New(a)
	}
	switch prop.Type {
	case "array":
		return property.New([]property.Value{})
	case "object":
		return property.New(property.Map{})
	}
	v, _, _ := twoValues(doc, prop)
	return v
}

// withPath returns m with the value at path set to v, building a skeleton for any
// object along the way that m does not already have.
func withPath(doc schemaDoc, m property.Map, props map[string]schemaProperty, path []string, v property.Value) property.Map {
	if len(path) > 1 {
		obj, _ := typeOf(doc, props[path[0]])
		inner := skeleton(doc, obj.Properties, obj.Required)
		if existing, ok := m.GetOk(path[0]); ok && existing.IsMap() {
			inner = existing.AsMap()
		}
		v = property.New(withPath(doc, inner, obj.Properties, path[1:], v))
	}
	return m.Set(path[0], v)
}

// TestEveryReplaceOnChangesReplaces changes each replaceOnChanges input, at whatever
// depth it is declared, and asserts that the provider's diff replaces the resource. The
// framework's default diff honors the tag only on top-level inputs.
func TestEveryReplaceOnChangesReplaces(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)
	prov, err := Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"),
		integration.WithProvider(prov))
	require.NoError(t, err)

	probed := 0
	for token, res := range doc.Resources {
		for _, rp := range replacePaths(doc, res.InputProperties, nil) {
			name := token + ":" + strings.Join(rp.path, ".")
			urn := resource.NewURN("test", "test", "", tokens.Type(token), "r")
			inputs := skeleton(doc, res.InputProperties, res.RequiredInputs)
			state := skeleton(doc, res.Properties, res.Required)
			olds := withPath(doc, inputs, res.InputProperties, rp.path, rp.old)
			d, err := server.Diff(p.DiffRequest{ID: "id", Urn: urn,
				State:     withPath(doc, state, res.Properties, rp.path, rp.old),
				OldInputs: olds,
				Inputs:    withPath(doc, inputs, res.InputProperties, rp.path, rp.new)})
			if !assert.NoError(t, err, name) {
				continue
			}
			// Diff may report an immutable leaf or an enclosing object.
			// Only the changed immutable path or an ancestor
			// may establish replacement, never an unrelated sibling's diff.
			replaces := false
			for depth := len(rp.path); depth > 0; depth-- {
				kind := d.DetailedDiff[strings.Join(rp.path[:depth], ".")].Kind
				if kind == p.UpdateReplace || kind == p.AddReplace || kind == p.DeleteReplace {
					replaces = true
					break
				}
			}
			assert.True(t, replaces, "changing %s must replace that path or an ancestor; diff=%v", name, d.DetailedDiff)
			probed++
		}
	}
	assert.NotZero(t, probed)
}

// TestEveryResourceHasLifecycleTestSourceReference checks for a resource token in
// a file containing lifecycle-test source. This is a source inventory check, not
// proof that a particular lifecycle executes or that its assertions are adequate.
func TestEveryResourceHasLifecycleTestSourceReference(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)
	var tests strings.Builder
	files, err := filepath.Glob("*/*_test.go")
	require.NoError(t, err)
	for _, f := range files {
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		// Workspace drives the same Check/Create/Diff/Update/Delete protocol
		// explicitly: LifeCycleTest omits OldInputs, which are essential for
		// deleting only previously configured merged-map keys. TunnelCertificate
		// also drives its lifecycle explicitly to verify import and write-only PEM.
		if strings.Contains(string(src), "integration.LifeCycleTest{") ||
			(filepath.ToSlash(f) == "organization/workspace_test.go" && strings.Contains(string(src), "func TestWorkspaceLifecycle(")) ||
			(filepath.ToSlash(f) == "agents/tunnel_certificate_test.go" && strings.Contains(string(src), "func TestTunnelCertificateLifecycle(")) {
			tests.Write(src)
		}
	}
	for token := range doc.Resources {
		assert.Contains(t, tests.String(), `"`+token+`"`, "%s has no lifecycle-test source reference", token)
	}
}

// TestEverythingIsDocumented requires a description on every resource, function, type,
// and property, since the schema is the registry documentation for every language.
func TestEverythingIsDocumented(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)
	check := func(owner string, obj schemaObject, props ...map[string]schemaProperty) {
		for _, m := range props {
			for name, prop := range m {
				assert.NotEmpty(t, prop.Description, "%s.%s has no description", owner, name)
			}
		}
	}
	for token, res := range doc.Resources {
		assert.NotEmpty(t, res.Description, "%s has no description", token)
		check(token, res, res.InputProperties, res.Properties)
	}
	for token, fn := range doc.Functions {
		assert.NotEmpty(t, fn.Description, "%s has no description", token)
		check(token, fn.Inputs, fn.Inputs.Properties, fn.Outputs.Properties)
	}
	for token, typ := range doc.Types {
		check(token, typ, typ.Properties)
	}
}

// secretName matches property names that hold credentials.
var secretName = regexp.MustCompile(`(?i)(^token$|accessToken$|refreshToken$|tunnelToken$|secretValue$|clientSecret$|password$|privateKey$)`)

// TestCredentialsAreSecret requires every property that holds a credential to be marked
// secret, so it is encrypted in state and masked in output.
func TestCredentialsAreSecret(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)
	check := func(owner string, props map[string]schemaProperty) {
		for name, prop := range props {
			if secretName.MatchString(name) {
				assert.True(t, prop.Secret, "%s.%s holds a credential but is not secret", owner, name)
			}
		}
	}
	for token, res := range doc.Resources {
		check(token, res.InputProperties)
		check(token, res.Properties)
	}
	for token, typ := range doc.Types {
		check(token, typ.Properties)
	}
}

// TestEveryResourceDeleteAcceptsMissingObject checks Delete against the empty fake
// API. It covers absence handling only; resource-specific lifecycle tests must
// assert the requested delete/archive/revoke operation and its effects.
func TestEveryResourceDeleteAcceptsMissingObject(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)
	api := fakeapi.New(t)
	prov, err := Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"),
		integration.WithProvider(prov))
	require.NoError(t, err)
	require.NoError(t, server.Configure(p.ConfigureRequest{Args: property.NewMap(map[string]property.Value{
		"apiKey": property.New("k"), "adminKey": property.New("k"), "authToken": property.New("t"),
		"baseUrl": property.New(api.URL),
	})}))
	for token, res := range doc.Resources {
		urn := resource.NewURN("test", "test", "", tokens.Type(token), "r")
		state := skeleton(doc, res.Properties, res.Required)
		err := server.Delete(p.DeleteRequest{ID: "parent_1/child_1", Urn: urn, Properties: state})
		assert.NoError(t, err, "deleting %s", token)
	}
}
