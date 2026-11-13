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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/blang/semver"
	"github.com/pulumi-labs/pulumi-anthropic/provider/agents"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const agentToken = "anthropic:agents:Agent"

var agentURN = resource.NewURN("test", "test", "", agentToken, "agent")

// agentServerBehavior is a synthetic SDK-shaped service. It exercises mappings,
// not platform truth. Defaults and version behavior are declared test assumptions.
func agentServerBehavior(api *fakeapi.Server) {
	api.SetHooks("agents", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) {
			agentCompleteFake(obj)
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
			agentCompleteFake(after)
			return nil
		},
	})
}

func agentCompleteFake(obj fakeapi.Object) {
	defaults := map[string]any{"description": "", "system": "", "tools": []any{}, "mcp_servers": []any{}, "skills": []any{}, "metadata": map[string]any{}, "multiagent": nil, "execution_identity": map[string]any{"type": "service_account"}}
	for k, v := range defaults {
		if _, ok := obj[k]; !ok {
			obj[k] = v
		}
	}
	if skills, ok := obj["skills"].([]any); ok {
		for _, skill := range skills {
			if s, ok := skill.(map[string]any); ok {
				if _, set := s["version"]; !set {
					s["version"] = "latest"
				}
			}
		}
	}
	if tools, ok := obj["tools"].([]any); ok {
		for _, tool := range tools {
			if t, ok := tool.(map[string]any); ok {
				if typ := t["type"]; typ == "agent_toolset_20260401" || typ == "mcp_toolset" {
					if _, set := t["default_config"]; !set {
						t["default_config"] = map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "auto"}}
					}
					if _, set := t["configs"]; !set {
						t["configs"] = []any{}
					}
				}
				if d, ok := t["default_config"].(map[string]any); ok {
					if _, set := d["enabled"]; !set {
						d["enabled"] = true
					}
					if _, set := d["permission_policy"]; !set {
						d["permission_policy"] = map[string]any{"type": "auto"}
					}
				}
				if configs, ok := t["configs"].([]any); ok {
					for _, config := range configs {
						if c, ok := config.(map[string]any); ok {
							if t["type"] == "agent_toolset_20260401" {
								c["type"] = c["name"]
								if c["name"] == "web_fetch" {
									if _, set := c["url_sources"]; !set {
										c["url_sources"] = nil
									}
								}
							}
							if _, set := c["enabled"]; !set {
								c["enabled"] = true
							}
							if _, set := c["permission_policy"]; !set {
								c["permission_policy"] = map[string]any{"type": "auto"}
							}
						}
					}
				}
			}
		}
	}
	if model, ok := obj["model"].(map[string]any); ok {
		if e, ok := model["effort"].(string); ok {
			model["effort"] = map[string]any{"type": e}
		}
	}
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
				assert.Equal(t, "sk-ant-api-test", req.Header.Get("x-api-key"))
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
				// Send all desired mutable fields, not a second changed-field diff.
				assert.Equal(t, fakeapi.Object{
					"name": "support", "model": map[string]any{"id": "claude-opus-5-5"},
					"system":   "You help customers, politely.",
					"metadata": map[string]any{"team": "support", "tier": nil}, "version": 1.0,
					"mcp_servers": []any{map[string]any{"name": "docs", "url": "https://mcp.example.com", "type": "url"}},
					"tools":       []any{map[string]any{"type": "agent_toolset_20260401", "default_config": map[string]any{"permission_policy": map[string]any{"type": "always_ask"}}}, map[string]any{"type": "mcp_toolset", "mcp_server_name": "docs"}},
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
	out, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: agentURN,
		State: created.Properties, Inputs: providertest.Props(updated)})
	require.Error(t, err)
	require.Nil(t, out.PartialState)
	require.Zero(t, out.Properties.Len())
	assert.Contains(t, err.Error(), "pulumi refresh")
	for _, request := range api.Requests()[1:] {
		require.Equal(t, http.MethodPost, request.Method, "a version conflict must not trigger an internal refresh")
		require.Equal(t, "/v1/agents/"+created.ID, request.Path)
		require.Equal(t, 1.0, request.Body["version"], "SDK retries must retain the engine-supplied version lock")
	}
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

func TestAgentRejectsMalformedNestedTools(t *testing.T) {
	location := &agents.SearchLocation{City: agents.AgentPtrForTest("requested"), Country: agents.AgentPtrForTest("US")}
	sources := &agents.FetchURLSources{ClientToolResults: &agents.FetchToolFilter{Type: "only", Tools: &[]string{"request-a", "request-b"}}, UserInput: agents.AgentPtrForTest("none")}
	prior := []agents.Tool{{Type: agents.AgentToolset, Configs: &[]agents.ToolConfig{
		{Name: "web_search", Enabled: agents.AgentPtrForTest(false), UserLocation: location},
		{Name: "web_fetch", Enabled: agents.AgentPtrForTest(false), URLSources: sources, AllowedDomains: &[]string{"request.invalid"}},
	}}}
	var r sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"tools":[{"type":"agent_toolset_20260401","configs":[{"type":"web_search","name":"web_search","enabled":true,"permission_policy":{"type":"auto"},"user_location":{"type":"approximate","city":"observed","country":42}},{"type":"web_fetch","name":"web_fetch","enabled":true,"permission_policy":{"type":"auto"},"allowed_domains":["observed.invalid",42],"url_sources":{"client_tool_results":{"type":"except","tools":[{"type":"tool_reference","name":42}]},"server_tool_results":{"type":"all"},"user_input":{"type":"all"}}}]}]}`), &r))
	tools, err := agents.AgentStrictToolsForTest(r.Tools, prior, false)
	require.Error(t, err)
	require.Nil(t, tools)
	require.Equal(t, "requested", *location.City)
	require.Equal(t, []string{"request-a", "request-b"}, *sources.ClientToolResults.Tools)
	require.Equal(t, []string{"request.invalid"}, *(*prior[0].Configs)[1].AllowedDomains)
	require.False(t, *(*prior[0].Configs)[0].Enabled)
}

func TestAgentRejectsMalformedModernDelegation(t *testing.T) {
	prior := &agents.Multiagent{Type: agents.AgentPtrForTest("multiagent_20261001"), Advisor: &agents.AgentAdvisor{Type: "enabled", Model: agents.AgentPtrForTest("requested-advisor")}, Subagents: &agents.AgentDelegation{Type: "enabled", InlineAgents: agents.AgentPtrForTest("disabled"), PredefinedAgents: &[]agents.RosterEntry{{Type: "agent", ID: agents.AgentPtrForTest("agent_requested"), Version: agents.AgentPtrForTest(1)}}}}
	var r sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"multiagent":{"type":"multiagent_20261001","advisor":{"type":"enabled","model":"observed-advisor"},"subagents":{"type":"enabled","inline_agents":{"type":"invalid"},"predefined_agents":[{"type":"agent","id":"agent_observed","version":3}]},"workflows":{"type":"disabled"}}}`), &r))
	multi, err := agents.AgentObservedMultiForTest(r.Multiagent, prior, false, "agent_owner")
	require.Error(t, err)
	require.Nil(t, multi)
	require.Equal(t, "requested-advisor", *prior.Advisor.Model)
	require.Equal(t, "agent_requested", *(*prior.Subagents.PredefinedAgents)[0].ID)
	require.Equal(t, 1, *(*prior.Subagents.PredefinedAgents)[0].Version)
}

func agentCoordinatorInputs(mode *property.Value) property.Map {
	m := providertest.Props(map[string]any{"agents": []any{map[string]any{"type": "self"}}})
	if mode != nil {
		m = m.Set("type", *mode)
	}
	return agentContractInputs().Set("multiagent", property.New(m))
}

func TestAgentCoordinatorDefaultNormalization(t *testing.T) {
	s, api := providertest.New(t)
	coordinator := property.New("coordinator")
	implicit, explicit := agentCoordinatorInputs(nil), agentCoordinatorInputs(&coordinator)
	beforeImplicit, beforeExplicit := agentCoordinatorInputs(nil), agentCoordinatorInputs(&coordinator)
	checked := make([]property.Map, 2)
	for i, inputs := range []property.Map{implicit, explicit} {
		resp, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, resp.Failures)
		checked[i] = resp.Inputs
		again, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: resp.Inputs})
		require.NoError(t, err)
		require.Empty(t, again.Failures)
		require.Equal(t, resp.Inputs, again.Inputs, "normalization must be idempotent")
	}
	require.Equal(t, beforeImplicit, implicit, "Check must not mutate the omitted-mode bag")
	require.Equal(t, beforeExplicit, explicit, "Check must not mutate the explicit-mode bag")
	require.Equal(t, explicit, checked[0], "the canonical mode is explicit coordinator")
	require.Equal(t, checked[0], checked[1], "semantically equal declarations must check to equal bags")
	for _, pair := range [][2]int{{0, 1}, {1, 0}} {
		diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: "agent_default", State: checked[pair[0]], OldInputs: checked[pair[0]], Inputs: checked[pair[1]]})
		require.NoError(t, err)
		require.False(t, diff.HasChanges)
		require.Empty(t, diff.DetailedDiff)
	}
	modernInputs := agentContractInputs().Set("multiagent", property.New(providertest.Props(map[string]any{"type": "multiagent_20261001"})))
	modern, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: modernInputs})
	require.NoError(t, err)
	require.Empty(t, modern.Failures)
	require.Equal(t, modernInputs, modern.Inputs, "modern mode must not be collapsed to coordinator")
	for _, pair := range [][2]property.Map{{checked[0], modern.Inputs}, {modern.Inputs, checked[0]}} {
		diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: "agent_default", State: pair[0], OldInputs: pair[0], Inputs: pair[1]})
		require.NoError(t, err)
		require.True(t, diff.HasChanges)
		require.False(t, diff.DeleteBeforeReplace)
	}
	require.Empty(t, api.Requests())
}

