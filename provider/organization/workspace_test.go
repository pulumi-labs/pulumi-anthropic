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
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workspaceServerBehavior supplies synthetic SDK-shaped observations. It is not
// independent evidence of platform behavior.
func workspaceServerBehavior(api *fakeapi.Server) {
	api.SetHooks("workspaces", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["compartment_id"] = "cmpt_1"
		if _, ok := obj["user_profile_id"]; !ok {
			obj["user_profile_id"] = nil
		}
		if _, ok := obj["external_key_id"]; !ok {
			obj["external_key_id"] = nil
		}
		if _, ok := obj["display_color"]; !ok {
			obj["display_color"] = "#123456"
		}
		if _, ok := obj["tags"]; !ok {
			obj["tags"] = map[string]any{}
		}
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
			"defaultInferenceGeo":  "us",
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

	// The older LifeCycleTest helper omits engine OldInputs. Drive these ordinary
	// protocol calls explicitly so removal ownership matches the actual engine.
	created, checked := providertest.Create(t, server, providertest.URN("anthropic:organization:Workspace"), providertest.Props(baseWorkspace()))
	assert.Equal(t, "cmpt_1", created.Properties.Get("compartmentId").AsString())
	createBody := api.LastRequest(t, http.MethodPost, "/v1/organizations/workspaces")
	assert.Equal(t, "sk-ant-admin-test", createBody.Header.Get("x-api-key"))
	assert.Empty(t, createBody.Header.Get("anthropic-beta"))
	assert.Equal(t, fakeapi.Object{"name": "research", "display_color": "#6C5BB9", "tags": map[string]any{"team": "research"}, "data_residency": map[string]any{"workspace_geo": "us", "allowed_inference_geos": []any{"us"}, "default_inference_geo": "us"}}, createBody.Body)
	next := providertest.Props(updated)
	diff, err := server.Diff(p.DiffRequest{Urn: urn("Workspace"), ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: next})
	require.NoError(t, err)
	for _, d := range diff.DetailedDiff {
		require.NotContains(t, string(d.Kind), "replace")
	}
	updatedState, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: next})
	require.NoError(t, err)
	assert.Equal(t, fakeapi.Object{"name": "research-2", "display_color": "#6C5BB9", "tags": map[string]any{"team": "research"}, "data_residency": map[string]any{"allowed_inference_geos": "unrestricted", "default_inference_geo": "global"}}, api.LastRequest(t, http.MethodPost, "/v1/organizations/workspaces/"+created.ID).Body)
	require.NoError(t, server.Delete(p.DeleteRequest{Urn: urn("Workspace"), ID: created.ID, Properties: updatedState.Properties}))

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
	moved["dataResidency"] = map[string]any{"allowedInferenceGeos": []any{"us"}, "defaultInferenceGeo": "us"}
	renamed := baseWorkspace()
	renamed["name"] = "other"

	for name, tc := range map[string]struct {
		from   p.CreateResponse
		inputs property.Map
		to     map[string]any
		want   map[string]p.PropertyDiff
	}{
		"setting a key updates": {unkeyed, unkeyedInputs, keyedInputs, map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.Add}}},
		"changing a key replaces": {keyed, keyedChecked, rekeyed, map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.UpdateReplace, InputDiff: true}}},
		"removing a key replaces": {keyed, keyedChecked, baseWorkspace(), map[string]p.PropertyDiff{
			"externalKeyId": {Kind: p.DeleteReplace, InputDiff: true}}},
		"changing the geography replaces": {unkeyed, unkeyedInputs, moved, map[string]p.PropertyDiff{
			"dataResidency": {Kind: p.UpdateReplace, InputDiff: true}}},
		"renaming updates": {unkeyed, unkeyedInputs, renamed, map[string]p.PropertyDiff{
			"name": {Kind: p.Update}}},
		"no change": {unkeyed, unkeyedInputs, baseWorkspace(), map[string]p.PropertyDiff{}},
	} {
		t.Run(name, func(t *testing.T) {
			got := providertest.Diff(t, server, urn("Workspace"), tc.from, tc.inputs, providertest.Props(tc.to))
			require.Equal(t, len(tc.want) > 0, got.HasChanges)
			assert.Equal(t, tc.want, got.DetailedDiff)
		})
	}
}

// Independently specified synthetic SDK response.
func workspaceObservation(id string) fakeapi.Object {
	return fakeapi.Object{"id": id, "type": "workspace", "name": "observed", "display_color": "#123456", "tags": map[string]any{"keep": "yes", "outside": "preserve"}, "external_key_id": nil, "user_profile_id": nil, "created_at": "2026-01-01T00:00:00Z", "archived_at": nil, "compartment_id": "cmpt_synthetic", "data_residency": map[string]any{"workspace_geo": "us", "allowed_inference_geos": "unrestricted", "default_inference_geo": "global"}}
}

