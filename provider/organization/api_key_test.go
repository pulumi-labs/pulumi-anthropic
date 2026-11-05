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

const apiKeyPath = "/v1/organizations/api_keys/apikey_1"

func seedApiKey(api *fakeapi.Server) {
	api.Put(apiKeyPath, fakeapi.Object{
		"id": "apikey_1", "type": "api_key", "name": "old", "status": "active",
		"partial_key_hint": "sk-ant-api03-R2D...igAA", "workspace_id": "wrkspc_1",
		"created_at": "2026-01-01T00:00:00Z", "expires_at": nil,
	})
}

func TestApiKeyLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedApiKey(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:ApiKey",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "ci"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "wrkspc_1", output.Get("workspaceId").AsString())
				req := api.LastRequest(t, http.MethodPost, apiKeyPath)
				assert.Equal(t, fakeapi.Object{"name": "ci"}, req.Body, "undeclared status is left alone")
			},
		},
		Updates: []integration.Operation{{
			// Dropping the name stops managing it; it is not cleared.
			Inputs: providertest.Props(map[string]any{"apiKeyId": "apikey_1", "status": "inactive"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, apiKeyPath)
				assert.Equal(t, fakeapi.Object{"status": "inactive"}, req.Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, fakeapi.Object{"status": "archived"}, last.Body, "deleting the resource archives the key")
	assert.Equal(t, "ci", api.Object(apiKeyPath)["name"])
}

func TestApiKeyMissing(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)
	_, err := server.Create(p.CreateRequest{Urn: urn("ApiKey"),
		Properties: providertest.Props(map[string]any{"apiKeyId": "apikey_9"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key apikey_9 does not exist")
}

func TestApiKeyRefreshAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedApiKey(api)

	created, checked := providertest.Create(t, server, urn("ApiKey"), providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "old"}))
	resp := providertest.Read(t, server, urn("ApiKey"), created, checked)
	assert.Equal(t, checked, resp.Inputs, "the undeclared status is not drift")

	api.Mutate(apiKeyPath, func(obj fakeapi.Object) { obj["name"] = "renamed" })
	resp = providertest.Read(t, server, urn("ApiKey"), created, checked)
	assert.Equal(t, "renamed", resp.Inputs.Get("name").AsString())

	imported, err := server.Read(p.ReadRequest{ID: "apikey_1", Urn: urn("ApiKey")})
	require.NoError(t, err)
	assert.Equal(t, providertest.Props(map[string]any{
		"apiKeyId": "apikey_1", "name": "renamed", "status": "active"}), imported.Inputs)
}