func TestAgentCoordinatorDefaultCreateReadConvergence(t *testing.T) {
	s, api := providertest.New(t)
	handler := func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"id":"agent_default","name":"contract","model":{"id":"claude-opus-5-5"},"system":"original","description":"","metadata":{},"tools":[],"mcp_servers":[],"skills":[],"multiagent":{"type":"coordinator","agents":[{"type":"agent","id":"agent_default","version":1}]},"execution_identity":{"type":"service_account"},"version":1,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","archived_at":null}`))
		require.NoError(t, err)
	}
	api.Handle("POST /v1/agents", handler)
	api.Handle("GET /v1/agents/agent_default", handler)
	checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: agentCoordinatorInputs(nil)})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, "coordinator", created.Properties.Get("multiagent").AsMap().Get("type").AsString())
	require.Equal(t, map[string]any{"type": "coordinator", "agents": []any{map[string]any{"type": "self"}}}, api.LastRequest(t, http.MethodPost, "/v1/agents").Body["multiagent"])
	read, err := s.Read(p.ReadRequest{Urn: agentURN, ID: created.ID, Inputs: checked.Inputs, Properties: created.Properties})
	require.NoError(t, err)
	coordinator := property.New("coordinator")
	for _, inputs := range []property.Map{agentCoordinatorInputs(nil), agentCoordinatorInputs(&coordinator)} {
		other, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, other.Failures)
		for _, state := range []property.Map{created.Properties, read.Properties} {
			diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: created.ID, State: state, OldInputs: checked.Inputs, Inputs: other.Inputs})
			require.NoError(t, err)
			require.False(t, diff.HasChanges)
		}
	}
	require.Len(t, api.Requests(), 2, "Check/Diff add no HTTP to the SDK Create/Get lifecycle")
}

func TestAgentCoordinatorNormalizationMetadata(t *testing.T) {
	leafDep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::mode")
	parentDep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::orchestration")
	for _, secret := range []bool{false, true} {
		for _, kind := range []string{"omitted", "null", "coordinator", "modern", "unknown mode", "unknown object", "null object"} {
			t.Run(kind+map[bool]string{false: "/public", true: "/secret"}[secret], func(t *testing.T) {
				s, api := providertest.New(t)
				var mode property.Value
				switch kind {
				case "null":
					mode = property.Value{}
				case "coordinator":
					mode = property.New("coordinator")
				case "modern":
					mode = property.New("multiagent_20261001")
				case "unknown mode":
					mode = property.New(property.Computed)
				}
				mode = mode.WithSecret(secret).WithDependencies([]resourceurn.URN{leafDep})
				var inputs property.Map
				if kind == "omitted" {
					inputs = agentCoordinatorInputs(nil)
				} else {
					inputs = agentCoordinatorInputs(&mode)
				}
				// Modern mode cannot contain the legacy agents roster.
				if kind == "modern" {
					inputs = inputs.Set("multiagent", property.New(providertest.Props(map[string]any{"type": mode})))
				}
				multi := inputs.Get("multiagent")
				if kind == "unknown object" {
					multi = property.New(property.Computed)
				}
				if kind == "null object" {
					multi = property.Value{}
				}
				multi = multi.WithSecret(secret).WithDependencies([]resourceurn.URN{parentDep})
				inputs = inputs.Set("multiagent", multi)
				expected := inputs
				if kind == "omitted" || kind == "null" {
					defaultMode := property.New("coordinator")
					if kind == "null" {
						defaultMode = defaultMode.WithSecret(secret).WithDependencies([]resourceurn.URN{leafDep})
					}
					expected = expected.Set("multiagent", property.WithGoValue(multi, multi.AsMap().Set("type", defaultMode)))
				}
				resp, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, resp.Failures)
				require.Equal(t, multi, inputs.Get("multiagent"), "normalization must not mutate the original object")
				require.Equal(t, expected, resp.Inputs, "normalization must preserve secrecy, dependencies and unresolved values on their original paths")
				again, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: resp.Inputs})
				require.NoError(t, err)
				require.Empty(t, again.Failures)
				require.Equal(t, resp.Inputs, again.Inputs)
				diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: "agent_metadata", State: resp.Inputs, OldInputs: resp.Inputs, Inputs: again.Inputs})
				require.NoError(t, err)
				require.False(t, diff.HasChanges)
				require.Empty(t, api.Requests())
			})
		}
	}
}

// These are Pulumi contract regressions, not observations of platform behavior.
func TestAgentCheckUnknownMCPRelationships(t *testing.T) {
	unknown := property.New(property.Computed)
	server := func(name property.Value) property.Value {
		return property.New(providertest.Props(map[string]any{"name": name, "url": "https://example.invalid/mcp"}))
	}
	tool := func(typ, name property.Value) property.Value {
		return property.New(providertest.Props(map[string]any{"type": typ, "mcpServerName": name}))
	}
	array := func(v ...property.Value) property.Value { return property.New(v) }
	docs := property.New("docs")
	mcp := property.New("mcp_toolset")
	for _, tc := range []struct {
		name           string
		servers, tools property.Value
		failures       []string
	}{
		{"server name", array(server(unknown)), array(tool(mcp, docs)), nil},
		{"tool reference", array(server(docs)), array(tool(mcp, unknown)), nil},
		{"server object", array(unknown), array(tool(mcp, docs)), nil},
		{"tool object", array(server(docs)), array(unknown), nil},
		{"server collection", unknown, array(tool(mcp, docs)), nil},
		{"tool collection", array(server(docs)), unknown, nil},
		{"tool type", array(server(docs)), array(tool(unknown, docs)), nil},
		{"known unused beside unknown server", array(server(docs), server(unknown)), array(), []string{"mcpServers[0]"}},
		{"known missing beside unknown tool", array(server(docs)), array(tool(mcp, property.New("missing")), unknown), []string{"tools[0].mcpServerName"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			inputs := providertest.Props(map[string]any{"name": "agent", "model": map[string]any{"id": "claude-opus-5-5"}, "mcpServers": tc.servers, "tools": tc.tools})
			resp, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
			require.NoError(t, err)
			paths := make([]string, 0, len(resp.Failures))
			for _, f := range resp.Failures {
				paths = append(paths, f.Property)
			}
			require.ElementsMatch(t, tc.failures, paths)
			require.Empty(t, api.Requests())
		})
	}
}

func TestAgentCheckMetadataIdentity(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			t.Run(map[bool]string{false: "unknown", true: "known"}[known]+map[bool]string{false: "public", true: "secret"}[secret], func(t *testing.T) {
				s, api := providertest.New(t)
				name := property.New(property.Computed)
				if known {
					name = property.New("docs")
				}
				name = name.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::name"})
				inputs := providertest.Props(map[string]any{
					"name": "agent", "model": map[string]any{"id": "claude-opus-5-5"},
					"mcpServers": []any{map[string]any{"name": name, "url": property.New(property.Computed)}},
					"tools":      []any{map[string]any{"type": "mcp_toolset", "mcpServerName": name}},
				})
				before := providertest.Props(map[string]any{
					"name": "agent", "model": map[string]any{"id": "claude-opus-5-5"},
					"mcpServers": []any{map[string]any{"name": name, "url": property.New(property.Computed)}},
					"tools":      []any{map[string]any{"type": "mcp_toolset", "mcpServerName": name}},
				})
				checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				require.Equal(t, before, inputs, "Check must not mutate its input")
				require.Equal(t, inputs, checked.Inputs, "metadata stays attached to each logical value")
				again, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: checked.Inputs})
				require.NoError(t, err)
				require.Empty(t, again.Failures)
				require.Equal(t, checked.Inputs, again.Inputs)
				require.Empty(t, api.Requests())
			})
		}
	}
}

func TestAgentCheckVariantValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		fail   bool
	}{
		{"unknown effort", map[string]any{"model": map[string]any{"id": "claude-opus-5-5", "effort": property.New(property.Computed)}}, false},
		{"invalid effort", map[string]any{"model": map[string]any{"id": "claude-opus-5-5", "effort": "invalid"}}, true},
		{"unknown orchestration", map[string]any{"multiagent": map[string]any{"type": property.New(property.Computed)}}, false},
		{"invalid orchestration", map[string]any{"multiagent": map[string]any{"type": "unknown"}}, true},
		{"unknown advisor model", map[string]any{"multiagent": map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "enabled", "model": property.New(property.Computed)}}}, false},
		{"missing advisor model", map[string]any{"multiagent": map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "enabled"}}}, true},
		{"custom required fields", map[string]any{"tools": []any{map[string]any{"type": "custom", "name": "custom"}}}, true},
		{"unknown principal", map[string]any{"executionIdentity": map[string]any{"type": property.New(property.Computed)}}, false},
		{"missing role ARN", map[string]any{"executionIdentity": map[string]any{"type": "aws_role"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			m := map[string]any{"name": "agent", "model": map[string]any{"id": "claude-opus-5-5"}}
			for k, v := range tc.fields {
				m[k] = v
			}
			resp, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: providertest.Props(m)})
			require.NoError(t, err)
			if tc.fail {
				require.NotEmpty(t, resp.Failures)
			} else {
				require.Empty(t, resp.Failures)
			}
			require.Empty(t, api.Requests())
		})
	}
}

func TestAgentCheckUnknownURLDoesNotHideMismatch(t *testing.T) {
	s, api := providertest.New(t)
	inputs := providertest.Props(map[string]any{
		"name": "agent", "model": map[string]any{"id": "claude-opus-5-5"},
		"mcpServers": []any{map[string]any{"name": "docs", "url": property.New(property.Computed)}},
		"tools":      []any{map[string]any{"type": "mcp_toolset", "mcpServerName": "missing"}},
	})
	resp, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
	require.NoError(t, err)
	require.Len(t, resp.Failures, 2)
	require.Empty(t, api.Requests())
}

// Exercise typed decoding through the provider protocol, not just direct Go
// conversions. Synthetic responses establish the mapping, not platform truth.
func TestAgentDelegationCollectionsProtocol(t *testing.T) {
	for _, branch := range []string{"subagents", "workflows"} {
		for _, tc := range []struct {
			name             string
			initial, desired []any
		}{
			{"never-managed", nil, nil},
			{"explicit-empty", []any{}, []any{}},
			{"empty-then-removed", []any{}, nil},
			{"configured-then-removed", []any{map[string]any{"type": "self"}}, nil},
		} {
			t.Run(branch+"/"+tc.name, func(t *testing.T) {
				s, api := providertest.New(t)
				const id = "agent_presence"
				handler := func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
					delegation := func() map[string]any {
						return map[string]any{"type": "enabled", "inline_agents": map[string]any{"type": "enabled"}, "predefined_agents": []any{}}
					}
					observed := fakeapi.Object{"id": id, "name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "version": 1, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil,
						"multiagent": map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "disabled"}, "subagents": delegation(), "workflows": delegation()}}
					agentCompleteFake(observed)
					fakeapi.WriteJSON(w, observed)
				}
				api.Handle("POST /v1/agents", handler)
				api.Handle("POST /v1/agents/"+id, handler)
				check := func(refs []any, name string) property.Map {
					d := map[string]any{"type": "enabled"}
					if refs != nil {
						d["predefinedAgents"] = refs
					}
					in := agentContractInputs().Set("name", property.New(name)).Set("multiagent", property.New(providertest.Props(map[string]any{"type": "multiagent_20261001", branch: d})))
					checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: in})
					require.NoError(t, err)
					require.Empty(t, checked.Failures)
					require.Equal(t, in, checked.Inputs)
					return checked.Inputs
				}
				initial := check(tc.initial, "initial")
				created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: initial})
				require.NoError(t, err)
				require.Len(t, api.Requests(), 1)
				createBody := api.LastRequest(t, http.MethodPost, "/v1/agents").Body["multiagent"].(map[string]any)[branch].(map[string]any)
				_, present := createBody["predefined_agents"]
				require.Equal(t, tc.initial != nil, present)
				if tc.initial != nil && len(tc.initial) == 0 {
					require.Equal(t, []any{}, createBody["predefined_agents"])
				}
				desired := check(tc.desired, "renamed")
				_, err = s.Update(p.UpdateRequest{Urn: agentURN, ID: id, State: created.Properties, OldInputs: initial, Inputs: desired})
				require.NoError(t, err)
				require.Len(t, api.Requests(), 2, "no SDK reads may be added")
				want := map[string]any{"type": "enabled"}
				if tc.desired != nil {
					want["predefined_agents"] = []any{}
				} else if tc.initial != nil {
					want["predefined_agents"] = nil
				}
				require.Equal(t, fakeapi.Object{"name": "renamed", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 1.0,
					"multiagent": map[string]any{"type": "multiagent_20261001", branch: want}}, api.LastRequest(t, http.MethodPost, "/v1/agents/"+id).Body)
			})
		}
	}
}

func TestAgentOptionalCollectionsProtocol(t *testing.T) {
	empty := map[string]any{"tools": []any{}, "skills": []any{}, "mcpServers": []any{}}
	configured := map[string]any{
		"tools":      []any{map[string]any{"type": "mcp_toolset", "mcpServerName": "remote"}},
		"skills":     []any{map[string]any{"type": "anthropic", "skillId": "xlsx"}},
		"mcpServers": []any{map[string]any{"name": "remote", "url": "https://example.invalid/mcp"}},
	}
	for _, tc := range []struct {
		name             string
		initial, desired map[string]any
		clear            bool
	}{
		{"never-managed", nil, nil, false},
		{"explicit-empty", empty, empty, true},
		{"configured-then-removed", configured, nil, true},
		{"configured-then-empty", configured, empty, true},
		{"empty-then-removed", empty, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			agentServerBehavior(api)
			inputs := func(collections map[string]any, name string) property.Map {
				m := agentContractInputs().Set("name", property.New(name))
				for k, v := range collections {
					m = m.Set(k, providertest.Props(map[string]any{"v": v}).Get("v"))
				}
				return m
			}
			check := func(in property.Map) property.Map {
				checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: in})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				require.Equal(t, in, checked.Inputs)
				return checked.Inputs
			}
			initial := check(inputs(tc.initial, "initial"))
			preview, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: initial, DryRun: true})
			require.NoError(t, err)
			for _, key := range []string{"tools", "skills", "mcpServers"} {
				require.Equal(t, initial.Get(key), preview.Properties.Get(key), "Create preview: "+key)
			}
			require.Empty(t, api.Requests(), "Check and preview must not query the SDK")
			created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: initial})
			require.NoError(t, err)
			require.Len(t, api.Requests(), 1, "Create must not read back")
			body := api.LastRequest(t, http.MethodPost, "/v1/agents").Body
			for input, wire := range map[string]string{"tools": "tools", "skills": "skills", "mcpServers": "mcp_servers"} {
				_, sent := body[wire]
				require.Equal(t, tc.initial != nil, sent, input)
				if tc.initial == nil {
					require.True(t, created.Properties.Get(input).IsNull(), input)
				} else if len(tc.initial[input].([]any)) == 0 {
					require.Equal(t, []any{}, body[wire], input)
					require.True(t, created.Properties.Get(input).IsArray(), input)
					require.Zero(t, created.Properties.Get(input).AsArray().Len(), input)
				}
			}
			desired := check(inputs(tc.desired, "renamed"))
			updatePreview, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: initial, Inputs: desired, DryRun: true})
			require.NoError(t, err)
			for _, key := range []string{"tools", "skills", "mcpServers"} {
				require.Equal(t, desired.Get(key), updatePreview.Properties.Get(key), "Update preview: "+key)
			}
			require.Len(t, api.Requests(), 1, "Update preview must not query the SDK")
			updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: initial, Inputs: desired})
			require.NoError(t, err)
			require.Len(t, api.Requests(), 2, "Update must not internally refresh")
			want := fakeapi.Object{"name": "renamed", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 1.0}
			if tc.clear {
				want["tools"], want["skills"], want["mcp_servers"] = []any{}, []any{}, []any{}
			}
			require.Equal(t, want, api.LastRequest(t, http.MethodPost, "/v1/agents/"+created.ID).Body)
			for _, key := range []string{"tools", "skills", "mcpServers"} {
				v := updated.Properties.Get(key)
				if tc.desired == nil {
					require.True(t, v.IsNull(), key)
				} else {
					require.True(t, v.IsArray(), key)
					require.Zero(t, v.AsArray().Len(), key)
				}
			}
		})
	}
}

func agentContractInputs() property.Map {
	return providertest.Props(map[string]any{"name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original"})
}

func TestAgentUnknownPreview(t *testing.T) {
	s, api := providertest.New(t)
	agentServerBehavior(api)
	inputs := agentContractInputs().Set("system", property.New(property.Computed))
	checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	preview, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("system").IsComputed())
	require.True(t, preview.Properties.Get("version").IsComputed())
	require.Empty(t, api.Requests())
	created, old := providertest.Create(t, s, agentURN, agentContractInputs())
	n := len(api.Requests())
	diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	update, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, update.Properties.Get("system").IsComputed())
	require.Len(t, api.Requests(), n)
}

