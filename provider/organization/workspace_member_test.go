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

const membersPath = "/v1/organizations/workspaces/wrkspc_1/members"

func memberServerBehavior(api *fakeapi.Server) {
	api.SetHooks("members", fakeapi.Hooks{KeyField: "user_id"})
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
