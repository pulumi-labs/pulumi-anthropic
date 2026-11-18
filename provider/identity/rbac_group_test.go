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

package identity_test

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

const (
	rbacGroupToken       = "anthropic:identity:RbacGroup"
	rbacGroupMemberToken = "anthropic:identity:RbacGroupMember"
)

func TestRbacGroupLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.SetHooks("rbac_groups", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["source_type"] = "direct"
		obj["role_ids"] = []any{"rbac_role_1"}
	}})

	integration.LifeCycleTest{
		Resource: rbacGroupToken,
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"name": "Platform"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/rbac_groups")
				assert.Equal(t, fakeapi.Object{"name": "Platform"}, req.Body)
				assert.Equal(t, "sk-ant-admin-test", req.Header.Get("x-api-key"), "RBAC takes the Admin key")
				assert.Equal(t, "direct", output.Get("sourceType").AsString())
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"name": "Platform Engineering"}),
			Hook: func(_, _ property.Map) {
				reqs := api.Requests()
				assert.Equal(t, fakeapi.Object{"name": "Platform Engineering"}, reqs[len(reqs)-1].Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodDelete, last.Method, "deleting a group deletes it")
	assert.Regexp(t, `^/v1/organizations/rbac_groups/rbac_group_\d+$`, last.Path)
}

// memberListServerBehavior seeds a group and serves its member list in two pages,
// paginated with the SDK's next_page cursor.
func memberListServerBehavior(api *fakeapi.Server) {
	const group = "/v1/organizations/rbac_groups/rbac_group_1"
	api.Put(group, fakeapi.Object{"id": "rbac_group_1", "name": "Platform"})
	api.SetHooks("members", fakeapi.Hooks{KeyField: "user_id", OnCreate: func(obj fakeapi.Object) {
		obj["email"] = "ada@example.com"
		obj["rbac_group_id"] = "rbac_group_1"
		obj["type"] = "rbac_group_member"
	}})
	api.Handle("GET "+group+"/members", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		first := fakeapi.Object{"data": []any{fakeapi.Object{"user_id": "user_other", "rbac_group_id": "rbac_group_1", "type": "rbac_group_member", "email": "x@example.com"}}}
		var second []any
		if m := api.Object(group + "/members/user_1"); m != nil {
			second = append(second, m)
		}
		q := r.URL.Query()
		switch {
		case q.Get("page") == "":
			first["next_page"] = "cursor-2"
			fakeapi.WriteJSON(w, first)
		default:
			fakeapi.WriteJSON(w, fakeapi.Object{"data": second, "has_more": false, "next_page": nil})
		}
	})
}

func TestRbacGroupMemberLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memberListServerBehavior(api)

	member := map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"}
	integration.LifeCycleTest{
		Resource: rbacGroupMemberToken,
		Create: integration.Operation{
			Inputs: providertest.Props(member),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/rbac_groups/rbac_group_1/members")
				assert.Equal(t, fakeapi.Object{"user_id": "user_1"}, req.Body)
				assert.Equal(t, "ada@example.com", output.Get("email").AsString())
			},
		},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, "/v1/organizations/rbac_groups/rbac_group_1/members/user_1")
}

func TestRbacGroupMemberRead(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"next_page"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, api := providertest.New(t)
			memberListServerBehavior(api)

			created, checked := providertest.Create(t, server, providertest.URN(rbacGroupMemberToken), providertest.Props(map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"}))
			assert.Equal(t, "rbac_group_1/user_1", created.ID)
			read := providertest.Read(t, server, providertest.URN(rbacGroupMemberToken), created, checked)
			assert.Equal(t, created.ID, read.ID, "the member is found on the second page")
			assert.Equal(t, checked, read.Inputs)

			imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: providertest.URN(rbacGroupMemberToken)})
			require.NoError(t, err)
			assert.Equal(t, checked, imported.Inputs)

			api.Mutate("/v1/organizations/rbac_groups/rbac_group_1/members/user_1", func(obj fakeapi.Object) {
				obj["user_id"] = "user_removed"
			})
			assert.Empty(t, providertest.Read(t, server, providertest.URN(rbacGroupMemberToken), created, checked).ID,
				"a member removed outside of Pulumi reads as gone")
		})
	}
}

// ID-only Read recovers inputs accepted by Check. An unchanged Diff converges,
// while an explicitly invoked Update still sends the full desired mutable state.
func TestRbacGroupImportAndFullUpdate(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroup")
	const path = "/v1/organizations/rbac_groups/group_existing"
	api.Put(path, fakeapi.Object{"id": "group_existing", "name": "Platform", "source_type": "direct", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "role_ids": []any{"role_1"}})
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: "group_existing"})
	require.NoError(t, err)
	require.Equal(t, "group_existing", imported.ID)
	require.Equal(t, "Platform", imported.Inputs.Get("name").AsString())
	require.Equal(t, "role_1", imported.Properties.Get("roleIds").AsArray().Get(0).AsString())
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, n+1, len(api.Requests()), "Update sends desired mutable state without GET")
	require.Equal(t, "Platform", updated.Properties.Get("name").AsString())
}

