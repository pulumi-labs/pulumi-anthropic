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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

const federationRuleWorkspaceToken = "anthropic:identity:FederationRuleWorkspace"

const rulePath = "/v1/organizations/federation_rules/fdrl_1"

// ruleBindingServerBehavior seeds a rule; its workspace bindings are a sub-collection
// keyed by workspace ID, whose objects the API fills in with the workspace's name.
func ruleBindingServerBehavior(api *fakeapi.Server) {
	api.Put(rulePath, fakeapi.Object{"id": "fdrl_1", "archived_at": nil})
	api.SetHooks("workspaces", fakeapi.Hooks{KeyField: "workspace_id", OnCreate: func(obj fakeapi.Object) {
		obj["type"] = "federation_rule_workspace"
		obj["federation_rule_id"] = "fdrl_1"
		obj["workspace_name"] = "Production"
		obj["created_at"] = "2026-09-25T00:00:00Z"
	}})
}

func binding(workspaceID string) map[string]any {
	return map[string]any{"federationRuleId": "fdrl_1", "workspaceId": workspaceID}
}

func TestFederationRuleWorkspaceLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	ruleBindingServerBehavior(api)

	integration.LifeCycleTest{
		Resource: federationRuleWorkspaceToken,
		Create: integration.Operation{
			Inputs: providertest.Props(binding("wrkspc_1")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, rulePath+"/workspaces")
				assert.Equal(t, fakeapi.Object{"workspace_id": "wrkspc_1"}, req.Body)
				assert.Equal(t, "Production", output.Get("workspaceName").AsString())
			},
		},
		// Bindings cannot be updated, so a change of workspace replaces the binding.
		Updates: []integration.Operation{{Inputs: providertest.Props(binding("wrkspc_2"))}},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, rulePath+"/workspaces/wrkspc_1")
	api.LastRequest(t, http.MethodDelete, rulePath+"/workspaces/wrkspc_2")
	assert.Nil(t, api.Object(rulePath+"/workspaces/wrkspc_2"))
}

func TestFederationRuleWorkspaceRead(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	ruleBindingServerBehavior(api)

	created, checked := providertest.Create(t, server, providertest.URN(federationRuleWorkspaceToken), providertest.Props(binding("wrkspc_1")))
	assert.Equal(t, "fdrl_1/wrkspc_1", created.ID)
	read := providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, checked, read.Inputs)

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: providertest.URN(federationRuleWorkspaceToken)})
	assert.NoError(t, err)
	assert.Equal(t, checked, imported.Inputs, "an import recovers both IDs from the resource ID")
	assert.Equal(t, "Production", imported.Properties.Get("workspaceName").AsString(),
		"an import reads the binding's details")

	api.Remove(rulePath + "/workspaces/wrkspc_1")
	assert.Empty(t, providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked).ID,
		"a binding removed outside of Pulumi reads as gone")

	api.Remove(rulePath)
	assert.Empty(t, providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked).ID,
		"a binding of a rule that no longer exists reads as gone")
}