// Imported empty optional collections are configured values, so all are sent
// when Update is invoked.
func TestAgentImportAndFullDesiredUpdate(t *testing.T) {
	s, api := providertest.New(t)
	const id = "agent_contract"
	const path = "/v1/agents/" + id
	obj := fakeapi.Object{"id": id, "name": "contract", "model": map[string]any{"id": "claude-opus-5-5", "speed": "standard"}, "system": "original", "version": 7.0, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	agentCompleteFake(obj)
	api.Put(path, obj)
	imported, err := s.Read(p.ReadRequest{Urn: agentURN, ID: id})
	require.NoError(t, err)
	require.Equal(t, id, imported.ID)
	require.Equal(t, "standard", imported.Inputs.Get("model").AsMap().Get("speed").AsString())
	checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	diff, err := s.Diff(p.DiffRequest{Urn: agentURN, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	desired := checked.Inputs.Set("name", property.New("renamed")).Delete("system")
	diff, err = s.Diff(p.DiffRequest{Urn: agentURN, ID: id, State: imported.Properties, Inputs: desired})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.False(t, diff.DeleteBeforeReplace)
	require.Equal(t, p.Update, diff.DetailedDiff["name"].Kind)
	require.Equal(t, p.Delete, diff.DetailedDiff["system"].Kind)
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: id, State: imported.Properties, Inputs: desired})
	require.NoError(t, err)
	require.Len(t, api.Requests(), n+1, "Update must not internally refresh")
	require.Equal(t, fakeapi.Object{"name": "renamed", "model": map[string]any{"id": "claude-opus-5-5", "speed": "standard"}, "system": "", "description": "", "tools": []any{}, "mcp_servers": []any{}, "skills": []any{}, "metadata": map[string]any{}, "execution_identity": map[string]any{"type": "service_account"}, "version": 7.0}, api.LastRequest(t, http.MethodPost, path).Body)
	refreshed, err := s.Read(p.ReadRequest{Urn: agentURN, ID: id, Properties: updated.Properties, Inputs: desired})
	require.NoError(t, err)
	again, err := s.Read(p.ReadRequest{Urn: agentURN, ID: id, Properties: updated.Properties, Inputs: desired})
	require.NoError(t, err)
	require.Equal(t, refreshed, again)
}

func TestAgentUnusableCreateAndLegacyRefresh(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent_created_remotely","name":"observed","model":{"id":"claude-opus-5-5"},"version":"malformed","created_at":"2026-01-01T00:00:00Z"}`))
	})
	inputs := agentContractInputs()
	result, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
	require.Error(t, err)
	require.Empty(t, result.ID)
	require.Nil(t, result.PartialState)
	require.Zero(t, result.Properties.Len())
	// Older providers may have saved incomplete state. Refresh explicitly before
	// Update so only observed server facts supply the version lock.
	api.Handle("GET /v1/agents/agent_created_remotely", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		body := fakeapi.Object{"id": "agent_created_remotely", "name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 1, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
		agentCompleteFake(body)
		fakeapi.WriteJSON(w, body)
	})
	refreshed, err := s.Read(p.ReadRequest{Urn: agentURN, ID: "agent_created_remotely", Inputs: inputs, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, 1.0, refreshed.Properties.Get("version").AsNumber())
	api.Handle("POST /v1/agents/agent_created_remotely", func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 1.0}, body)
		body["id"] = "agent_created_remotely"
		body["version"] = 2
		body["created_at"] = "2026-01-01T00:00:00Z"
		body["updated_at"] = "2026-01-02T00:00:00Z"
		body["archived_at"] = nil
		agentCompleteFake(body)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(body))
	})
	updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: refreshed.ID, State: refreshed.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Equal(t, 2.0, updated.Properties.Get("version").AsNumber())
	require.Len(t, api.Requests(), 4, "create compensation adds an archive, not a recovery read")
	require.Equal(t, "/v1/agents/agent_created_remotely/archive", api.Requests()[1].Path)
	require.Equal(t, http.MethodGet, api.Requests()[2].Method)
}

func TestAgentCreateConservativeRetry(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"synthetic ambiguous allocation"}}`))
	})
	result, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: agentContractInputs()})
	require.Error(t, err)
	require.Empty(t, result.ID)
	require.Nil(t, result.PartialState)
	require.Zero(t, result.Properties.Len())
	require.Len(t, api.Requests(), 1, "generated New must not retry an ambiguous server failure")
}

func TestAgentForeignUpdateObservation(t *testing.T) {
	s, api := providertest.New(t)
	agentServerBehavior(api)
	created, _ := providertest.Create(t, s, agentURN, agentContractInputs())
	api.Handle("POST /v1/agents/"+created.ID, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent_foreign","name":"foreign name","model":{"id":"foreign model"},"version":500,"created_at":"2040-01-01T00:00:00Z"}`))
	})
	desired := agentContractInputs().Set("system", property.New("accepted desired"))
	updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, Inputs: desired})
	require.Error(t, err)
	require.Nil(t, updated.PartialState)
	require.Zero(t, updated.Properties.Len(), "foreign observations must not produce saved state")
	require.Len(t, api.Requests(), 2)
}

func TestAgentReadFailureIsNotAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		absent     bool
	}{
		{"not found", `{"type":"error","error":{"type":"not_found_error","message":"synthetic"}}`, 404, true},
		{"forbidden", `{"type":"error","error":{"type":"permission_error","message":"synthetic"}}`, 403, false},
		{"foreign", `{"id":"agent_foreign"}`, 200, false},
		{"incomplete", `{"id":"agent_requested","name":"incomplete"}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/agents/agent_requested", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			read, err := s.Read(p.ReadRequest{Urn: agentURN, ID: "agent_requested"})
			if tc.absent {
				require.NoError(t, err)
				require.Empty(t, read.ID)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestAgentNumericResponseIDIsNotAnIdentity(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42}`))
	})
	created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: agentContractInputs()})
	require.Error(t, err)
	require.Empty(t, created.ID, "SDK numeric-to-string coercion must not invent a string identity")
	require.Nil(t, created.PartialState)
	require.Len(t, api.Requests(), 1)
}

func TestAgentLegacyIncompleteStateRefreshAndArchive(t *testing.T) {
	s, api := providertest.New(t)
	body := fakeapi.Object{"id": "agent_partial", "name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 2, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	agentCompleteFake(body)
	api.Put("/v1/agents/agent_partial", body)
	refreshed, err := s.Read(p.ReadRequest{Urn: agentURN, ID: "agent_partial", Properties: agentContractInputs()})
	require.NoError(t, err)
	require.Equal(t, "agent_partial", refreshed.ID)
	api.Handle("POST /v1/agents/agent_partial/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent_partial"}`))
	})
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: agentURN, ID: refreshed.ID, Properties: refreshed.Properties}))
	require.Len(t, api.Requests(), 2)
	require.Equal(t, "/v1/agents/agent_partial/archive", api.Requests()[1].Path)
}

func TestAgentNullableDescriptionAndSystem(t *testing.T) {
	server, api := providertest.New(t)
	inputs, row := agentPrimitiveFixture(false)
	inputs = inputs.Delete("description").Delete("system")
	row["description"], row["system"] = nil, nil
	handler := func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, row) }
	api.Handle("POST /v1/agents", handler)
	api.Handle("GET /v1/agents/agent_primitive", handler)
	api.Handle("POST /v1/agents/agent_primitive", handler)

	created, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, "agent_primitive", created.ID)
	require.True(t, created.Properties.Get("description").IsNull())
	require.True(t, created.Properties.Get("system").IsNull())
	imported, err := server.Read(p.ReadRequest{Urn: agentURN, ID: created.ID})
	require.NoError(t, err)
	require.True(t, imported.Properties.Get("description").IsNull())
	require.True(t, imported.Properties.Get("system").IsNull())
	require.True(t, imported.Inputs.Get("description").IsNull())
	require.True(t, imported.Inputs.Get("system").IsNull())
	updated, err := server.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("description").IsNull())
	require.True(t, updated.Properties.Get("system").IsNull())
	require.Len(t, api.Requests(), 3, "nullable observations need no extra reads")
}

func TestAgentCreateConversionFailureCompensation(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "archived"
		if cleanupFails {
			name = "archive-failed"
		}
		t.Run(name, func(t *testing.T) {
			server, api := providertest.New(t)
			inputs, row := agentPrimitiveFixture(false)
			row["version"] = "malformed"
			api.Handle("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, row) })
			archived := false
			api.Handle("POST /v1/agents/agent_primitive/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				if cleanupFails {
					fakeapi.WriteError(w, http.StatusForbidden, "archive unavailable")
					return
				}
				archived = true
				fakeapi.WriteJSON(w, map[string]any{"id": "agent_primitive", "type": "agent"})
			})
			result, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
			require.Error(t, err)
			require.Empty(t, result.ID)
			require.Nil(t, result.PartialState)
			require.Equal(t, !cleanupFails, archived)
			require.Len(t, api.Requests(), 2)
			if cleanupFails {
				require.ErrorContains(t, err, "could not archive newly created agent agent_primitive")
			}
		})
	}
}

// This is a request-state isolation test, not an API concurrency guarantee.
// Overlap two creates through the same registered wrapper, then run Check/Diff
// and another Create without the earlier explicitly empty, annotated collection.
func TestAgentOperationStateIsInvocationLocal(t *testing.T) {
	arrived := make(chan string, 2)
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, _ := body["name"].(string)
		if name != "later" {
			arrived <- name
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		observed := map[string]any{
			"id": "agent_" + name, "name": name,
			"model":   map[string]any{"id": "claude-opus-5-5"},
			"version": 1, "created_at": "2026-01-01T00:00:00Z",
			"updated_at": "2026-01-01T00:00:00Z", "archived_at": nil,
		}
		agentCompleteFake(observed)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(observed)
	}))
	t.Cleanup(api.Close)
	provider, err := infer.NewProviderBuilder().WithResources(agents.NewAgent()).
		WithConfig(infer.Config(&config.Config{})).Build()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), "anthropic", semver.MustParse("0.1.0"), integration.WithProvider(provider))
	require.NoError(t, err)
	require.NoError(t, server.Configure(p.ConfigureRequest{Args: property.NewMap(map[string]property.Value{
		"apiKey": property.New("synthetic-operation-key"), "baseUrl": property.New(api.URL),
	})}))
	makeInputs := func(name string) property.Map {
		return property.NewMap(map[string]property.Value{
			"name":  property.New(name),
			"model": property.New(property.NewMap(map[string]property.Value{"id": property.New("claude-opus-5-5")})),
		})
	}
	type result struct {
		name     string
		inputs   property.Map
		response p.CreateResponse
		err      error
	}
	results := make(chan result, 2)
	expected := map[string]property.Value{}
	for _, name := range []string{"first", "second"} {
		dep := resourceurn.URN(fmt.Sprintf("urn:pulumi:test::test::test:index:Dependency::%s", name))
		tools := property.New([]property.Value{}).WithSecret(name == "first").WithDependencies([]resourceurn.URN{dep})
		expected[name] = tools
		inputs := makeInputs(name).Set("tools", tools)
		go func() {
			response, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
			results <- result{name, inputs, response, err}
		}()
	}
	// Release blocked handlers even if an assertion aborts the test.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for range 2 {
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("both operations did not reach the synthetic API")
		}
	}
	close(release)
	var previous result
	for range 2 {
		got := <-results
		require.NoError(t, got.err)
		require.Equal(t, "agent_"+got.name, got.response.ID)
		require.True(t, expected[got.name].WithDependencies(nil).Equals(got.response.Properties.Get("tools")),
			"operation %s received another operation's collection metadata", got.name)
		previous = got
	}
	later := makeInputs("later")
	checked, err := server.Check(p.CheckRequest{Urn: agentURN, Inputs: later})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	_, err = server.Diff(p.DiffRequest{Urn: agentURN, ID: previous.response.ID,
		State: previous.response.Properties, OldInputs: previous.inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	created, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, "agent_later", created.ID)
	require.True(t, created.Properties.Get("tools").IsNull(), "prior invocation's collection presence must not survive")
	require.False(t, created.Properties.Get("tools").Secret())
	require.Empty(t, created.Properties.Get("tools").Dependencies())
}

func TestAgentRejectsNumericVersionString(t *testing.T) {
	var r sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"id":"agent_invalid","version":"7"}`), &r))
	s, err := agents.AgentObservedForTest(&r, agents.AgentArgs{Name: "desired", Model: agents.Model{ID: "model"}}, false)
	require.Error(t, err)
	require.Equal(t, agents.AgentState{}, s)
}

