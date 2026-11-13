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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const membersPath = "/v1/organizations/workspaces/wrkspc_1/members"

func memberServerBehavior(api *fakeapi.Server) {
	api.SetHooks("members", fakeapi.Hooks{KeyField: "user_id", OnCreate: func(obj fakeapi.Object) { obj["type"] = "workspace_member"; obj["workspace_id"] = "wrkspc_1" }})
}

func member(role string) map[string]any {
	return map[string]any{"workspaceId": "wrkspc_1", "userId": "user_1", "workspaceRole": role}
}

func TestWorkspaceMemberLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memberServerBehavior(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:WorkspaceMember",
		Create: integration.Operation{
			Inputs: providertest.Props(member("workspace_developer")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, membersPath)
				assert.Equal(t, fakeapi.Object{"user_id": "user_1", "workspace_role": "workspace_developer"}, req.Body)
			},
		},
		Updates: []integration.Operation{
			{
				Inputs: providertest.Props(member("workspace_admin")),
				Hook: func(_, output property.Map) {
					req := api.LastRequest(t, http.MethodPost, membersPath+"/user_1")
					assert.Equal(t, fakeapi.Object{"workspace_role": "workspace_admin"}, req.Body)
				},
			},
			{
				// Moving the membership to another user replaces it.
				Inputs: providertest.Props(map[string]any{
					"workspaceId": "wrkspc_1", "userId": "user_2", "workspaceRole": "workspace_admin"}),
			},
		},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, membersPath+"/user_1")
	api.LastRequest(t, http.MethodDelete, membersPath+"/user_2")
}

func TestWorkspaceMemberBilling(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memberServerBehavior(api)

	created, _ := providertest.Create(t, server, urn("WorkspaceMember"), providertest.Props(member("workspace_billing")))
	assert.Equal(t, "wrkspc_1/user_1", created.ID)

	// The API accepts workspace_billing only on update, so the member is added as a
	// workspace_user and then granted it.
	add := api.LastRequest(t, http.MethodPost, membersPath)
	assert.Equal(t, "workspace_user", add.Body["workspace_role"])
	grant := api.LastRequest(t, http.MethodPost, membersPath+"/user_1")
	assert.Equal(t, fakeapi.Object{"workspace_role": "workspace_billing"}, grant.Body)
	assert.Equal(t, "workspace_billing", api.Object(membersPath + "/user_1")["workspace_role"])
}

func TestWorkspaceMemberRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memberServerBehavior(api)

	created, checked := providertest.Create(t, server, urn("WorkspaceMember"), providertest.Props(member("workspace_user")))
	resp := providertest.Read(t, server, urn("WorkspaceMember"), created, checked)
	assert.Equal(t, checked, resp.Inputs)

	api.Mutate(membersPath+"/user_1", func(obj fakeapi.Object) { obj["workspace_role"] = "workspace_admin" })
	resp = providertest.Read(t, server, urn("WorkspaceMember"), created, checked)
	assert.Equal(t, "workspace_admin", resp.Inputs.Get("workspaceRole").AsString())

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: urn("WorkspaceMember")})
	require.NoError(t, err)
	assert.Equal(t, providertest.Props(member("workspace_admin")), imported.Inputs,
		"an import learns the workspace and user from the ID")

	err = server.Delete(p.DeleteRequest{ID: created.ID, Urn: urn("WorkspaceMember"), Properties: created.Properties})
	require.NoError(t, err)
	resp = providertest.Read(t, server, urn("WorkspaceMember"), created, checked)
	assert.Empty(t, resp.ID, "a removed member reads as deleted")
}

func workspaceMemberInputs(role string) property.Map {
	return providertest.Props(map[string]any{"workspaceId": "wrkspc_test", "userId": "user_test", "workspaceRole": role})
}

const workspaceMemberPath = "/v1/organizations/workspaces/wrkspc_test/members/user_test"

