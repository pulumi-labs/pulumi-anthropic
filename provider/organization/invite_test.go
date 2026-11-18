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
	"fmt"
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

func inviteServerBehavior(api *fakeapi.Server) {
	api.SetHooks("invites", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["status"] = "pending"
		obj["invited_at"] = "2026-09-25T00:00:00Z"
		obj["expires_at"] = "2026-10-16T00:00:00Z"
		obj["rbac_group_ids"] = []any{}
	}})
}

func TestInviteLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	inviteServerBehavior(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:Invite",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"email": "ada@example.com", "role": "developer"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "pending", output.Get("status").AsString())
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/invites")
				assert.Equal(t, fakeapi.Object{"email": "ada@example.com", "role": "developer"}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			// Invitations cannot be changed, so a new role means a new invitation.
			Inputs: providertest.Props(map[string]any{"email": "ada@example.com", "role": "billing"}),
		}},
	}.Run(t, server)

	deletes := 0
	for _, req := range api.Requests() {
		if req.Method == http.MethodDelete {
			assert.Regexp(t, `^/v1/organizations/invites/invite_\d+$`, req.Path)
			deletes++
		}
	}
	assert.Equal(t, 2, deletes, "the replaced and the final invitation are both deleted")
}

func TestInviteRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	inviteServerBehavior(api)

	created, checked := providertest.Create(t, server, urn("Invite"), providertest.Props(map[string]any{"email": "ada@example.com", "role": "user"}))
	resp := providertest.Read(t, server, urn("Invite"), created, checked)
	assert.Equal(t, checked, resp.Inputs, "server-filled fields are not drift")

	api.Mutate("/v1/organizations/invites/"+created.ID, func(obj fakeapi.Object) { obj["role"] = "developer" })
	resp = providertest.Read(t, server, urn("Invite"), created, checked)
	assert.Equal(t, "developer", resp.Inputs.Get("role").AsString())
}

// Scalars and whole collections survive previews without HTTP.
func TestInviteUnknownPreview(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:Invite")
	inputs := property.NewMap(map[string]property.Value{
		"email": property.New(property.Computed), "role": property.New(property.Computed),
		"rbacGroupIds": property.New(property.Computed),
	})
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("email").IsComputed())
	require.True(t, checked.Inputs.Get("role").IsComputed())
	require.True(t, checked.Inputs.Get("rbacGroupIds").IsComputed())
	preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("email").IsComputed())
	require.True(t, preview.Properties.Get("role").IsComputed())
	require.True(t, preview.Properties.Get("status").IsComputed())
	require.Empty(t, api.Requests())

	inviteServerBehavior(api)
	created, old := providertest.Create(t, server, urn, providertest.Props(map[string]any{"email": "preview@example.com", "role": "user"}))
	before := len(api.Requests())
	diff, err := server.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.UpdateReplace, diff.DetailedDiff["email"].Kind)
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("email").IsComputed())
	require.Len(t, api.Requests(), before)
}

// Contract INV-E2 / provider policy: ID-only recovery, no-op reapply and defaults.
func TestInviteImportStableAndStatus(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:Invite")
	path := "/v1/organizations/invites/invite_import"
	api.Put(path, fakeapi.Object{"id": "invite_import", "type": "invite", "email": "import@example.com", "role": "user", "status": "pending", "rbac_group_ids": []any{}, "invited_at": "2026-01-01T00:00:00Z", "expires_at": "2026-01-22T00:00:00Z", "accepted_at": nil})
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: "invite_import"})
	require.NoError(t, err)
	require.Equal(t, "invite_import", imported.ID)
	require.Equal(t, "import@example.com", imported.Inputs.Get("email").AsString())
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	created := p.CreateResponse{ID: imported.ID, Properties: imported.Properties}
	require.False(t, providertest.Diff(t, server, urn, created, imported.Inputs, checked.Inputs).HasChanges)
	// Empty and omitted groups denote no memberships in this provider policy.
	omitted := providertest.Props(map[string]any{"email": "import@example.com", "role": "user"})
	require.False(t, providertest.Diff(t, server, urn, created, imported.Inputs, omitted).HasChanges)
	before := len(api.Requests())
	unchanged, err := server.Update(p.UpdateRequest{Urn: urn, ID: imported.ID, State: imported.Properties, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, imported.Properties, unchanged.Properties)
	require.Len(t, api.Requests(), before)
	for _, status := range []string{"accepted", "expired", "deleted"} {
		api.Mutate(path, func(obj fakeapi.Object) { obj["status"] = status })
		refreshed := providertest.Read(t, server, urn, created, imported.Inputs)
		require.Equal(t, imported.ID, refreshed.ID, "characterization: terminal status alone is not absence")
		require.Equal(t, status, refreshed.Properties.Get("status").AsString())
		require.False(t, providertest.Diff(t, server, urn, p.CreateResponse{ID: refreshed.ID, Properties: refreshed.Properties}, refreshed.Inputs, checked.Inputs).HasChanges)
	}
}

