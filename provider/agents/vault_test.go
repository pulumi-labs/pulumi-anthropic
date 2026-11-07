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

package agents_test

import (
	"net/http"
	"testing"

	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

func TestVaultLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)

	integration.LifeCycleTest{
		Resource: "anthropic:agents:Vault",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{
				"displayName": "prod", "metadata": map[string]any{"env": "prod", "owner": "infra"},
			}),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/vaults")
				assert.Equal(t, "managed-agents-2026-04-01", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"display_name": "prod", "metadata": map[string]any{"env": "prod", "owner": "infra"},
				}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"displayName": "production", "metadata": map[string]any{"env": "prod"}}),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{
					"display_name": "production", "metadata": map[string]any{"env": "prod", "owner": nil},
				}, api.Last().Body)
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method, "deleting a vault deletes it")
	assert.Regexp(t, `^/v1/vaults/vault_\d+$`, last.Path)
}

func TestMemoryStoreLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)

	integration.LifeCycleTest{
		Resource: "anthropic:agents:MemoryStore",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"name": "notes", "description": "Team notes."}),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/memory_stores")
				assert.Equal(t, "agent-memory-2026-07-22", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{"name": "notes", "description": "Team notes."}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"name": "notes"}),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{"description": nil}, api.Last().Body,
					"a dropped description is cleared")
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Regexp(t, `^/v1/memory_stores/memory_store_\d+$`, last.Path)
}

func TestUserProfileLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)

	integration.LifeCycleTest{
		Resource: "anthropic:agents:UserProfile",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{
				"name": "Acme", "accessType": "passthrough", "externalId": "acme-1",
				"metadata": map[string]any{"plan": "pro", "region": "us"},
			}),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/user_profiles")
				assert.Equal(t, "user-profiles-2026-08-18", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"name": "Acme", "access_type": "passthrough", "external_id": "acme-1",
					"metadata": map[string]any{"plan": "pro", "region": "us"},
				}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{
				"name": "Acme", "accessType": "passthrough", "externalId": "acme-1",
				"metadata": map[string]any{"plan": "pro"},
			}),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{"metadata": map[string]any{"plan": "pro", "region": ""}},
					api.Last().Body, "user profiles remove a metadata key with an empty string")
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method, "a user profile is deleted, not abandoned")
	assert.NotRegexp(t, `/archive$`, last.Path)
}