// Contract: preview must preserve unknowns without contacting the API.
func TestWorkspaceMemberPreview(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:WorkspaceMember")
	unknown := property.NewMap(map[string]property.Value{
		"workspaceId": property.New(property.Computed), "userId": property.New(property.Computed), "workspaceRole": property.New(property.Computed),
	})
	check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: unknown})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	require.Equal(t, unknown, check.Inputs)
	preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: check.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, preview.Properties.Get("workspaceId").IsComputed())
	require.True(t, preview.Properties.Get("userId").IsComputed())
	require.True(t, preview.Properties.Get("workspaceRole").IsComputed())
	known := workspaceMemberInputs("workspace_user")
	diff, err := server.Diff(p.DiffRequest{Urn: urn, ID: "wrkspc_test/user_test", State: known, OldInputs: known, Inputs: unknown})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	update, err := server.Update(p.UpdateRequest{Urn: urn, ID: "wrkspc_test/user_test", State: known, OldInputs: known, Inputs: unknown, DryRun: true})
	require.NoError(t, err)
	require.True(t, update.Properties.Get("workspaceRole").IsComputed())
	require.Empty(t, api.Requests())
}

// Contract: identity is the ID pair, not an API-generated id; imported state is immediately usable.
func TestWorkspaceMemberImportUpdate(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:WorkspaceMember")
	// This is the SDK response shape: deliberately no synthetic `id` field.
	api.Put(workspaceMemberPath, fakeapi.Object{"type": "workspace_member", "workspace_id": "wrkspc_test", "user_id": "user_test", "workspace_role": "workspace_restricted_developer"})
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_test/user_test"})
	require.NoError(t, err)
	require.Equal(t, workspaceMemberInputs("workspace_restricted_developer"), imported.Inputs)
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	diff, err := server.Diff(p.DiffRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, before+1, len(api.Requests()), "Update sends the complete desired role without an internal GET")
	require.Equal(t, imported.Properties, updated.Properties)
	for _, key := range []string{"workspaceId", "userId"} {
		changed := imported.Inputs.AsMap()
		changed[key] = property.New("other")
		d, err := server.Diff(p.DiffRequest{Urn: urn, ID: imported.ID, State: imported.Properties, Inputs: property.NewMap(changed)})
		require.NoError(t, err)
		require.Equal(t, p.UpdateReplace, d.DetailedDiff[key].Kind)
		require.False(t, d.DeleteBeforeReplace, "a different identity does not collide")
	}
}

// A failed promotion can leave a workspace_user allocation, without a checkpoint.
func TestWorkspaceMemberBillingPromotionFailure(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:WorkspaceMember")
	fail := true
	api.SetHooks("members", fakeapi.Hooks{KeyField: "user_id", OnCreate: func(obj fakeapi.Object) { obj["type"] = "workspace_member"; obj["workspace_id"] = "wrkspc_test" }, OnUpdate: func(before, after, body fakeapi.Object) error {
		if fail {
			return &fakeapi.Error{Status: http.StatusBadRequest, Message: "injected promotion failure"}
		}
		return nil
	}})
	inputs := workspaceMemberInputs("workspace_billing")
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs})
	requireOrdinaryError(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Empty(t, created.Properties.AsMap())
	require.Len(t, api.Requests(), 2, "Add then one promotion attempt")
	fail = false
	refreshed, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_test/user_test", Properties: inputs.Delete("workspaceRole"), Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "workspace_user", refreshed.Properties.Get("workspaceRole").AsString(), "the final billing role was not accepted")
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: refreshed.ID, State: refreshed.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "workspace_billing", updated.Properties.Get("workspaceRole").AsString())
	require.Len(t, api.Requests(), before+1, "Update never reads internally")
	require.Equal(t, fakeapi.Object{"workspace_role": "workspace_billing"}, api.LastRequest(t, http.MethodPost, workspaceMemberPath).Body)
	adds := 0
	for _, r := range api.Requests() {
		if r.Method == http.MethodPost && r.Path == "/v1/organizations/workspaces/wrkspc_test/members" {
			adds++
		}
	}
	require.Equal(t, 1, adds, "explicitly adopting the allocation must not Add a second membership")
}