func workspaceReply(w http.ResponseWriter, obj fakeapi.Object) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(obj)
}

func TestWorkspaceCheckMetadataAndValidation(t *testing.T) {
	server, api := providertest.New(t)
	dep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::value")
	for _, v := range []property.Value{property.New("synthetic"), property.New(property.Computed), property.New("synthetic").WithSecret(true), property.New(property.Computed).WithSecret(true)} {
		v = v.WithDependencies([]resourceurn.URN{dep})
		bag := property.NewMap(map[string]property.Value{"name": v, "tags": property.New(property.NewMap(map[string]property.Value{"team": v})), "dataResidency": property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{dep})})
		before := bag
		checked, err := server.Check(p.CheckRequest{Urn: urn("Workspace"), Inputs: bag})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, bag, checked.Inputs)
		require.Equal(t, before, bag)
		again, err := server.Check(p.CheckRequest{Urn: urn("Workspace"), Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Equal(t, checked.Inputs, again.Inputs)
		preview, err := server.Create(p.CreateRequest{Urn: urn("Workspace"), Properties: bag, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, v, preview.Properties.Get("name"))
		require.True(t, preview.Properties.Get("compartmentId").IsComputed())
		update, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: "workspace_synthetic", State: providertest.Props(map[string]any{"name": "old", "createdAt": "2026-01-01T00:00:00Z", "compartmentId": "cmpt_synthetic"}), Inputs: bag, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, v, update.Properties.Get("name"))
	}
	for _, tc := range []struct {
		field string
		value any
		want  string
	}{
		{"name", "", "name"}, {"displayColor", "red", "displayColor"}, {"tags", map[string]any{"anthropicReserved": "x"}, "tags"},
		{"dataResidency", map[string]any{"allowedInferenceGeos": []any{}}, "dataResidency.allowedInferenceGeos"},
		{"dataResidency", map[string]any{"workspaceGeo": "eu"}, "dataResidency.workspaceGeo"},
		{"dataResidency", map[string]any{"defaultInferenceGeo": "moon"}, "dataResidency.defaultInferenceGeo"},
		{"dataResidency", map[string]any{"allowedInferenceGeos": []any{"us"}}, "dataResidency.defaultInferenceGeo"},
		{"dataResidency", map[string]any{"allowedInferenceGeos": []any{"moon"}}, "dataResidency.allowedInferenceGeos"},
	} {
		bag := map[string]any{"name": "valid"}
		bag[tc.field] = tc.value
		out, err := server.Check(p.CheckRequest{Urn: urn("Workspace"), Inputs: providertest.Props(bag)})
		require.NoError(t, err)
		found := false
		for _, f := range out.Failures {
			if f.Property == tc.want {
				found = true
			}
		}
		require.True(t, found, "%v: %v", tc, out.Failures)
	}
	// Long UUID probe names are known accepted; do not invent the old 40-char limit.
	out, err := server.Check(p.CheckRequest{Urn: urn("Workspace"), Inputs: providertest.Props(map[string]any{"name": strings.Repeat("x", 58), "dataResidency": map[string]any{"allowedInferenceGeos": []any{property.New(property.Computed)}, "defaultInferenceGeo": "us"}})})
	require.NoError(t, err)
	require.Empty(t, out.Failures)
	require.Empty(t, api.Requests())
}