// Immutable changes delete first, avoiding a duplicate pending invitation.
// Duplicate rejection below models SDK's documented InviteAlreadyExists error.
func TestInviteReplacementCollision(t *testing.T) {
	server, api := providertest.New(t)
	inviteServerBehavior(api)
	urn := providertest.URN("anthropic:organization:Invite")
	created, old := providertest.Create(t, server, urn, providertest.Props(map[string]any{"email": "same@example.com", "role": "user"}))
	for _, desired := range []map[string]any{
		{"email": "other@example.com", "role": "user"},
		{"email": "same@example.com", "role": "developer"},
		{"email": "same@example.com", "role": "user", "rbacGroupIds": []any{"group_a"}},
	} {
		diff := providertest.Diff(t, server, urn, created, old, providertest.Props(desired))
		require.True(t, diff.HasChanges)
		require.True(t, diff.DeleteBeforeReplace)
		for _, change := range diff.DetailedDiff {
			require.Contains(t, []p.DiffKind{p.AddReplace, p.UpdateReplace, p.DeleteReplace}, change.Kind)
		}
	}
	require.Equal(t, fakeapi.Object{"email": "same@example.com", "role": "user"}, api.LastRequest(t, http.MethodPost, "/v1/organizations/invites").Body)
	api.Handle("POST /v1/organizations/invites", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(fakeapi.Object{"type": "error", "error": fakeapi.Object{"type": "invalid_request_error", "message": "invite_already_exists"}})
	})
	_, err := server.Create(p.CreateRequest{Urn: urn, Properties: providertest.Props(map[string]any{"email": "same@example.com", "role": "developer"})})
	require.ErrorContains(t, err, "invite_already_exists")
	require.NotNil(t, api.Object("/v1/organizations/invites/"+created.ID))
}

// Contract INV-E4: exact delete identity, repeated absence, and permission errors.
func TestInviteDeleteAndReadErrors(t *testing.T) {
	server, api := providertest.New(t)
	inviteServerBehavior(api)
	urn := providertest.URN("anthropic:organization:Invite")
	created, inputs := providertest.Create(t, server, urn, providertest.Props(map[string]any{"email": "delete@example.com", "role": "user", "rbacGroupIds": []any{"group_a"}}))
	require.Equal(t, fakeapi.Object{"email": "delete@example.com", "role": "user", "rbac_group_ids": []any{"group_a"}}, api.LastRequest(t, http.MethodPost, "/v1/organizations/invites").Body)
	for range 2 {
		err := server.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: created.Properties})
		require.NoError(t, err)
	}
	require.Empty(t, providertest.Read(t, server, urn, created, inputs).ID)
	api.LastRequest(t, http.MethodDelete, "/v1/organizations/invites/"+created.ID)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		api.Handle(method+" /v1/organizations/invites/invite_denied", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"denied"}}`))
		})
	}
	_, err := server.Read(p.ReadRequest{Urn: urn, ID: "invite_denied"})
	require.Error(t, err)
	err = server.Delete(p.DeleteRequest{Urn: urn, ID: "invite_denied", Properties: created.Properties})
	require.Error(t, err)
}

func TestInviteUnusableCreateResponse(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:Invite")
	api.Handle("POST /v1/organizations/invites", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"invite_partial","type":"invite","email":"partial@example.invalid","role":"user","rbac_group_ids":[],"status":42,"invited_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-22T00:00:00Z"}`))
	})
	inputs := providertest.Props(map[string]any{"email": "partial@example.invalid", "role": "user"})
	out, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	requireOrdinaryError(t, err)
	require.Nil(t, out.PartialState)
	require.Empty(t, out.ID)
	require.Empty(t, out.Properties.AsMap())
	require.Len(t, api.Requests(), 1, "an unusable response must not send another invitation")
}

func TestInviteLegacyIncompleteRefreshAndImmutableUpdate(t *testing.T) {
	server, api := providertest.New(t)
	u := urn("Invite")
	inputs := providertest.Props(map[string]any{"email": "legacy@example.invalid", "role": "user"})
	api.Handle("GET /v1/organizations/invites/invite_legacy", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "invite_legacy", "type": "invite", "email": "legacy@example.invalid", "role": "user", "rbac_group_ids": []any{}, "status": "accepted", "invited_at": "2026-01-01T00:00:00Z", "expires_at": "2026-01-22T00:00:00Z"})
	})
	refreshed, err := server.Read(p.ReadRequest{Urn: u, ID: "invite_legacy", Properties: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "accepted", refreshed.Properties.Get("status").AsString())
	require.Equal(t, "2026-01-01T00:00:00Z", refreshed.Properties.Get("invitedAt").AsString())
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: refreshed.ID, State: refreshed.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Equal(t, refreshed.Properties, updated.Properties)
	require.Len(t, api.Requests(), before, "immutable Update must not resend or read the invitation")
}