// Contract: authorization failures are errors, not absence; deletion uses physical ID and tolerates absence.
func TestWorkspaceMemberDeleteAndReadErrors(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:WorkspaceMember")
	api.Handle("GET "+workspaceMemberPath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusForbidden, "injected forbidden")
	})
	_, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_test/user_test"})
	require.Error(t, err)
	for range 2 {
		err := server.Delete(p.DeleteRequest{Urn: urn, ID: "wrkspc_test/user_test", Properties: workspaceMemberInputs("workspace_user")})
		require.NoError(t, err)
	}
	api.LastRequest(t, http.MethodDelete, workspaceMemberPath)
	_, err = server.Read(p.ReadRequest{Urn: urn, ID: "malformed"})
	require.Error(t, err)
}

func TestWorkspaceMemberCheckMetadata(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN("anthropic:organization:WorkspaceMember")
	for _, unknown := range []bool{false, true} {
		role := property.New("workspace_user")
		if unknown {
			role = property.New(property.Computed)
		}
		role = role.WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:m:T::dependency"})
		inputs := providertest.Props(member("workspace_user")).Set("workspaceRole", role)
		snapshot := inputs
		for range 2 {
			checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
			inputs = checked.Inputs
		}
		require.Equal(t, snapshot, inputs)
		preview, err := server.Create(p.CreateRequest{Urn: u, Properties: inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, property.New(inputs), property.New(preview.Properties))
		if !unknown {
			memberServerBehavior(api)
			created, err := server.Create(p.CreateRequest{Urn: u, Properties: inputs})
			require.NoError(t, err)
			requireOutputValue(t, role, created.Properties.Get("workspaceRole"))
			before := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: created.Properties, Inputs: inputs, OldInputs: inputs})
			require.NoError(t, err)
			require.Equal(t, before+1, len(api.Requests()))
			requireOutputValue(t, role, updated.Properties.Get("workspaceRole"))
			require.Equal(t, snapshot, inputs)
		}
	}
	for k, v := range map[string]string{"workspaceId": "", "userId": "x/y", "workspaceRole": "invalid"} {
		inputs := providertest.Props(member("workspace_user")).Set(k, property.New(v).WithSecret(true))
		checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
		require.Equal(t, k, checked.Failures[0].Property)
	}
}

func TestWorkspaceMemberMalformedResponses(t *testing.T) {
	for _, body := range []string{
		`{"type":"workspace_member","workspace_id":"foreign","user_id":"user_1","workspace_role":"workspace_admin"}`,
		`{"type":"workspace_member","workspace_id":"wrkspc_1","user_id":"user_1","workspace_role":"future_role"}`,
		`{"type":"workspace_member","workspace_id":"wrkspc_1","user_id":"user_1"}`,
		`{"type":"wrong","workspace_id":"wrkspc_1","user_id":"user_1","workspace_role":"workspace_admin"}`,
		`{"type":"workspace_member","workspace_id":"wrkspc_1","user_id":"user_1","workspace_role":42}`,
	} {
		t.Run(body, func(t *testing.T) {
			server, api := providertest.New(t)
			u := providertest.URN("anthropic:organization:WorkspaceMember")
			handler := func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(body)); err != nil {
					t.Error(err)
				}
			}
			api.Handle("POST "+membersPath, handler)
			api.Handle("GET "+membersPath+"/user_1", handler)
			inputs := providertest.Props(member("workspace_user"))
			created, err := server.Create(p.CreateRequest{Urn: u, Properties: inputs})
			requireOrdinaryError(t, err)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Empty(t, created.Properties.AsMap())
			_, err = server.Read(p.ReadRequest{Urn: u, ID: "wrkspc_1/user_1"})
			require.Error(t, err)
			api.Handle("POST "+membersPath+"/user_1", handler)
			before := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: u, ID: "wrkspc_1/user_1", State: inputs, Inputs: inputs, OldInputs: inputs})
			requireOrdinaryError(t, err)
			require.Nil(t, updated.PartialState)
			require.Empty(t, updated.Properties.AsMap())
			require.Equal(t, before+1, len(api.Requests()))
		})
	}
}
