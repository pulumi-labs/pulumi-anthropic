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

package agents_test

import (
	"net/http"
	"reflect"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const agentToken = "anthropic:agents:Agent"

var agentURN = resource.NewURN("test", "test", "", agentToken, "agent")

// agentServerBehavior mimics the real API: agents start at version 1, a configuration
// change bumps the version, a stale version is rejected, and unset model settings are
// filled in with server defaults.
func agentServerBehavior(api *fakeapi.Server) {
	api.SetHooks("agents", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) {
			obj["version"] = 1.0
			model := obj["model"].(map[string]any)
			if model["effort"] == nil {
				model["effort"] = map[string]any{"type": "high"}
			}
		},
		OnUpdate: func(before, after, body fakeapi.Object) error {
			if v, ok := body["version"]; ok && v != before["version"] {
				return &fakeapi.Error{Status: http.StatusConflict, Message: "version mismatch"}
			}
			delete(after, "version")
			after["version"] = before["version"]
			for k := range body {
				if k != "version" && !reflect.DeepEqual(before[k], after[k]) {
					after["version"] = before["version"].(float64) + 1
					break
				}
			}
			return nil
		},
	})
}

func baseAgent() map[string]any {
	return map[string]any{
		"name":   "support",
		"model":  map[string]any{"id": "claude-opus-5-5"},
		"system": "You help customers.",
		"mcpServers": []any{
			map[string]any{"name": "docs", "url": "https://mcp.example.com"},
		},
		"tools": []any{
			map[string]any{"type": "agent_toolset_20260401",
				"defaultConfig": map[string]any{"permissionPolicy": "always_ask"}},
			map[string]any{"type": "mcp_toolset", "mcpServerName": "docs"},
		},
		"metadata": map[string]any{"team": "support", "tier": "1"},
	}
}

func TestAgentLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	agentServerBehavior(api)

	updated := baseAgent()
	updated["system"] = "You help customers, politely."
	updated["metadata"] = map[string]any{"team": "support"}

	integration.LifeCycleTest{
		Resource: agentToken,
		Create: integration.Operation{
			Inputs: providertest.Props(baseAgent()),
			Hook: func(_, output property.Map) {
				assert.Equal(t, 1.0, output.Get("version").AsNumber())
				// The server's default effort must not leak into the declared model.
				_, hasEffort := output.Get("model").AsMap().GetOk("effort")
				assert.False(t, hasEffort)

				req := api.LastRequest(t, http.MethodPost, "/v1/agents")
				assert.Equal(t, "managed-agents-2026-04-01", req.Header.Get("anthropic-beta"))
				assert.Equal(t, "sk-ant-api-test", req.Header.Get("X-Api-Key"))
				assert.Equal(t, "url", req.Body["mcp_servers"].([]any)[0].(map[string]any)["type"])
				assert.Equal(t, map[string]any{"permission_policy": map[string]any{"type": "always_ask"}},
					req.Body["tools"].([]any)[0].(map[string]any)["default_config"])
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(updated),
			Hook: func(_, output property.Map) {
				assert.Equal(t, 2.0, output.Get("version").AsNumber())
				reqs := api.Requests()
				req := reqs[len(reqs)-1]
				// Only changed fields are sent, the dropped metadata key is removed with a
				// null, and the prior version is sent as an optimistic lock.
				assert.Equal(t, fakeapi.Object{
					"system":   "You help customers, politely.",
					"metadata": map[string]any{"team": "support", "tier": nil},
					"version":  1.0,
				}, req.Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Regexp(t, `^/v1/agents/agent_\d+/archive$`, last.Path, "deleting an agent archives it")
}

func TestAgentRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	agentServerBehavior(api)

	created, inputs := providertest.Create(t, server, agentURN, providertest.Props(baseAgent()))
	path := "/v1/agents/" + created.ID

	// With no changes outside of Pulumi, a refresh reports no drift, even though the
	// server filled in a default effort.
	read, err := server.Read(p.ReadRequest{ID: created.ID, Urn: agentURN, Properties: created.Properties,
		Inputs: inputs})
	require.NoError(t, err)
	assert.Equal(t, created.Properties.Get("model"), read.Inputs.Get("model"))
	assert.Equal(t, created.Properties.Get("tools"), read.Inputs.Get("tools"))

	// A change made outside of Pulumi shows up as drift.
	api.Mutate(path, func(obj fakeapi.Object) { obj["system"] = "Edited in the console." })
	read, err = server.Read(p.ReadRequest{ID: created.ID, Urn: agentURN, Properties: created.Properties,
		Inputs: inputs})
	require.NoError(t, err)
	assert.Equal(t, "Edited in the console.", read.Inputs.Get("system").AsString())

	// An archived agent reads as deleted, so Pulumi recreates it.
	api.Mutate(path, func(obj fakeapi.Object) { obj["archived_at"] = "2026-09-25T00:00:00Z" })
	read, err = server.Read(p.ReadRequest{ID: created.ID, Urn: agentURN, Properties: created.Properties})
	require.NoError(t, err)
	assert.Empty(t, read.ID)
}

func TestAgentImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	agentServerBehavior(api)

	created, _ := providertest.Create(t, server, agentURN, providertest.Props(baseAgent()))
	read, err := server.Read(p.ReadRequest{ID: created.ID, Urn: agentURN})
	require.NoError(t, err)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, "support", read.Inputs.Get("name").AsString())
	assert.Equal(t, "high", read.Inputs.Get("model").AsMap().Get("effort").AsString(),
		"an import adopts the server's values wholesale")
}

func TestAgentUpdateConflict(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	agentServerBehavior(api)

	created, _ := providertest.Create(t, server, agentURN, providertest.Props(baseAgent()))
	api.Mutate("/v1/agents/"+created.ID, func(obj fakeapi.Object) { obj["version"] = 2.0 })

	updated := baseAgent()
	updated["system"] = "Changed."
	_, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: agentURN,
		State: created.Properties, Inputs: providertest.Props(updated)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pulumi refresh")
}

func TestAgentCheck(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)

	inputs := baseAgent()
	inputs["tools"] = []any{map[string]any{"type": "mcp_toolset", "mcpServerName": "wiki"}}
	resp, err := server.Check(p.CheckRequest{Urn: agentURN, Inputs: providertest.Props(inputs)})
	require.NoError(t, err)
	assert.ElementsMatch(t, []p.CheckFailure{
		{Property: "tools[0].mcpServerName", Reason: `no MCP server named "wiki" is declared in mcpServers`},
		{Property: "mcpServers[0]", Reason: `MCP server "docs" must be referenced by an mcp_toolset tool`},
	}, resp.Failures)
}

func TestAgentMissingCredential(t *testing.T) {
	t.Parallel()
	server, _ := providertest.NewWithConfig(t, map[string]property.Value{
		"adminKey": property.New("sk-ant-admin-test"),
	})
	_, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: providertest.Props(baseAgent())})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic:apiKey")
}