// Contract RG-E2: unknown name survives Check/Create/Update previews without I/O.
// The empty-to-unknown Diff case is also covered in TestRbacGroupMetadataAndUnknownDiff.
func TestRbacGroupUnknownPreview(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroup")
	inputs := property.NewMap(map[string]property.Value{"name": property.New(property.Computed)})
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("name").IsComputed())
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, created.ID)
	require.True(t, created.Properties.Get("name").IsComputed())
	old := providertest.Props(map[string]any{"name": "Platform", "sourceType": "direct", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: "group_1", State: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.Update, diff.DetailedDiff["name"].Kind)
	require.Equal(t, inputs, checked.Inputs, "Check does not mutate the caller's bag")
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: "group_1", State: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("name").IsComputed())
	require.Empty(t, api.Requests())
}

// Contract RG-E3: mutable name drift is visible; rename is an in-place POST and
// refreshed state converges. Fake hooks are not server evidence.
func TestRbacGroupDriftAndRename(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroup")
	api.SetHooks("rbac_groups", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) { obj["source_type"] = "direct"; obj["role_ids"] = []any{} }})
	created, inputs := providertest.Create(t, s, urn, providertest.Props(map[string]any{"name": "Platform"}))
	path := "/v1/organizations/rbac_groups/" + created.ID
	api.Mutate(path, func(obj fakeapi.Object) { obj["name"] = "Out of band"; obj["role_ids"] = []any{"role_external"} })
	read := providertest.Read(t, s, urn, created, inputs)
	require.Equal(t, "Out of band", read.Inputs.Get("name").AsString())
	require.Equal(t, "role_external", read.Properties.Get("roleIds").AsArray().Get(0).AsString())
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, p.Update, diff.DetailedDiff["name"].Kind)
	require.False(t, diff.DeleteBeforeReplace)
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, err)
	req := api.LastRequest(t, http.MethodPost, path)
	require.Equal(t, fakeapi.Object{"name": "Platform"}, req.Body)
	require.Equal(t, "Platform", api.Object(path)["name"])
	n := len(api.Requests())
	_, err = s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: updated.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, n+1, len(api.Requests()))
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: created.ID, Properties: updated.Properties, Inputs: inputs})
	require.NoError(t, err)
	diff, err = s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: refreshed.Properties, OldInputs: refreshed.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
}

// Contract RG-E4: authorization/SCIM/owner-protection failures must not become
// absence or successful deletion. The fake's 400/403 are fault-injection hypotheses.
func TestRbacGroupReadAndDeleteFailures(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroup")
	const path = "/v1/organizations/rbac_groups/group_protected"
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 403, "forbidden")
	})
	api.Handle("DELETE "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 400, "group grants owner access")
	})
	_, err := s.Read(p.ReadRequest{Urn: urn, ID: "group_protected"})
	require.Error(t, err)
	state := providertest.Props(map[string]any{"name": "Protected", "sourceType": "scim", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	err = s.Delete(p.DeleteRequest{Urn: urn, ID: "group_protected", Properties: state})
	require.ErrorContains(t, err, "group grants owner access")
	api.LastRequest(t, http.MethodDelete, path)
	api.Handle("GET /v1/organizations/rbac_groups/group_missing", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusNotFound, "not found")
	})
	missing, err := s.Read(p.ReadRequest{Urn: urn, ID: "group_missing"})
	require.NoError(t, err)
	require.Empty(t, missing.ID)
	for i := 0; i < 2; i++ {
		require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn, ID: "group_missing", Properties: state}))
	}
}

// Ambiguous non-idempotent creates must not be retried after a 500.
func TestRbacGroupAmbiguousCreateRetry(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroup")
	attempts := 0
	api.Handle("POST /v1/organizations/rbac_groups", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		attempts++
		if attempts == 1 {
			w.Header().Set("retry-after-ms", "1")
			fakeapi.WriteError(w, 500, "response lost after creation")
			return
		}
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "group_second", "name": body["name"], "source_type": "direct", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
	})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: providertest.Props(map[string]any{"name": "Platform"})})
	require.Error(t, err)
	require.Equal(t, 1, attempts)
	require.Empty(t, created.ID)
}

func groupResponse() fakeapi.Object {
	return fakeapi.Object{"id": "group_test", "type": "rbac_group", "name": "Platform", "source_type": "direct", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "role_ids": []any{}}
}

