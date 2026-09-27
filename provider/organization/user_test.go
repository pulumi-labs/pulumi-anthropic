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

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const userPath = "/v1/organizations/users/user_1"

func seedUser(api *fakeapi.Server) {
	api.Put(userPath, fakeapi.Object{
		"id": "user_1", "type": "user", "email": "ada@example.com", "name": "Ada",
		"role": "user", "added_at": "2026-01-01T00:00:00Z",
	})
}

func TestUserLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:User",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"userId": "user_1", "role": "developer"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "ada@example.com", output.Get("email").AsString())
				assert.Equal(t, "user_1", output.Get("userId").AsString())
				req := api.LastRequest(t, http.MethodPost, userPath)
				assert.Equal(t, fakeapi.Object{"role": "developer"}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"userId": "user_1", "role": "billing"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, userPath)
				assert.Equal(t, fakeapi.Object{"role": "billing"}, req.Body)
			},
		}},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, userPath)
	assert.Nil(t, api.Object(userPath), "deleting the resource removes the user from the organization")
}

func TestUserAdoptsWithoutChange(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	created, _ := providertest.Create(t, server, urn("User"), providertest.Props(map[string]any{"userId": "user_1", "role": "user"}))
	assert.Equal(t, "user_1", created.ID)
	for _, req := range api.Requests() {
		assert.NotEqual(t, http.MethodPost, req.Method, "a user already in the declared role is not updated")
	}
}

func TestUserMissing(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)
	_, err := server.Create(p.CreateRequest{Urn: urn("User"),
		Properties: providertest.Props(map[string]any{"userId": "user_9", "role": "user"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user user_9 does not exist")
	assert.Contains(t, err.Error(), "Invite")
}

func TestUserRefreshAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	created, checked := providertest.Create(t, server, urn("User"), providertest.Props(map[string]any{"userId": "user_1", "role": "user"}))
	resp := providertest.Read(t, server, urn("User"), created, checked)
	assert.Equal(t, checked, resp.Inputs)

	api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "developer" })
	resp = providertest.Read(t, server, urn("User"), created, checked)
	assert.Equal(t, "developer", resp.Inputs.Get("role").AsString())

	imported, err := server.Read(p.ReadRequest{ID: "user_1", Urn: urn("User")})
	require.NoError(t, err)
	assert.Equal(t, providertest.Props(map[string]any{"userId": "user_1", "role": "developer"}), imported.Inputs)
}
