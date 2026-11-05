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

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
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
				assert.Equal(t, "sk-ant-admin-test", req.Header.Get("X-Api-Key"), "RBAC takes the Admin key")
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
// paginated with either the next_page or the has_more/last_id scheme.
func memberListServerBehavior(api *fakeapi.Server, cursor bool) {
	const group = "/v1/organizations/rbac_groups/rbac_group_1"
	api.Put(group, fakeapi.Object{"id": "rbac_group_1", "name": "Platform"})
	api.SetHooks("members", fakeapi.Hooks{KeyField: "user_id", OnCreate: func(obj fakeapi.Object) {
		obj["email"] = "ada@example.com"
	}})
	api.Handle("GET "+group+"/members", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		first := fakeapi.Object{"data": []any{fakeapi.Object{"user_id": "user_other", "email": "x@example.com"}}}
		var second []any
		if m := api.Object(group + "/members/user_1"); m != nil {
			second = append(second, m)
		}
		q := r.URL.Query()
		switch {
		case cursor && q.Get("page") == "":
			first["next_page"] = "cursor-2"
			fakeapi.WriteJSON(w, first)
		case !cursor && q.Get("after_id") == "":
			first["has_more"], first["last_id"] = true, "user_other"
			fakeapi.WriteJSON(w, first)
		default:
			fakeapi.WriteJSON(w, fakeapi.Object{"data": second, "has_more": false, "next_page": nil})
		}
	})
}

func TestRbacGroupMemberLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memberListServerBehavior(api, false)

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
	for name, cursor := range map[string]bool{"has_more": false, "next_page": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, api := providertest.New(t)
			memberListServerBehavior(api, cursor)

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
