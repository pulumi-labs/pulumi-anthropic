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

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const serviceAccountWorkspaceToken = "anthropic:identity:ServiceAccountWorkspace"

const membershipPath = "/v1/organizations/workspaces/wrkspc_1/service_accounts/svac_1"

func membership(role string) map[string]any {
	return map[string]any{"workspaceId": "wrkspc_1", "serviceAccountId": "svac_1", "workspaceRole": role}
}

func TestServiceAccountWorkspaceLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.SetHooks("service_accounts", fakeapi.Hooks{KeyField: "service_account_id"})

	integration.LifeCycleTest{
		Resource: serviceAccountWorkspaceToken,
		Create: integration.Operation{
			Inputs: providertest.Props(membership("workspace_user")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/workspaces/wrkspc_1/service_accounts")
				assert.Equal(t, fakeapi.Object{"service_account_id": "svac_1", "workspace_role": "workspace_user"},
					req.Body, "the workspace ID travels in the path, not the body")
				assert.Equal(t, "wrkspc_1", output.Get("workspaceId").AsString())
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(membership("workspace_developer")),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, membershipPath)
				assert.Equal(t, fakeapi.Object{"workspace_role": "workspace_developer"}, req.Body)
			},
		}},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, membershipPath)
	assert.Nil(t, api.Object(membershipPath))
}

func TestServiceAccountWorkspaceReadAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.SetHooks("service_accounts", fakeapi.Hooks{KeyField: "service_account_id"})

	created, checked := providertest.Create(t, server, providertest.URN(serviceAccountWorkspaceToken), providertest.Props(membership("workspace_user")))
	assert.Equal(t, "wrkspc_1/svac_1", created.ID)
	assert.Equal(t, checked, providertest.Read(t, server, providertest.URN(serviceAccountWorkspaceToken), created, checked).Inputs)

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: providertest.URN(serviceAccountWorkspaceToken)})
	require.NoError(t, err)
	assert.Equal(t, created.ID, imported.ID)
	assert.Equal(t, checked, imported.Inputs, "an import recovers the workspace ID from the resource ID")

	api.Mutate(membershipPath, func(obj fakeapi.Object) { obj["workspace_role"] = "workspace_admin" })
	assert.Equal(t, "workspace_admin",
		providertest.Read(t, server, providertest.URN(serviceAccountWorkspaceToken), created, checked).Inputs.Get("workspaceRole").AsString())

	d := providertest.Diff(t, server, providertest.URN(serviceAccountWorkspaceToken), created, checked, providertest.Props(map[string]any{
		"workspaceId": "wrkspc_2", "serviceAccountId": "svac_1", "workspaceRole": "workspace_user",
	}))
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["workspaceId"].Kind)
}