func TestWorkspaceFullUpdateOwnershipAndResets(t *testing.T) {
	server, api := providertest.New(t)
	id := "workspace_synthetic"
	path := "/v1/organizations/workspaces/" + id
	observed := workspaceObservation(id)
	api.Handle("POST "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, observed) })
	// State includes foreign tags and defaults. Only old declared keys can be deleted.
	_, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: id})
	require.Error(t, err) // no fixture GET yet; cannot invent an import observation
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, observed) })
	state, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: id})
	require.NoError(t, err)
	require.Equal(t, "observed", state.Inputs.Get("name").AsString())
	old := providertest.Props(map[string]any{"name": "old", "displayColor": "#123456", "tags": map[string]any{"keep": "yes", "remove": "old"}, "dataResidency": map[string]any{"allowedInferenceGeos": []any{"us"}, "defaultInferenceGeo": "us"}})
	next := providertest.Props(map[string]any{"name": "desired", "tags": map[string]any{"keep": "yes"}})
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: id, State: state.Properties, OldInputs: old, Inputs: next})
	require.NoError(t, err)
	require.Len(t, api.Requests(), before+1, "Update must not refresh internally")
	require.Equal(t, fakeapi.Object{"name": "desired", "tags": map[string]any{"keep": "yes", "remove": nil}, "data_residency": map[string]any{"allowed_inference_geos": "unrestricted", "default_inference_geo": "global"}}, api.LastRequest(t, "POST", path).Body)
	require.Equal(t, "#123456", updated.Properties.Get("displayColor").AsString(), "removal does not clear color")
	// Full response observations win over requested names and expose readable drift.
	require.Equal(t, "observed", updated.Properties.Get("name").AsString())
	next = next.Set("name", property.New("observed"))
	diff, err := server.Diff(p.DiffRequest{Urn: urn("Workspace"), ID: id, State: updated.Properties, OldInputs: next, Inputs: next})
	require.NoError(t, err)
	require.False(t, diff.HasChanges, "unmanaged defaults/tags must not cause perpetual diff: %v", diff)
	require.Equal(t, "preserve", updated.Properties.Get("tags").AsMap().Get("outside").AsString())
}

func TestWorkspaceUnusableAllocationAndLegacyRefresh(t *testing.T) {
	server, api := providertest.New(t)
	id := "workspace_partial"
	path := "/v1/organizations/workspaces/" + id
	partial := workspaceObservation(id)
	delete(partial, "created_at")
	delete(partial, "compartment_id")
	partial["data_residency"] = map[string]any{"workspace_geo": "us", "allowed_inference_geos": 42, "default_inference_geo": "us"}
	partial["tags"] = map[string]any{"valid": "accepted", "invalid": 42}
	api.Handle("POST /v1/organizations/workspaces", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, partial) })
	inputs := providertest.Props(map[string]any{"name": "desired", "tags": map[string]any{"valid": "accepted", "invalid": "desired"}, "dataResidency": map[string]any{"allowedInferenceGeos": []any{"us"}, "defaultInferenceGeo": "us"}})
	created, err := server.Create(p.CreateRequest{Urn: urn("Workspace"), Properties: inputs})
	requireOrdinaryError(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Empty(t, created.Properties.AsMap())
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		workspaceReply(w, workspaceObservation(id))
	})
	refreshed, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: id, Properties: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "cmpt_synthetic", refreshed.Properties.Get("compartmentId").AsString())
	api.Handle("POST "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		workspaceReply(w, workspaceObservation(id))
	})
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: id, State: refreshed.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Len(t, api.Requests(), before+1)
	require.Equal(t, fakeapi.Object{"name": "desired", "tags": map[string]any{"valid": "accepted", "invalid": "desired"}, "data_residency": map[string]any{"allowed_inference_geos": []any{"us"}, "default_inference_geo": "us"}}, api.LastRequest(t, "POST", path).Body)
	require.Equal(t, "cmpt_synthetic", updated.Properties.Get("compartmentId").AsString())
}

func TestWorkspaceReadValidationAndIdentity(t *testing.T) {
	for _, field := range []string{"id", "archived_at", "data_residency", "tags", "default", "geo", "union"} {
		t.Run(field, func(t *testing.T) {
			server, api := providertest.New(t)
			obj := workspaceObservation("workspace_synthetic")
			switch field {
			case "id":
				obj["id"] = "workspace_foreign"
			case "archived_at":
				obj[field] = "not a timestamp"
			case "data_residency":
				obj[field] = 42
			case "tags":
				obj[field] = map[string]any{"x": 42}
			case "default":
				obj["data_residency"].(map[string]any)["default_inference_geo"] = "moon"
			case "geo":
				obj["data_residency"].(map[string]any)["workspace_geo"] = "moon"
			case "union":
				obj["data_residency"].(map[string]any)["allowed_inference_geos"] = map[string]any{}
			}
			api.Handle("GET /v1/organizations/workspaces/workspace_synthetic", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, obj) })
			_, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: "workspace_synthetic"})
			require.Error(t, err)
		})
	}
	server, api := providertest.New(t)
	path := "/v1/organizations/workspaces/workspace_synthetic"
	obj := workspaceObservation("workspace_synthetic")
	obj["user_profile_id"] = "profile_synthetic"
	status := 0
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		if status != 0 {
			fakeapi.WriteError(w, status, "synthetic")
			return
		}
		workspaceReply(w, obj)
	})
	api.Handle("POST "+path+"/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic")
	})
	imported, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: "workspace_synthetic"})
	require.NoError(t, err)
	require.Equal(t, "profile_synthetic", imported.Inputs.Get("userProfileId").AsString())
	partial, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: "workspace_synthetic", Properties: providertest.Props(map[string]any{"name": "old"}), Inputs: providertest.Props(map[string]any{"name": "old"})})
	require.NoError(t, err)
	require.True(t, partial.Inputs.Get("dataResidency").IsNull(), "partial managed Read is not import even without createdAt")
	require.True(t, partial.Properties.Get("dataResidency").IsMap())
	again, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: "workspace_synthetic"})
	require.NoError(t, err)
	require.Equal(t, imported, again)
	for _, code := range []int{403, 404, 503} {
		status = code
		out, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: "workspace_synthetic"})
		if status == 404 {
			require.NoError(t, err)
			require.Empty(t, out.ID)
		} else {
			require.Error(t, err)
		}
		err = server.Delete(p.DeleteRequest{Urn: urn("Workspace"), ID: "workspace_synthetic", Properties: imported.Properties})
		if status == 404 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}

