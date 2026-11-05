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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

const environmentToken = "anthropic:agents:Environment"

var environmentURN = providertest.URN(environmentToken)

// environmentServerBehavior fills in the defaults the real API does.
func environmentServerBehavior(api *fakeapi.Server) {
	api.SetHooks("environments", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) {
			obj["scope"] = "organization"
			if obj["description"] == nil {
				obj["description"] = ""
			}
		},
	})
}

func baseEnvironment() map[string]any {
	return map[string]any{
		"name": "sandbox",
		"config": map[string]any{
			"type": "cloud",
			"networking": map[string]any{
				"type":                 "limited",
				"allowedHosts":         []any{"api.github.com"},
				"allowPackageManagers": true,
			},
			"packages": map[string]any{"pip": []any{"pandas"}},
		},
		"metadata": map[string]any{"team": "data", "tier": "1"},
	}
}

func TestEnvironmentLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	environmentServerBehavior(api)

	updated := baseEnvironment()
	updated["description"] = "Where data agents run."
	updated["metadata"] = map[string]any{"team": "data"}

	integration.LifeCycleTest{
		Resource: environmentToken,
		Create: integration.Operation{
			Inputs: providertest.Props(baseEnvironment()),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/environments")
				assert.Equal(t, "managed-agents-2026-04-01", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"type": "cloud",
					"networking": map[string]any{
						"type": "limited", "allowed_hosts": []any{"api.github.com"}, "allow_package_managers": true,
					},
					"packages": map[string]any{
						"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{},
						"pip": []any{"pandas"},
					},
				}, req.Body["config"])
				_, hasScope := output.GetOk("scope")
				assert.False(t, hasScope, "the server's default scope must not leak into the declared inputs")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(updated),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{
					"description": "Where data agents run.",
					"metadata":    map[string]any{"team": "data", "tier": nil},
				}, api.Last().Body)
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Regexp(t, `^/v1/environments/environment_\d+$`, last.Path)
}

func TestEnvironmentDroppedPackagesAreCleared(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	environmentServerBehavior(api)
	created, inputs := providertest.Create(t, server, environmentURN, providertest.Props(baseEnvironment()))

	updated := baseEnvironment()
	delete(updated["config"].(map[string]any), "packages")
	check, err := server.Check(p.CheckRequest{Urn: environmentURN, State: created.Properties,
		Inputs: providertest.Props(updated)})
	require.NoError(t, err)
	_, err = server.Update(p.UpdateRequest{ID: created.ID, Urn: environmentURN, State: created.Properties,
		OldInputs: inputs, Inputs: check.Inputs})
	require.NoError(t, err)

	assert.Equal(t, map[string]any{
		"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{},
	}, api.Last().Body["config"].(map[string]any)["packages"],
		"the API keeps omitted config fields, so dropped packages are sent empty")
}

func TestEnvironmentRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	environmentServerBehavior(api)
	created, inputs := providertest.Create(t, server, environmentURN, providertest.Props(baseEnvironment()))

	refreshed := providertest.Read(t, server, environmentURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs, "server defaults are not drift")

	api.Mutate("/v1/environments/"+created.ID, func(obj fakeapi.Object) { obj["name"] = "renamed" })
	refreshed = providertest.Read(t, server, environmentURN, created, inputs)
	assert.Equal(t, "renamed", refreshed.Inputs.Get("name").AsString())
}

func TestEnvironmentDiff(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	environmentServerBehavior(api)
	created, inputs := providertest.Create(t, server, environmentURN, providertest.Props(baseEnvironment()))

	renamed := baseEnvironment()
	renamed["name"] = "renamed"
	d := providertest.Diff(t, server, environmentURN, created, inputs, providertest.Props(renamed))
	assert.Equal(t, map[string]p.PropertyDiff{"name": {Kind: p.Update, InputDiff: true}}, d.DetailedDiff)

	selfHosted := baseEnvironment()
	selfHosted["config"] = map[string]any{"type": "self_hosted"}
	d = providertest.Diff(t, server, environmentURN, created, inputs, providertest.Props(selfHosted))
	assert.Equal(t, map[string]p.PropertyDiff{"config": {Kind: p.UpdateReplace, InputDiff: true}}, d.DetailedDiff,
		"changing config.type replaces the environment")

	d = providertest.Diff(t, server, environmentURN, created, inputs, providertest.Props(baseEnvironment()))
	assert.False(t, d.HasChanges)
}

func TestEnvironmentCheck(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)

	inputs := baseEnvironment()
	inputs["config"].(map[string]any)["networking"].(map[string]any)["allowPackageManagers"] = false
	resp, err := server.Check(p.CheckRequest{Urn: environmentURN, Inputs: providertest.Props(inputs)})
	require.NoError(t, err)
	assert.Equal(t, []p.CheckFailure{{
		Property: "config.networking.allowPackageManagers",
		Reason:   "installing packages behind limited networking requires allowPackageManagers to be true",
	}}, resp.Failures)
}

func TestEnvironmentDeleteFallsBackToArchive(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	environmentServerBehavior(api)
	api.Handle("DELETE /v1/environments/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusConflict, "environment is referenced by sessions")
	})
	created, _ := providertest.Create(t, server, environmentURN, providertest.Props(baseEnvironment()))

	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: environmentURN, Properties: created.Properties}))
	last := api.Last()
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, "/v1/environments/"+created.ID+"/archive", last.Path)
	assert.NotNil(t, api.Object("/v1/environments/" + created.ID)["archived_at"])
}