func TestAgentRejectsMalformedOptionalToolObjects(t *testing.T) {
	for _, tc := range []struct{ name, tool, field, raw string }{
		{"location scalar", "web_search", "user_location", `42`},
		{"location string", "web_search", "user_location", `"malformed"`},
		{"location array", "web_search", "user_location", `[]`},
		{"location empty object", "web_search", "user_location", `{}`},
		{"location numeric leaf without kind", "web_search", "user_location", `{"city":42}`},
		{"location invalid leaves", "web_search", "user_location", `{"type":42,"city":42,"country":[]}`},
		{"location unsupported kind", "web_search", "user_location", `{"type":"unsupported","city":"unsupported"}`},
		{"sources scalar", "web_fetch", "url_sources", `42`},
		{"sources string", "web_fetch", "url_sources", `"malformed"`},
		{"sources array", "web_fetch", "url_sources", `[]`},
		{"sources empty object", "web_fetch", "url_sources", `{}`},
		{"sources invalid leaves", "web_fetch", "url_sources", `{"client_tool_results":42,"server_tool_results":{"type":"unsupported"},"user_input":{"type":"invalid"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r sdk.BetaManagedAgentsAgent
			raw := fmt.Sprintf(`{"tools":[{"type":"agent_toolset_20260401","configs":[{"type":%q,"name":%q,"enabled":false,"permission_policy":{"type":"auto"},%q:%s}]}]}`, tc.tool, tc.tool, tc.field, tc.raw)
			require.NoError(t, json.Unmarshal([]byte(raw), &r))
			tools, err := agents.AgentStrictToolsForTest(r.Tools, nil, true)
			require.Error(t, err)
			require.Nil(t, tools)
		})
	}
}

func TestAgentReferenceProtocolAndSuccessfulPins(t *testing.T) {
	prior := []agents.RosterEntry{{Type: "self"}, {Type: "agent", ID: agents.AgentPtrForTest("agent_unpinned")}, {Type: "agent", ID: agents.AgentPtrForTest("agent_pinned"), Version: agents.AgentPtrForTest(2)}}
	var refs []sdk.BetaManagedAgentsAgentReference
	require.NoError(t, json.Unmarshal([]byte(`[{"type":"agent","id":"agent_owner","version":6},{"type":"agent","id":"agent_unpinned","version":5},{"type":"agent","id":"agent_pinned","version":4}]`), &refs))
	got, err := agents.AgentObservedReferencesForTest(refs, prior, false, "agent_owner")
	require.NoError(t, err)
	require.Equal(t, []agents.RosterEntry{{Type: "self"}, {Type: "agent", ID: agents.AgentPtrForTest("agent_unpinned")}, {Type: "agent", ID: agents.AgentPtrForTest("agent_pinned"), Version: agents.AgentPtrForTest(4)}}, got)
	require.Equal(t, 2, *prior[2].Version)
	for _, raw := range []string{
		`[{"type":"agent","id":"agent_pinned","version":"3"}]`,
		`[{"type":"agent","id":"agent_foreign","version":"malformed"}]`,
		`[{"type":"agent","id":42,"version":999}]`,
		`[{"type":"agent","id":"agent_pinned","version":3},{"type":"agent","id":42,"version":9}]`,
	} {
		require.NoError(t, json.Unmarshal([]byte(raw), &refs))
		got, err := agents.AgentObservedReferencesForTest(refs, prior, false, "agent_owner")
		require.Error(t, err)
		require.Nil(t, got, "malformed references must not inherit accepted identities or pins")
	}
}

func agentMetadataPath(parts ...any) property.Path {
	segments := make([]property.PathSegment, len(parts))
	for i, p := range parts {
		switch p := p.(type) {
		case string:
			segments[i] = property.NewSegment(p)
		case int:
			segments[i] = property.NewSegment(p)
		}
	}
	return property.PathFromSegments(segments...)
}

func agentPathSecret(t *testing.T, value property.Value, path property.Path) bool {
	t.Helper()
	secret := value.Secret()
	path.Segments(func(segment property.PathSegment) bool {
		var err error
		value, err = property.PathFromSegments(segment).Get(value)
		require.NoError(t, err)
		secret = secret || value.Secret()
		return true
	})
	return secret
}

// Collection presence and secrecy survive typed encoding; custom-resource
// outputs must not inherit dependency annotations from their inputs.
func TestAgentKnownValuesAndCollectionMetadata(t *testing.T) {
	s, api := providertest.New(t)
	agentServerBehavior(api)
	dep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::known")
	annotated := func(v property.Value) property.Value {
		return v.WithSecret(true).WithDependencies([]resourceurn.URN{dep})
	}
	empty := annotated(property.New([]property.Value{}))
	inputs := agentContractInputs().Set("system", annotated(property.New(""))).Set("tools", property.New([]property.Value{
		property.New(providertest.Props(map[string]any{
			"type":          "agent_toolset_20260401",
			"defaultConfig": map[string]any{"enabled": annotated(property.New(false))},
			"configs":       []any{map[string]any{"name": "web_fetch", "enabled": annotated(property.New(false)), "allowedDomains": empty}},
		})),
	}))
	checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, inputs, checked.Inputs)
	created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: checked.Inputs})
	require.NoError(t, err)
	updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: checked.Inputs, Inputs: checked.Inputs.Set("name", property.New("renamed"))})
	require.NoError(t, err)
	require.Len(t, api.Requests(), 2, "neither mutation may issue an extra read")
	read, err := s.Read(p.ReadRequest{Urn: agentURN, ID: created.ID, Inputs: checked.Inputs, Properties: updated.Properties})
	require.NoError(t, err)
	for i, bag := range []property.Map{created.Properties, updated.Properties, read.Inputs, read.Properties} {
		require.Equal(t, property.New("").WithSecret(true), bag.Get("system").WithDependencies(nil))
		if i != 2 {
			require.Empty(t, bag.Get("system").Dependencies())
		}
		for _, path := range []property.Path{agentMetadataPath("tools", 0, "configs", 0, "enabled"), agentMetadataPath("tools", 0, "defaultConfig", "enabled")} {
			v, err := path.Get(property.New(bag))
			require.NoError(t, err)
			require.Equal(t, property.New(false), v.WithSecret(false).WithDependencies(nil))
			require.True(t, agentPathSecret(t, property.New(bag), path), "operation bag %d path %v", i, path)
			if i != 2 {
				require.Empty(t, v.Dependencies())
			}
		}
		v, err := agentMetadataPath("tools", 0, "configs", 0, "allowedDomains").Get(property.New(bag))
		require.NoError(t, err)
		require.Equal(t, property.New([]property.Value{}), v.WithSecret(false).WithDependencies(nil))
		require.True(t, agentPathSecret(t, property.New(bag), agentMetadataPath("tools", 0, "configs", 0, "allowedDomains")))
	}
}

func TestAgentEmptyCollectionMetadataCreateRead(t *testing.T) {
	dep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::leaf")
	parentDep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::parent")
	for _, secret := range []bool{false, true} {
		empty := property.New([]property.Value{}).WithSecret(secret).WithDependencies([]resourceurn.URN{dep})
		modern := func(key string, leaf any) map[string]any {
			return map[string]any{"multiagent": map[string]any{"type": "multiagent_20261001", key: map[string]any{"type": "enabled", "predefinedAgents": leaf}}}
		}
		modernResponse := func() map[string]any {
			return map[string]any{"multiagent": map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "disabled"}, "subagents": map[string]any{"type": "enabled", "inline_agents": map[string]any{"type": "enabled"}, "predefined_agents": []any{}}, "workflows": map[string]any{"type": "enabled", "inline_agents": map[string]any{"type": "enabled"}, "predefined_agents": []any{}}}}
		}
		for _, tc := range []struct {
			name            string
			input, observed map[string]any
			path            property.Path
		}{
			{"tools", map[string]any{"tools": empty}, map[string]any{"tools": []any{}}, agentMetadataPath("tools")},
			{"skills", map[string]any{"skills": empty}, map[string]any{"skills": []any{}}, agentMetadataPath("skills")},
			{"mcpServers", map[string]any{"mcpServers": empty}, map[string]any{"mcp_servers": []any{}}, agentMetadataPath("mcpServers")},
			{"configs", map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": empty}}}, map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{}}}}, agentMetadataPath("tools", 0, "configs")},
			{"allowedDomains", map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "allowedDomains": empty}}}}}, map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "allowed_domains": []any{}}}}}}, agentMetadataPath("tools", 0, "configs", 0, "allowedDomains")},
			{"blockedDomains", map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_search", "blockedDomains": empty}}}}}, map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_search", "blocked_domains": []any{}}}}}}, agentMetadataPath("tools", 0, "configs", 0, "blockedDomains")},
			{"clientFilter", map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "urlSources": map[string]any{"clientToolResults": map[string]any{"type": "only", "tools": empty}}}}}}}, map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "url_sources": map[string]any{"client_tool_results": map[string]any{"type": "only", "tools": []any{}}, "server_tool_results": nil, "user_input": nil}}}}}}, agentMetadataPath("tools", 0, "configs", 0, "urlSources", "clientToolResults", "tools")},
			{"serverFilter", map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "urlSources": map[string]any{"serverToolResults": map[string]any{"type": "except", "tools": empty}}}}}}}, map[string]any{"tools": []any{map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "url_sources": map[string]any{"server_tool_results": map[string]any{"type": "except", "tools": []any{}}, "client_tool_results": nil, "user_input": nil}}}}}}, agentMetadataPath("tools", 0, "configs", 0, "urlSources", "serverToolResults", "tools")},
			{"subagents", modern("subagents", empty), modernResponse(), agentMetadataPath("multiagent", "subagents", "predefinedAgents")},
			{"workflows", modern("workflows", empty), modernResponse(), agentMetadataPath("multiagent", "workflows", "predefinedAgents")},
		} {
			t.Run(tc.name+map[bool]string{true: "/secret", false: "/public"}[secret], func(t *testing.T) {
				s, api := providertest.New(t)
				observed := fakeapi.Object{"id": "agent_metadata", "name": "contract", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "original", "version": 1, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
				for k, v := range tc.observed {
					observed[k] = v
				}
				agentCompleteFake(observed)
				handler := func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
					w.Header().Set("Content-Type", "application/json")
					require.NoError(t, json.NewEncoder(w).Encode(observed))
				}
				api.Handle("POST /v1/agents", handler)
				api.Handle("GET /v1/agents/agent_metadata", handler)
				api.Handle("POST /v1/agents/agent_metadata", handler)
				inputs := agentContractInputs()
				for k, v := range tc.input {
					value := providertest.Props(map[string]any{"v": v}).Get("v")
					inputs = inputs.Set(k, value.WithDependencies(append(value.Dependencies(), parentDep)))
				}
				checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				require.Equal(t, inputs, checked.Inputs)
				wirePath := agentMetadataPath(tc.name)
				switch tc.name {
				case "configs":
					wirePath = agentMetadataPath("tools", 0, "configs")
				case "mcpServers":
					wirePath = agentMetadataPath("mcp_servers")
				case "allowedDomains":
					wirePath = agentMetadataPath("tools", 0, "configs", 0, "allowed_domains")
				case "blockedDomains":
					wirePath = agentMetadataPath("tools", 0, "configs", 0, "blocked_domains")
				case "clientFilter":
					wirePath = agentMetadataPath("tools", 0, "configs", 0, "url_sources", "client_tool_results", "tools")
				case "serverFilter":
					wirePath = agentMetadataPath("tools", 0, "configs", 0, "url_sources", "server_tool_results", "tools")
				case "subagents", "workflows":
					wirePath = agentMetadataPath("multiagent", tc.name, "predefined_agents")
				}
				assertWireEmpty := func(path string) {
					body := api.LastRequest(t, http.MethodPost, path).Body
					leaf, err := wirePath.Get(property.New(providertest.Props(body)))
					require.NoError(t, err)
					require.True(t, leaf.IsArray(), "SDK request must include the explicit empty list")
					require.Zero(t, leaf.AsArray().Len())
				}
				created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: checked.Inputs})
				require.NoError(t, err)
				assertWireEmpty("/v1/agents")
				n := len(api.Requests())
				updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: created.ID, State: created.Properties, OldInputs: checked.Inputs, Inputs: checked.Inputs.Set("name", property.New("renamed"))})
				require.NoError(t, err)
				require.Len(t, api.Requests(), n+1, "Update must not internally refresh")
				assertWireEmpty("/v1/agents/" + created.ID)
				read, err := s.Read(p.ReadRequest{Urn: agentURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
				require.NoError(t, err)
				for i, bag := range []property.Map{created.Properties, updated.Properties, read.Inputs, read.Properties} {
					leaf, err := tc.path.Get(property.New(bag))
					require.NoError(t, err)
					require.True(t, leaf.IsArray())
					require.Zero(t, leaf.AsArray().Len())
					require.Equal(t, secret, agentPathSecret(t, property.New(bag), tc.path), "operation bag %d", i)
					// Check preserves input dependencies; outputs represent this resource.
					if i != 2 {
						require.Empty(t, leaf.Dependencies())
					}
				}
				root := tc.path // Also check container metadata, not only the leaf.
				var first property.PathSegment
				root.Segments(func(seg property.PathSegment) bool { first = seg; return false })
				for i, bag := range []property.Map{created.Properties, updated.Properties, read.Inputs, read.Properties} {
					v, err := property.PathFromSegments(first).Get(property.New(bag))
					require.NoError(t, err)
					if i != 2 {
						require.Empty(t, v.Dependencies())
					}
				}
			})
		}
	}
}

// The pinned SDK coerces some JSON primitives despite marking them Valid. The
// resource must validate wire types, not persist a coercion as an observation.
func TestAgentResponsePrimitiveProtocol(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wire   []any
		bad    any
		legacy bool
	}{
		{"numeric model ID", []any{"model", "id"}, 42, false},
		{"boolean model ID", []any{"model", "id"}, true, false},
		{"array model ID", []any{"model", "id"}, []any{}, false},
		{"object model ID", []any{"model", "id"}, map[string]any{}, false},
		{"null model ID", []any{"model", "id"}, nil, false},
		{"numeric name", []any{"name"}, 42, false},
		{"numeric system", []any{"system"}, 42, false},
		{"numeric description", []any{"description"}, 42, false},
		{"numeric inference region", []any{"model", "inference_geo"}, 42, false},
		{"null model effort", []any{"model", "effort"}, nil, false},
		{"null model speed", []any{"model", "speed"}, nil, false},
		{"null inference region", []any{"model", "inference_geo"}, nil, false},
		{"numeric MCP name", []any{"mcp_servers", 0, "name"}, 42, false},
		{"numeric MCP URL", []any{"mcp_servers", 0, "url"}, 42, false},
		{"numeric skill ID", []any{"skills", 0, "skill_id"}, 42, false},
		{"numeric skill version", []any{"skills", 0, "version"}, 42, false},
		{"null skill version", []any{"skills", 0, "version"}, nil, false},
		{"numeric execution role", []any{"execution_identity", "role_arn"}, 42, false},
		{"null execution identity", []any{"execution_identity"}, nil, false},
		{"numeric custom name", []any{"tools", 1, "name"}, 42, false},
		{"numeric custom description", []any{"tools", 1, "description"}, 42, false},
		{"numeric MCP toolset reference", []any{"tools", 2, "mcp_server_name"}, 42, false},
		{"null toolset defaults", []any{"tools", 0, "default_config"}, nil, false},
		{"null toolset configs", []any{"tools", 0, "configs"}, nil, false},
		{"string enabled", []any{"tools", 0, "configs", 0, "enabled"}, "true", false},
		{"string content tokens", []any{"tools", 0, "configs", 0, "max_content_tokens"}, "32", false},
		{"fractional content tokens", []any{"tools", 0, "configs", 0, "max_content_tokens"}, 1.5, false},
		{"null domain list", []any{"tools", 0, "configs", 0, "allowed_domains"}, nil, false},
		{"numeric domain member", []any{"tools", 0, "configs", 0, "allowed_domains", 0}, 42, false},
		{"null domain member", []any{"tools", 0, "configs", 0, "allowed_domains", 0}, nil, false},
		{"numeric location city", []any{"tools", 0, "configs", 1, "user_location", "city"}, 42, false},
		{"numeric modern advisor", []any{"multiagent", "advisor", "model"}, 42, false},
		{"numeric legacy advisor", []any{"multiagent", "agents", 0, "model"}, 42, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			inputs, response := agentPrimitiveFixture(tc.legacy)
			handler := func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(response))
			}
			api.Handle("GET /v1/agents/agent_primitive", handler)
			api.Handle("POST /v1/agents", handler)
			api.Handle("POST /v1/agents/agent_primitive", handler)
			checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			// A positive control prevents malformed-fixture failures masking a regression.
			good, err := s.Read(p.ReadRequest{Urn: agentURN, ID: "agent_primitive"})
			require.NoError(t, err)
			require.Equal(t, "agent_primitive", good.ID)
			agentSetPrimitiveFixture(response, tc.wire, tc.bad)
			_, err = s.Read(p.ReadRequest{Urn: agentURN, ID: "agent_primitive"})
			require.Error(t, err, "Read must reject malformed documented primitives")
			created, err := s.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
			require.Error(t, err)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Zero(t, created.Properties.Len())
			desired := inputs.Set("name", property.New("updated"))
			updated, err := s.Update(p.UpdateRequest{Urn: agentURN, ID: good.ID, State: good.Properties, Inputs: desired})
			require.Error(t, err)
			require.Nil(t, updated.PartialState)
			require.Zero(t, updated.Properties.Len())
			require.Len(t, api.Requests(), 5, "failed create compensates with archive, never an internal recovery read")
			require.Equal(t, http.MethodPost, api.Requests()[3].Method)
			require.Equal(t, "/v1/agents/agent_primitive/archive", api.Requests()[3].Path)
		})
	}
}

func agentSetPrimitiveFixture(root map[string]any, path []any, value any) {
	var current any = root
	for _, segment := range path[:len(path)-1] {
		switch key := segment.(type) {
		case string:
			current = current.(map[string]any)[key]
		case int:
			current = current.([]any)[key]
		}
	}
	switch key := path[len(path)-1].(type) {
	case string:
		current.(map[string]any)[key] = value
	case int:
		current.([]any)[key] = value
	default:
		panic(fmt.Sprintf("unsupported fixture path segment %T", key))
	}
}

func agentPrimitiveFixture(legacy bool) (property.Map, map[string]any) {
	desired := map[string]any{
		"name": "created", "model": map[string]any{"id": "claude-opus-5-5", "effort": "high", "speed": "standard", "inferenceGeo": "us"},
		"system": "requested system", "description": "requested description", "metadata": map[string]any{"owner": "test"},
		"mcpServers":        []any{map[string]any{"name": "docs", "url": "https://example.invalid/mcp"}},
		"skills":            []any{map[string]any{"type": "custom", "skillId": "skill_requested", "version": "requested-version"}},
		"executionIdentity": map[string]any{"type": "aws_role", "roleArn": "arn:aws:iam::123456789012:role/requested"},
		"multiagent":        map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "enabled", "model": "advisor-model"}},
		"tools": []any{
			map[string]any{"type": "agent_toolset_20260401", "configs": []any{map[string]any{"name": "web_fetch", "enabled": false, "permissionPolicy": "auto", "maxContentTokens": 64, "allowedDomains": []any{"requested.invalid"}}, map[string]any{"name": "web_search", "enabled": false, "permissionPolicy": "auto", "userLocation": map[string]any{"city": "requested city"}}}},
			map[string]any{"type": "custom", "name": "custom", "description": "requested custom description", "inputSchema": map[string]any{"type": "object"}},
			map[string]any{"type": "mcp_toolset", "mcpServerName": "docs"},
		},
	}
	response := map[string]any{
		"id": "agent_primitive", "type": "agent", "name": "observed", "model": map[string]any{"id": "claude-opus-5-5", "effort": map[string]any{"type": "high"}, "speed": "standard", "inference_geo": "us"},
		"system": "requested system", "description": "requested description", "metadata": map[string]any{"owner": "test"},
		"mcp_servers":        []any{map[string]any{"type": "url", "name": "docs", "url": "https://example.invalid/mcp"}},
		"skills":             []any{map[string]any{"type": "custom", "skill_id": "skill_requested", "version": "requested-version"}},
		"execution_identity": map[string]any{"type": "aws_role", "role_arn": "arn:aws:iam::123456789012:role/requested"},
		"multiagent":         map[string]any{"type": "multiagent_20261001", "advisor": map[string]any{"type": "enabled", "model": "advisor-model"}, "subagents": map[string]any{"type": "disabled"}, "workflows": map[string]any{"type": "disabled"}},
		"created_at":         "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "archived_at": nil, "version": 2,
		"tools": []any{
			map[string]any{"type": "agent_toolset_20260401", "default_config": map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "auto"}}, "configs": []any{map[string]any{"type": "web_fetch", "name": "web_fetch", "enabled": false, "permission_policy": map[string]any{"type": "auto"}, "max_content_tokens": 64, "allowed_domains": []any{"requested.invalid"}, "url_sources": nil}, map[string]any{"type": "web_search", "name": "web_search", "enabled": false, "permission_policy": map[string]any{"type": "auto"}, "user_location": map[string]any{"type": "approximate", "city": "requested city"}}}},
			map[string]any{"type": "custom", "name": "custom", "description": "requested custom description", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"type": "mcp_toolset", "mcp_server_name": "docs", "default_config": map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "auto"}}, "configs": []any{}},
		},
	}
	if legacy {
		desired["multiagent"] = map[string]any{"agents": []any{map[string]any{"type": "advisor", "model": "advisor-model"}}}
		response["multiagent"] = map[string]any{"type": "coordinator", "agents": []any{map[string]any{"type": "advisor", "model": "advisor-model"}}}
	}
	return providertest.Props(desired), response
}

func TestAgentSDKFullDesiredAndRemovals(t *testing.T) {
	old := agents.AgentState{AgentArgs: agents.AgentArgs{Name: "before", Model: agents.Model{ID: "claude-opus-5-5", Speed: agents.AgentPtrForTest(agents.Speed("fast")), InferenceGeo: agents.AgentPtrForTest("us")}, System: agents.AgentPtrForTest("old"), Description: agents.AgentPtrForTest("old"), Tools: &[]agents.Tool{{Type: agents.AgentToolset}}, Skills: &[]agents.SkillRef{{Type: "custom", SkillID: "skill_old"}}, MCPServers: &[]agents.MCPServer{{Name: "old", URL: "https://example.invalid"}}, Metadata: map[string]string{"keep": "old", "remove": "old"}, Multiagent: &agents.Multiagent{Agents: &[]agents.RosterEntry{{Type: "self"}}}, ExecutionIdentity: &agents.ExecutionIdentity{Type: "aws_role", RoleARN: agents.AgentPtrForTest("arn:aws:iam::123456789012:role/test")}}, Version: 7}
	desired := agents.AgentArgs{Name: "desired", Model: agents.Model{ID: "claude-opus-5-5"}, Metadata: map[string]string{"keep": "new", "empty": ""}}
	params, err := agents.AgentUpdateForTest(old, desired)
	require.NoError(t, err)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"desired","model":{"id":"claude-opus-5-5","speed":"standard"},"system":"","description":"","tools":[],"skills":[],"mcp_servers":[],"multiagent":null,"execution_identity":null,"metadata":{"keep":"new","empty":"","remove":null},"version":7}`, string(raw))
	require.Equal(t, "old", old.Metadata["keep"], "conversion must not mutate engine state")
	require.Equal(t, "fast", string(*old.Model.Speed))
}

func TestAgentSDKModernMergeRemovals(t *testing.T) {
	typ := "multiagent_20261001"
	old := agents.AgentState{AgentArgs: agents.AgentArgs{Multiagent: &agents.Multiagent{Type: &typ, Advisor: &agents.AgentAdvisor{Type: "enabled", Model: agents.AgentPtrForTest("advisor")}, Subagents: &agents.AgentDelegation{Type: "enabled", InlineAgents: agents.AgentPtrForTest("disabled"), PredefinedAgents: &[]agents.RosterEntry{{Type: "self"}}}, Workflows: &agents.AgentDelegation{Type: "disabled"}}}}
	desired := agents.AgentArgs{Name: "modern", Model: agents.Model{ID: "claude-opus-5-5"}, Multiagent: &agents.Multiagent{Type: &typ, Subagents: &agents.AgentDelegation{Type: "enabled"}}}
	params, err := agents.AgentUpdateForTest(old, desired)
	require.NoError(t, err)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"modern","model":{"id":"claude-opus-5-5"},"multiagent":{"type":"multiagent_20261001","advisor":null,"subagents":{"type":"enabled","inline_agents":null,"predefined_agents":null},"workflows":null}}`, string(raw))
}

func TestAgentSDKTypedToolMappings(t *testing.T) {
	a := agents.AgentArgs{Name: "tools", Model: agents.Model{ID: "claude-opus-5-5", Effort: agents.AgentPtrForTest(agents.Effort("high"))}, Tools: &[]agents.Tool{{Type: agents.AgentToolset, Configs: &[]agents.ToolConfig{{Name: "bash", Enabled: agents.AgentPtrForTest(false), PermissionPolicy: agents.AgentPtrForTest(agents.PermissionPolicy("always_ask"))}, {Name: "web_fetch", MaxContentTokens: agents.AgentPtrForTest(256), AllowedDomains: &[]string{"example.invalid"}, URLSources: &agents.FetchURLSources{ClientToolResults: &agents.FetchToolFilter{Type: "only", Tools: &[]string{"custom"}}, UserInput: agents.AgentPtrForTest("none")}}}}}, ExecutionIdentity: &agents.ExecutionIdentity{Type: "aws_role", RoleARN: agents.AgentPtrForTest("arn:aws:iam::123456789012:role/test")}}
	params, err := agents.AgentNewForTest(a)
	require.NoError(t, err)
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"tools","model":{"id":"claude-opus-5-5","effort":"high"},"execution_identity":{"type":"aws_role","role_arn":"arn:aws:iam::123456789012:role/test"},"tools":[{"type":"agent_toolset_20260401","configs":[{"name":"bash","enabled":false,"permission_policy":{"type":"always_ask"}},{"name":"web_fetch","max_content_tokens":256,"allowed_domains":["example.invalid"],"url_sources":{"client_tool_results":{"type":"only","tools":[{"type":"tool_reference","name":"custom"}]},"user_input":"none"}}]}]}`, string(raw))
}