func TestWorkspaceConservativeCreateRetry(t *testing.T) {
	server, api := providertest.New(t)
	var calls atomic.Int32
	api.Handle("POST /v1/organizations/workspaces", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		calls.Add(1) // simulated allocation committed before the response is lost
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	})
	_, err := server.Create(p.CreateRequest{Urn: urn("Workspace"), Properties: providertest.Props(map[string]any{"name": "ambiguous"})})
	require.Error(t, err)
	require.Equal(t, int32(1), calls.Load(), "non-idempotent allocation must not be retried after ambiguous transport loss")
}

func TestWorkspaceInvalidMutationResponsesAndIdentity(t *testing.T) {
	for _, mode := range []string{"omitted", "malformed", "foreign", "null"} {
		t.Run(mode, func(t *testing.T) {
			server, api := providertest.New(t)
			id := "workspace_synthetic"
			path := "/v1/organizations/workspaces/" + id
			obj := workspaceObservation(id)
			obj["external_key_id"] = "ekey_one"
			api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, obj) })
			prior, err := server.Read(p.ReadRequest{Urn: urn("Workspace"), ID: id})
			require.NoError(t, err)
			old := providertest.Props(map[string]any{"name": "observed", "tags": map[string]any{"keep": "yes"}, "dataResidency": map[string]any{"defaultInferenceGeo": "global"}, "externalKeyId": "ekey_one"})
			desired := providertest.Props(map[string]any{"name": "accepted", "displayColor": "#654321", "tags": map[string]any{"new": "accepted"}, "dataResidency": map[string]any{"allowedInferenceGeos": []any{"us"}, "defaultInferenceGeo": "us"}, "externalKeyId": "ekey_one"})
			response := workspaceObservation(id)
			response["external_key_id"] = "ekey_one"
			switch mode {
			case "omitted":
				delete(response, "name")
				delete(response, "display_color")
				delete(response, "tags")
				delete(response, "compartment_id")
				delete(response, "created_at")
				response["data_residency"] = map[string]any{"workspace_geo": "us"}
			case "malformed":
				response["name"] = 42
				response["display_color"] = 42
				response["tags"] = map[string]any{"new": 42, "outside": "observed"}
				response["data_residency"] = map[string]any{"workspace_geo": 42, "allowed_inference_geos": 42, "default_inference_geo": 42}
				response["created_at"] = 42
				response["compartment_id"] = 42
			case "foreign":
				response = workspaceObservation("workspace_foreign")
				response["name"] = "foreign"
				response["external_key_id"] = "ekey_foreign"
				response["compartment_id"] = "cmpt_foreign"
			case "null":
				response = nil
			}
			api.Handle("POST "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, response) })
			before := len(api.Requests())
			out, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: id, State: prior.Properties, OldInputs: old, Inputs: desired})
			requireOrdinaryError(t, err)
			require.Nil(t, out.PartialState)
			require.Empty(t, out.Properties.AsMap())
			require.Len(t, api.Requests(), before+1)
			require.Equal(t, fakeapi.Object{"name": "accepted", "display_color": "#654321", "tags": map[string]any{"keep": nil, "new": "accepted"}, "data_residency": map[string]any{"allowed_inference_geos": []any{"us"}, "default_inference_geo": "us"}}, api.LastRequest(t, "POST", path).Body)
			require.True(t, api.LastRequest(t, "POST", path).Body["external_key_id"] == nil, "already attached binding is not a mutable update field")
		})
	}
	// Nonstring identity cannot establish a new allocation, even if the SDK coerces it.
	server, api := providertest.New(t)
	obj := workspaceObservation("workspace_synthetic")
	obj["id"] = 42
	api.Handle("POST /v1/organizations/workspaces", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { workspaceReply(w, obj) })
	out, err := server.Create(p.CreateRequest{Urn: urn("Workspace"), Properties: providertest.Props(map[string]any{"name": "desired"})})
	require.Error(t, err)
	require.Empty(t, out.ID)
	require.Nil(t, out.PartialState)
}