func TestInviteCheckMetadataAndValidation(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:Invite")
	inputs := providertest.Props(map[string]any{"email": "metadata@example.invalid", "role": "user"}).Set("rbacGroupIds", property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:dependency"}))
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, inputs, checked.Inputs)
	again, err := server.Check(p.CheckRequest{Urn: urn, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, checked.Inputs, again.Inputs)
	for _, bad := range []property.Map{inputs.Set("email", property.New("not an address")), inputs.Set("role", property.New("owner"))} {
		c, err := server.Check(p.CheckRequest{Urn: urn, Inputs: bad})
		require.NoError(t, err)
		require.NotEmpty(t, c.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestInviteTerminalDeleteForgetsOnly(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:organization:Invite")
	for _, status := range []string{"accepted", "expired", "deleted"} {
		err := server.Delete(p.DeleteRequest{Urn: urn, ID: "invite_terminal", Properties: providertest.Props(map[string]any{"email": "terminal@example.invalid", "role": "user", "status": status, "invitedAt": "2026-01-01T00:00:00Z", "expiresAt": "2026-01-22T00:00:00Z"})})
		require.NoError(t, err)
	}
	require.Empty(t, api.Requests())
}

func TestInviteObservedGroupMetadata(t *testing.T) {
	server, api := providertest.New(t)
	inviteServerBehavior(api)
	api.SetHooks("invites", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["status"] = "pending"
		obj["invited_at"] = "2026-01-01T00:00:00Z"
		obj["expires_at"] = "2026-01-22T00:00:00Z"
		obj["rbac_group_ids"] = []any{"group_b", "group_a"}
	}})
	urn := providertest.URN("anthropic:organization:Invite")
	a := property.New("group_a").WithSecret(true).WithDependencies([]resourceurn.URN{"urn:dep:a"})
	b := property.New("group_b").WithDependencies([]resourceurn.URN{"urn:dep:b"})
	inputs := providertest.Props(map[string]any{"email": "metadata@example.invalid", "role": "user"}).Set("rbacGroupIds", property.New([]property.Value{a, b}))
	snapshot := inputs
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	require.NoError(t, err)
	require.Nil(t, created.PartialState)
	assertGroups := func(m property.Map, want ...property.Value) {
		requireOutputValue(t, property.New(want), m.Get("rbacGroupIds"))
	}
	assertGroups(created.Properties, b, a)
	require.Equal(t, snapshot, inputs)
	path := "/v1/organizations/invites/" + created.ID
	// Move the IDs again during refresh. Secret ancestors may protect the whole
	// collection, but graph dependencies must never flow to resource outputs.
	api.Mutate(path, func(obj fakeapi.Object) { obj["rbac_group_ids"] = []any{"group_a", "group_b"} })
	// Exercise a legacy leaf-secret state, without infer's Create-time ancestor
	// secret. The formerly secret group moves to a previously public index.
	legacy := created.Properties.Set("rbacGroupIds", property.New([]property.Value{b, a}))
	read, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Inputs: inputs, Properties: legacy})
	require.NoError(t, err)
	assertGroups(read.Inputs, a, b)
	assertGroups(read.Properties, a, b)
	assertGroups(created.Properties, b, a)
	require.Equal(t, snapshot, inputs)
}

func TestInviteRejectsCoercedResponseStrings(t *testing.T) {
	for _, field := range []string{"email", "rbac_group_ids"} {
		for _, invalid := range []string{"123", "true", "null"} {
			t.Run(field+"/"+invalid, func(t *testing.T) {
				server, api := providertest.New(t)
				urn := providertest.URN("anthropic:organization:Invite")
				email, groups := `"observed@example.invalid"`, `["group_a"]`
				if field == "email" {
					email = invalid
				} else {
					groups = "[" + invalid + "]"
				}
				response := fmt.Sprintf(`{"id":"invite_invalid","type":"invite","email":%s,"role":"user","rbac_group_ids":%s,"status":"pending","invited_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-22T00:00:00Z"}`, email, groups)
				handler := func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(response))
				}
				api.Handle("POST /v1/organizations/invites", handler)
				api.Handle("GET /v1/organizations/invites/invite_invalid", handler)
				inputs := providertest.Props(map[string]any{"email": "accepted@example.invalid", "role": "user", "rbacGroupIds": []any{"group_desired"}})
				created, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
				requireOrdinaryError(t, err)
				require.Nil(t, created.PartialState)
				require.Empty(t, created.ID)
				require.Empty(t, created.Properties.AsMap())
				_, err = server.Read(p.ReadRequest{Urn: urn, ID: "invite_invalid", Inputs: inputs, Properties: inputs})
				require.Error(t, err)
				_, err = server.Read(p.ReadRequest{Urn: urn, ID: "invite_invalid"})
				require.Error(t, err)
			})
		}
	}
}
