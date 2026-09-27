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

	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
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