func TestWorkspaceOutputAnnotationsAndBindingDiff(t *testing.T) {
	server, api := providertest.New(t)
	workspaceServerBehavior(api)
	dep := resourceurn.URN("urn:pulumi:test::test::test:index:Dependency::value")
	marked := func(s string) property.Value {
		return property.New(s).WithSecret(true).WithDependencies([]resourceurn.URN{dep})
	}
	inputs := providertest.Props(map[string]any{"name": marked("workspace"), "tags": map[string]any{"team": marked("synthetic")}, "externalKeyId": marked("ekey_one"), "userProfileId": marked("profile_one"), "dataResidency": map[string]any{"workspaceGeo": marked("us"), "defaultInferenceGeo": marked("us"), "allowedInferenceGeos": []any{marked("us")}}})
	created, checked := providertest.Create(t, server, urn("Workspace"), inputs)
	for k, v := range inputs.All {
		requireOutputValue(t, v, created.Properties.Get(k))
	}
	before := created.Properties
	// Removing metadata from an unchanged write-once ID is never replacement.
	next := checked.Set("externalKeyId", property.New("ekey_one")).Set("userProfileId", property.New("profile_one"))
	d, err := server.Diff(p.DiffRequest{Urn: urn("Workspace"), ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: next})
	require.NoError(t, err)
	for _, change := range d.DetailedDiff {
		require.NotContains(t, string(change.Kind), "replace")
	}
	require.Equal(t, before, created.Properties)
	require.Equal(t, inputs, checked)
	for _, key := range []string{"externalKeyId", "userProfileId"} {
		for _, v := range []property.Value{property.New("different"), property.Value{}, property.New(property.Computed)} {
			changed := checked.Set(key, v)
			d, err := server.Diff(p.DiffRequest{Urn: urn("Workspace"), ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: changed})
			require.NoError(t, err)
			require.Contains(t, string(d.DetailedDiff[key].Kind), "replace")
		}
	}
	refreshed := providertest.Read(t, server, urn("Workspace"), created, checked)
	requireOutputValue(t, inputs.Get("name"), refreshed.Properties.Get("name"))
	requireOutputValue(t, inputs.Get("tags"), refreshed.Properties.Get("tags"))
}

func TestWorkspaceWholeTagsRemovalAndBindingAttachment(t *testing.T) {
	server, api := providertest.New(t)
	workspaceServerBehavior(api)
	inputs := providertest.Props(map[string]any{"name": "workspace", "tags": map[string]any{"a": "one", "b": "two"}})
	created, old := providertest.Create(t, server, urn("Workspace"), inputs)
	next := providertest.Props(map[string]any{"name": "workspace", "externalKeyId": "ekey_one", "userProfileId": "profile_one"})
	_, err := server.Update(p.UpdateRequest{Urn: urn("Workspace"), ID: created.ID, State: created.Properties, OldInputs: old, Inputs: next})
	require.NoError(t, err)
	require.Equal(t, fakeapi.Object{"name": "workspace", "tags": map[string]any{"a": nil, "b": nil}, "external_key_id": "ekey_one", "user_profile_id": "profile_one"}, api.LastRequest(t, "POST", "/v1/organizations/workspaces/"+created.ID).Body)
}

func TestWorkspaceReadProtectsInputOnlySecrets(t *testing.T) {
	s, api := providertest.New(t)
	workspaceServerBehavior(api)
	u := providertest.URN("anthropic:organization:Workspace")
	created, inputs := providertest.Create(t, s, u, providertest.Props(baseWorkspace()))
	inputs = inputs.Set("name", inputs.Get("name").WithSecret(true).WithDependencies([]resourceurn.URN{"urn:input"}))
	for _, state := range []property.Map{{}, created.Properties} {
		read, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: inputs, Properties: state})
		require.NoError(t, err)
		require.Equal(t, inputs.Get("name").AsString(), read.Properties.Get("name").AsString())
		require.True(t, read.Properties.Get("name").Secret(), "public/missing old state must not expose a secret input")
		require.True(t, read.Inputs.Get("name").Secret())
		requireNoOutputDependencies(t, property.New(read.Properties))
	}
	require.True(t, inputs.Get("name").Secret())
	require.False(t, created.Properties.Get("name").Secret(), "Read must not mutate old output state")
}
