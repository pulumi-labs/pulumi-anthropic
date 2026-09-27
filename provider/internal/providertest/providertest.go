// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package providertest runs the provider against the fake Anthropic API.
package providertest

import (
	"fmt"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/pulumi-anthropic/provider"
	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
)

// New starts a fake API and a provider configured against it with every credential.
func New(t *testing.T) (integration.Server, *fakeapi.Server) {
	return NewWithConfig(t, map[string]property.Value{
		"apiKey":    property.New("sk-ant-api-test").WithSecret(true),
		"adminKey":  property.New("sk-ant-admin-test").WithSecret(true),
		"authToken": property.New("oauth-test").WithSecret(true),
	})
}

// NewWithConfig starts a fake API and a provider configured against it with the given
// configuration, to which the fake's base URL is added.
func NewWithConfig(t *testing.T, cfg map[string]property.Value) (integration.Server, *fakeapi.Server) {
	t.Helper()
	api := fakeapi.New(t)
	prov, err := provider.Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), provider.Name, semver.MustParse("0.1.0"),
		integration.WithProvider(prov))
	require.NoError(t, err)
	cfg["baseUrl"] = property.New(api.URL)
	require.NoError(t, server.Configure(p.ConfigureRequest{Args: property.NewMap(cfg)}))
	return server, api
}

// URN returns a test URN for a resource type.
func URN(token tokens.Type) resource.URN {
	return resource.NewURN("test", "test", "", token, "test")
}

// Create checks and creates a resource, as the engine does, returning the response and
// the checked inputs.
func Create(t *testing.T, server integration.Server, urn resource.URN, inputs property.Map) (p.CreateResponse, property.Map) {
	t.Helper()
	check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	resp, err := server.Create(p.CreateRequest{Urn: urn, Properties: check.Inputs})
	require.NoError(t, err)
	return resp, check.Inputs
}

// Read refreshes a created resource.
func Read(
	t *testing.T, server integration.Server, urn resource.URN, created p.CreateResponse, inputs property.Map,
) p.ReadResponse {
	t.Helper()
	resp, err := server.Read(p.ReadRequest{ID: created.ID, Urn: urn, Properties: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	return resp
}

// Diff checks new inputs for a created resource and diffs them against it, as the engine
// does.
func Diff(
	t *testing.T, server integration.Server, urn resource.URN, created p.CreateResponse,
	oldInputs, newInputs property.Map,
) p.DiffResponse {
	t.Helper()
	check, err := server.Check(p.CheckRequest{Urn: urn, State: created.Properties, Inputs: newInputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	resp, err := server.Diff(p.DiffRequest{
		ID: created.ID, Urn: urn, State: created.Properties, OldInputs: oldInputs, Inputs: check.Inputs,
	})
	require.NoError(t, err)
	return resp
}

// Props builds a property map from JSON-like Go values, for concise test inputs.
func Props(m map[string]any) property.Map {
	values := make(map[string]property.Value, len(m))
	for k, v := range m {
		values[k] = value(v)
	}
	return property.NewMap(values)
}

func value(v any) property.Value {
	switch v := v.(type) {
	case property.Value:
		return v
	case string:
		return property.New(v)
	case bool:
		return property.New(v)
	case int:
		return property.New(float64(v))
	case float64:
		return property.New(v)
	case []any:
		elems := make([]property.Value, len(v))
		for i, e := range v {
			elems[i] = value(e)
		}
		return property.New(elems)
	case map[string]any:
		return property.New(Props(v))
	default:
		panic(fmt.Sprintf("providertest.Props: unsupported value %#v", v))
	}
}