func TestRbacGroupRoleAvailability(t *testing.T) {
	for _, roles := range []any{nil, []any{}, []any{"role_1"}} {
		t.Run("roles", func(t *testing.T) {
			s, api := providertest.New(t)
			obj := groupResponse()
			obj["role_ids"] = roles
			api.Put("/v1/organizations/rbac_groups/group_test", obj)
			r, err := s.Read(p.ReadRequest{Urn: providertest.URN(rbacGroupToken), ID: "group_test"})
			require.NoError(t, err)
			v := r.Properties.Get("roleIds")
			if roles == nil {
				require.True(t, v.IsNull())
			} else {
				require.True(t, v.IsArray())
				require.Equal(t, len(roles.([]any)), v.AsArray().Len())
			}
		})
	}
}

func TestRbacGroupMalformedCreateAndLegacyRefresh(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN(rbacGroupToken)
	api.Handle("POST /v1/organizations/rbac_groups", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		obj := groupResponse()
		obj["source_type"] = map[string]any{"invalid": true}
		delete(obj, "updated_at")
		fakeapi.WriteJSON(w, obj)
	})
	inputs := providertest.Props(map[string]any{"name": "Platform"})
	created, err := s.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	require.ErrorContains(t, err, "incomplete RBAC group response")
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	api.Put("/v1/organizations/rbac_groups/group_test", groupResponse())
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: "group_test", Properties: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "2026-01-01T00:00:00Z", refreshed.Properties.Get("createdAt").AsString())
	api.Handle("POST /v1/organizations/rbac_groups/group_test", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"name": "Platform"}, body)
		fakeapi.WriteJSON(w, groupResponse())
	})
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: refreshed.ID, State: refreshed.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Equal(t, n+1, len(api.Requests()), "Update must not refresh internally")
	require.Equal(t, "direct", updated.Properties.Get("sourceType").AsString())
}

func TestRbacGroupRejectsForeignAndMalformedRead(t *testing.T) {
	for _, field := range []string{"id", "type", "source_type", "created_at", "role_ids", "name"} {
		t.Run(field, func(t *testing.T) {
			s, api := providertest.New(t)
			obj := groupResponse()
			switch field {
			case "id":
				obj[field] = "foreign"
			case "type":
				obj[field] = "other"
			case "source_type":
				obj[field] = true
			case "created_at":
				obj[field] = "not-time"
			case "role_ids":
				obj[field] = []any{3}
			case "name":
				delete(obj, field)
			}
			api.Put("/v1/organizations/rbac_groups/group_test", obj)
			_, err := s.Read(p.ReadRequest{Urn: providertest.URN(rbacGroupToken), ID: "group_test"})
			require.Error(t, err)
		})
	}
}

func TestRbacGroupMalformedAllocationFields(t *testing.T) {
	for _, field := range []string{"created_at", "source_type", "role_ids"} {
		t.Run(field, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("POST /v1/organizations/rbac_groups", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				obj := groupResponse()
				switch field {
				case "created_at":
					obj[field] = "invalid-time"
				case "source_type":
					obj[field] = map[string]any{}
				case "role_ids":
					obj[field] = []any{4}
				}
				fakeapi.WriteJSON(w, obj)
			})
			created, err := s.Create(p.CreateRequest{Urn: providertest.URN(rbacGroupToken), Properties: providertest.Props(map[string]any{"name": "Platform"})})
			require.Error(t, err)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Zero(t, created.Properties.Len())
			require.Len(t, api.Requests(), 1)
		})
	}
}

func TestRbacGroupMetadataAndUnknownDiff(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN(rbacGroupToken)
	deps := []resourceurn.URN{providertest.URN(rbacGroupToken)}
	for _, v := range []property.Value{property.New(property.Computed), property.New("Platform").WithSecret(true), property.New(property.Computed).WithSecret(true), property.New("Platform").WithDependencies(deps), property.New(property.Computed).WithSecret(true).WithDependencies(deps)} {
		inputs := property.NewMap(map[string]property.Value{"name": v})
		checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, inputs, checked.Inputs)
		created, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, v, created.Properties.Get("name"))
		if v.IsComputed() {
			for _, old := range []string{"", "Platform"} {
				state := providertest.Props(map[string]any{"name": old, "sourceType": "direct", "createdAt": "x", "updatedAt": "x"})
				diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: "group_test", State: state, Inputs: inputs})
				require.NoError(t, err)
				require.True(t, diff.HasChanges)
				require.Equal(t, p.Update, diff.DetailedDiff["name"].Kind)
			}
		}
	}
	require.Empty(t, api.Requests())
}
