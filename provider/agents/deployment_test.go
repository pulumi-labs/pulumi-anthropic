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
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const deploymentToken = "anthropic:agents:Deployment"

var deploymentURN = providertest.URN(deploymentToken)

// deploymentServerBehavior mimics the API: a bare agent ID resolves to the agent's
// latest version (here 7), the response always carries the agent object, deployments
// start active, and pause and unpause set the status.
func deploymentServerBehavior(api *fakeapi.Server) {
	resolve := func(obj fakeapi.Object) {
		if id, ok := obj["agent"].(string); ok {
			obj["agent"] = map[string]any{"type": "agent", "id": id, "version": 7.0}
		}
	}
	api.SetHooks("deployments", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) {
			resolve(obj)
			obj["status"] = "active"
		},
		OnUpdate: func(_, after, _ fakeapi.Object) error { resolve(after); return nil },
	})
	for action, status := range map[string]string{"pause": "paused", "unpause": "active"} {
		api.Handle("POST /v1/deployments/{id}/"+action, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
			path := "/v1/deployments/" + r.PathValue("id")
			api.Mutate(path, func(obj fakeapi.Object) { obj["status"] = status })
			fakeapi.WriteJSON(w, api.Object(path))
		})
	}
}

func baseDeployment() map[string]any {
	return map[string]any{
		"name":          "triage",
		"agent":         map[string]any{"id": "agent_1"},
		"environmentId": "env_1",
		"initialEvents": []any{map[string]any{
			"type": "user.message", "content": []any{map[string]any{"type": "text", "text": "Triage new issues."}},
		}},
		"resources": []any{map[string]any{
			"type": "github_repository", "url": "https://github.com/acme/app",
			"checkout": map[string]any{"type": "branch", "name": "main"}, "authorizationToken": secret("ghp"),
		}},
		"schedule": map[string]any{"expression": "0 9 * * 1-5", "timezone": "UTC"},
		"budget":   map[string]any{"maxListCost": map[string]any{"amount": "2500", "currency": "USD"}},
	}
}

func TestDeploymentLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	deploymentServerBehavior(api)

	pinned := baseDeployment()
	pinned["agent"] = map[string]any{"id": "agent_1", "version": 3}

	integration.LifeCycleTest{
		Resource: deploymentToken,
		Create: integration.Operation{
			Inputs: providertest.Props(baseDeployment()),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/deployments")
				assert.Equal(t, "managed-agents-2026-04-01", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"name":           "triage",
					"agent":          "agent_1",
					"environment_id": "env_1",
					"initial_events": []any{map[string]any{
						"type": "user.message", "content": []any{map[string]any{"type": "text", "text": "Triage new issues."}},
					}},
					"resources": []any{map[string]any{
						"type": "github_repository", "url": "https://github.com/acme/app",
						"checkout": map[string]any{"type": "branch", "name": "main"}, "authorization_token": "ghp",
					}},
					"schedule": map[string]any{"type": "cron", "expression": "0 9 * * 1-5", "timezone": "UTC"},
					"budget": map[string]any{
						"type": "limit", "max_list_cost": map[string]any{"amount": "2500", "currency": "USD"},
					},
				}, req.Body, "an unversioned agent is sent as its bare ID; paused is not on the wire")
				_, hasVersion := output.Get("agent").AsMap().GetOk("version")
				assert.False(t, hasVersion, "the version the server resolved is not declared")
				assert.Equal(t, "active", output.Get("status").AsString())
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(pinned),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{
					"agent": map[string]any{"type": "agent", "id": "agent_1", "version": 3.0},
				}, api.Last().Body, "a pinned agent is sent as an object")
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Regexp(t, `^/v1/deployments/deployment_\d+/archive$`, last.Path, "deleting a deployment archives it")
}

func TestDeploymentPause(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	deploymentServerBehavior(api)

	paused := baseDeployment()
	paused["paused"] = true
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(paused))
	assert.Regexp(t, `/pause$`, api.Last().Path, "a deployment declared paused is paused after creation")
	assert.Equal(t, "paused", created.Properties.Get("status").AsString())

	resumed := baseDeployment()
	resumed["paused"] = false
	check, err := server.Check(p.CheckRequest{Urn: deploymentURN, State: created.Properties, Inputs: providertest.Props(resumed)})
	require.NoError(t, err)
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: created.Properties,
		OldInputs: inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	assert.Regexp(t, `/unpause$`, api.Last().Path)
	assert.Equal(t, "active", updated.Properties.Get("status").AsString())

	// Pausing outside of Pulumi is drift when pausing is declared.
	api.Mutate("/v1/deployments/"+created.ID, func(obj fakeapi.Object) { obj["status"] = "paused" })
	refreshed := providertest.Read(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: updated.Properties}, check.Inputs)
	assert.True(t, refreshed.Inputs.Get("paused").AsBool())
}

func TestDeploymentUnmanagedPause(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))

	// When pausing is not declared, a pause made elsewhere is left alone.
	api.Mutate("/v1/deployments/"+created.ID, func(obj fakeapi.Object) { obj["status"] = "paused" })
	refreshed := providertest.Read(t, server, deploymentURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs)
	assert.Equal(t, "paused", refreshed.Properties.Get("status").AsString())
}

func TestDeploymentRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))

	refreshed := providertest.Read(t, server, deploymentURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs, "the resolved agent version and write-only token are not drift")

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: deploymentURN})
	require.NoError(t, err)
	assert.Equal(t, 7.0, imported.Inputs.Get("agent").AsMap().Get("version").AsNumber())
	assert.False(t, imported.Inputs.Get("paused").AsBool(), "an import adopts the pause status")
}