func TestAgentRejectsIncompleteLeaves(t *testing.T) {
	prior := agents.AgentArgs{Name: "desired", Model: agents.Model{ID: "desired-model", Speed: agents.AgentPtrForTest(agents.Speed("fast"))}, Metadata: map[string]string{"valid": "requested", "invalid": "accepted"}, Tools: &[]agents.Tool{{Type: agents.AgentToolset, Configs: &[]agents.ToolConfig{{Name: "bash", Enabled: agents.AgentPtrForTest(false), PermissionPolicy: agents.AgentPtrForTest(agents.PermissionPolicy("always_ask"))}}}}}
	var r sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"id":"agent_partial","name":"observed","model":{"id":"observed-model"},"metadata":{"valid":"observed","invalid":42},"tools":[{"type":"agent_toolset_20260401","configs":[{"type":"bash","name":"bash","enabled":true,"permission_policy":{"type":"unknown"}}]}]}`), &r))
	s, err := agents.AgentObservedForTest(&r, prior, false)
	require.Error(t, err)
	require.Equal(t, agents.AgentState{}, s, "failed conversion must not return reconstructed state")
	require.Equal(t, "requested", prior.Metadata["valid"])
	require.False(t, *(*(*prior.Tools)[0].Configs)[0].Enabled, "conversion must not mutate inputs")
}

func TestAgentSDKObservedModernImport(t *testing.T) {
	var r sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"id":"agent_modern","name":"modern","model":{"id":"claude-opus-5-5","effort":{"type":"high"},"speed":"standard"},"system":"","description":"","metadata":{},"version":5,"mcp_servers":[],"tools":[],"skills":[],"multiagent":{"type":"multiagent_20261001","advisor":{"type":"enabled","model":"advisor"},"subagents":{"type":"enabled","inline_agents":{"type":"disabled"},"predefined_agents":[{"type":"agent","id":"agent_sub","version":2}]},"workflows":{"type":"disabled"}},"execution_identity":{"type":"aws_role","role_arn":"arn:aws:iam::123456789012:role/test"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","archived_at":null}`), &r))
	s, err := agents.AgentObservedForTest(&r, agents.AgentArgs{}, true)
	require.NoError(t, err)
	require.Equal(t, "multiagent_20261001", *s.Multiagent.Type)
	require.Equal(t, "advisor", *s.Multiagent.Advisor.Model)
	require.Equal(t, "disabled", *s.Multiagent.Subagents.InlineAgents)
	require.Equal(t, 2, *(*s.Multiagent.Subagents.PredefinedAgents)[0].Version)
	require.Equal(t, "aws_role", s.ExecutionIdentity.Type)
	p, err := agents.AgentNewForTest(s.AgentArgs)
	require.NoError(t, err)
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"predefined_agents":[{"id":"agent_sub","version":2,"type":"agent"}]`)
}

func TestAgentToolDefaultConfigListsDoNotAppearOnRefresh(t *testing.T) {
	server, api := providertest.New(t)
	inputs := providertest.Props(map[string]any{
		"name": "triage", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "example prompt",
		"mcpServers": []any{map[string]any{"name": "github", "url": "https://api.githubcopilot.com/mcp/"}},
		"tools": []any{
			map[string]any{"type": "agent_toolset_20260401", "defaultConfig": map[string]any{"permissionPolicy": "always_allow"}},
			map[string]any{"type": "mcp_toolset", "mcpServerName": "github"},
		},
	})
	tools := []any{
		map[string]any{"type": "agent_toolset_20260401", "default_config": map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "always_allow"}}},
		map[string]any{"type": "mcp_toolset", "mcp_server_name": "github", "default_config": map[string]any{"enabled": true, "permission_policy": map[string]any{"type": "always_ask"}}},
	}
	row := fakeapi.Object{"id": "agent_defaults", "type": "agent", "name": "triage", "model": map[string]any{"id": "claude-opus-5-5"}, "system": "example prompt", "description": nil, "tools": tools,
		"version": 1, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	agentCompleteFake(row)
	api.Handle("POST /v1/agents", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, row) })
	api.Handle("GET /v1/agents/agent_defaults", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		for _, tool := range tools {
			tool.(map[string]any)["configs"] = []any{}
		}
		fakeapi.WriteJSON(w, row)
	})
	created, err := server.Create(p.CreateRequest{Urn: agentURN, Properties: inputs})
	require.NoError(t, err)
	read, err := server.Read(p.ReadRequest{Urn: agentURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, err)
	require.Equal(t, created.Properties.Get("tools"), read.Properties.Get("tools"))
	require.Equal(t, inputs.Get("tools"), read.Inputs.Get("tools"))
	require.Len(t, api.Requests(), 2)
	imported, err := server.Read(p.ReadRequest{Urn: agentURN, ID: created.ID})
	require.NoError(t, err)
	for _, tool := range imported.Inputs.Get("tools").AsArray().All {
		configs := tool.AsMap().Get("configs")
		require.True(t, configs.IsArray(), "import recovers the observed explicit empty list")
		require.Zero(t, configs.AsArray().Len())
	}
}

func TestAgentKnownToolCountLimit(t *testing.T) {
	for _, tc := range []struct {
		name                                         string
		count                                        int
		unknownTool, unknownName, wholeUnknown, fail bool
	}{
		{name: "255", count: 255},
		{name: "256 boundary", count: 256},
		{name: "257 rejected", count: 257, fail: true},
		{name: "256 plus unknown type is indeterminate", count: 256, unknownTool: true},
		{name: "257 plus unknown type still rejected", count: 257, unknownTool: true, fail: true},
		{name: "257 known custom types with unknown names still rejected", count: 257, unknownName: true, fail: true},
		{name: "whole unknown collection", wholeUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			tools := make([]property.Value, 0, tc.count+1)
			for i := 0; i < tc.count; i++ {
				name := property.New(fmt.Sprintf("tool_%d", i))
				if tc.unknownName {
					name = property.New(property.Computed)
				}
				tools = append(tools, property.New(providertest.Props(map[string]any{"type": "custom", "name": name, "description": "test", "inputSchema": map[string]any{}})))
			}
			if tc.unknownTool {
				tools = append(tools, property.New(property.Computed))
			}
			value := property.New(tools)
			if tc.wholeUnknown {
				value = property.New(property.Computed)
			}
			inputs := agentContractInputs().Set("tools", value)
			checked, err := s.Check(p.CheckRequest{Urn: agentURN, Inputs: inputs})
			require.NoError(t, err)
			if tc.fail {
				require.Contains(t, checked.Failures, p.CheckFailure{Property: "tools", Reason: "must contain at most 256 tools across all toolsets"})
			} else {
				require.Empty(t, checked.Failures)
			}
			require.Empty(t, api.Requests())
		})
	}
}
