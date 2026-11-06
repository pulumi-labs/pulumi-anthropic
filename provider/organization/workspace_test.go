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

package organization_test

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

// workspaceServerBehavior fills in the server-side defaults the real API reports.
func workspaceServerBehavior(api *fakeapi.Server) {
	api.SetHooks("workspaces", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["compartment_id"] = "cmpt_1"
		residency, _ := obj["data_residency"].(map[string]any)
		if residency == nil {
			residency = map[string]any{}
			obj["data_residency"] = residency
		}
		for k, v := range map[string]any{"workspace_geo": "us", "allowed_inference_geos": "unrestricted",
			"default_inference_geo": "global"} {
			if _, ok := residency[k]; !ok {
				residency[k] = v
			}
		}
	}})
}

func baseWorkspace() map[string]any {
	return map[string]any{
		"name":         "research",
		"displayColor": "#6C5BB9",
		"tags":         map[string]any{"team": "research"},
		"dataResidency": map[string]any{
			"workspaceGeo":         "us",
			"allowedInferenceGeos": []any{"us"},
		},
	}
}

func TestWorkspaceLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	workspaceServerBehavior(api)

	updated := baseWorkspace()
	updated["name"] = "research-2"
	updated["dataResidency"] = map[string]any{"workspaceGeo": "us"}

	integration.LifeCycleTest{
		Resource: "anthropic:organization:Workspace",
		Create: integration.Operation{
			Inputs: providertest.Props(baseWorkspace()),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "cmpt_1", output.Get("compartmentId").AsString())
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/workspaces")
				assert.Equal(t, "sk-ant-admin-test", req.Header.Get("X-Api-Key"))
				assert.Empty(t, req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"name":          "research",
					"display_color": "#6C5BB9",
					"tags":          map[string]any{"team": "research"},
					"data_residency": map[string]any{
						"workspace_geo":          "us",
						"allowed_inference_geos": []any{"us"},
					},
				}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(updated),
			Hook: func(_, output property.Map) {
				reqs := api.Requests()
				// Lifting the restriction must say so explicitly, and the workspace's
				// geography, which the API rejects on update, is not sent.
				assert.Equal(t, fakeapi.Object{
					"name":           "research-2",
					"data_residency": map[string]any{"allowed_inference_geos": "unrestricted"},
				}, reqs[len(reqs)-1].Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Regexp(t, `^/v1/organizations/workspaces/workspace_\d+/archive$`, last.Path,
		"deleting a workspace archives it")
}

func TestWorkspaceRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	workspaceServerBehavior(api)

	inputs := baseWorkspace()
	delete(inputs, "dataResidency")
	created, checked := providertest.Create(t, server, urn("Workspace"), providertest.Props(inputs))

	// The server's default data residency does not show up as drift.
	resp := providertest.Read(t, server, urn("Workspace"), created, checked)
	assert.Equal(t, checked, resp.Inputs)

	path := "/v1/organizations/workspaces/" + created.ID
	api.Mutate(path, func(obj fakeapi.Object) { obj["display_color"] = "#000000" })
	resp = providertest.Read(t, server, urn("Workspace"), created, checked)
	assert.Equal(t, "#000000", resp.Inputs.Get("displayColor").AsString())

	api.Mutate(path, func(obj fakeapi.Object) { obj["archived_at"] = "2026-09-25T00:00:00Z" })
	resp = providertest.Read(t, server, urn("Workspace"), created, checked)
	assert.Empty(t, resp.ID, "an archived workspace reads as deleted")
}

func TestWorkspaceUnrestrictedRoundTrip(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	workspaceServerBehavior(api)

	inputs := baseWorkspace()
	inputs["dataResidency"] = map[string]any{"workspaceGeo": "us", "defaultInferenceGeo": "us"}
	created, checked := providertest.Create(t, server, urn("Workspace"), providertest.Props(inputs))
	req := api.LastRequest(t, http.MethodPost, "/v1/organizations/workspaces")
	assert.Equal(t, "unrestricted", req.Body["data_residency"].(map[string]any)["allowed_inference_geos"])

	resp := providertest.Read(t, server, urn("Workspace"), created, checked)
	assert.Equal(t, checked, resp.Inputs, "\"unrestricted\" reads back as unset")
}

func TestWorkspaceDiff(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	workspaceServerBehavior(api)

	unkeyed, unkeyedInputs := providertest.Create(t, server, urn("Workspace"), providertest.Props(baseWorkspace()))
	keyedInputs := baseWorkspace()
	keyedInputs["externalKeyId"] = "ekey_1"
	keyed, keyedChecked := providertest.Create(t, server, urn("Workspace"), providertest.Props(keyedInputs))

	rekeyed := baseWorkspace()
	rekeyed["externalKeyId"] = "ekey_2"
	moved := baseWorkspace()
	moved["dataResidency"] = map[string]any{"workspaceGeo": "eu", "allowedInferenceGeos": []any{"us"}}
	renamed := baseWorkspace()
	renamed["name"] = "other"

	for name, tc := range map[string]struct {
		from   p.CreateResponse
		inputs property.Map
		to     map[string]any
		want   map[string]p.PropertyDiff
	}{
		"setting a key updates": {unkeyed, unkeyedInputs, keyedInputs, map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.Add, InputDiff: true}}},
		"changing a key replaces": {keyed, keyedChecked, rekeyed, map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.UpdateReplace, InputDiff: true}}},
		"removing a key replaces": {keyed, keyedChecked, baseWorkspace(), map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.DeleteReplace, InputDiff: true}}},
		"changing the geography replaces": {unkeyed, unkeyedInputs, moved, map[string]p.PropertyDiff{
			"dataResidency": {Kind: p.UpdateReplace, InputDiff: true}}},
		"renaming updates": {unkeyed, unkeyedInputs, renamed, map[string]p.PropertyDiff{
			"name": {Kind: p.Update, InputDiff: true}}},
		"no change": {unkeyed, unkeyedInputs, baseWorkspace(), map[string]p.PropertyDiff{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := providertest.Diff(t, server, urn("Workspace"), tc.from, tc.inputs, providertest.Props(tc.to))
			require.Equal(t, len(tc.want) > 0, got.HasChanges)
			assert.Equal(t, tc.want, got.DetailedDiff)
		})
	}
}
