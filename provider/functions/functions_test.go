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

package functions_test

import (
	"net/http"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

func invoke(t *testing.T, server integration.Server, token string, args map[string]any) property.Map {
	t.Helper()
	resp, err := server.Invoke(p.InvokeRequest{Token: tokens.Type("anthropic:index:" + token), Args: providertest.Props(args)})
	require.NoError(t, err)
	require.Empty(t, resp.Failures)
	return resp.Return
}

func TestGetOrganization(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.Put("/v1/organizations/me", fakeapi.Object{"id": "8f9e", "name": "Acme", "type": "organization"})

	out := invoke(t, server, "getOrganization", nil)
	assert.Equal(t, "8f9e", out.Get("organizationId").AsString())
	assert.Equal(t, "Acme", out.Get("name").AsString())
	req := api.LastRequest(t, http.MethodGet, "/v1/organizations/me")
	assert.Equal(t, "sk-ant-admin-test", req.Header.Get("X-Api-Key"), "organization reads use the Admin key")
}

// getModels follows the after_id pagination style.
func TestGetModels(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.Handle("GET /v1/models", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		assert.Empty(t, r.URL.Query().Get("beta"), "the Models API is GA")
		if r.URL.Query().Get("after_id") == "" {
			fakeapi.WriteJSON(w, fakeapi.Object{"has_more": true, "last_id": "claude-opus-5-5", "data": []any{
				fakeapi.Object{"id": "claude-opus-5-5", "display_name": "Claude Opus 5.5",
					"created_at": "2026-08-01T00:00:00Z", "type": "model"},
			}})
			return
		}
		assert.Equal(t, "claude-opus-5-5", r.URL.Query().Get("after_id"))
		fakeapi.WriteJSON(w, fakeapi.Object{"has_more": false, "data": []any{
			fakeapi.Object{"id": "claude-haiku-4-5", "display_name": "Claude Haiku 4.5",
				"created_at": "2025-10-01T00:00:00Z", "type": "model"},
		}})
	})

	out := invoke(t, server, "getModels", nil)
	models := out.Get("models").AsArray().AsSlice()
	require.Len(t, models, 2, "every page is read")
	assert.Equal(t, "claude-opus-5-5", models[0].AsMap().Get("id").AsString())
	assert.Equal(t, "Claude Haiku 4.5", models[1].AsMap().Get("displayName").AsString())
}

// getRateLimits follows the next_page pagination style.
func TestGetRateLimitsOrganization(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.Handle("GET /v1/organizations/rate_limits", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		if r.URL.Query().Get("page") == "" {
			fakeapi.WriteJSON(w, fakeapi.Object{"next_page": "p2", "data": []any{fakeapi.Object{
				"id": "rl_1", "type": "rate_limit", "group_type": "model_group", "models": []any{"claude-opus-5-5"},
				"group":  fakeapi.Object{"id": "opus", "type": "model_group", "display_name": "Claude Opus"},
				"limits": []any{fakeapi.Object{"type": "requests_per_minute", "value": 4000}},
			}}})
			return
		}
		assert.Equal(t, "p2", r.URL.Query().Get("page"))
		fakeapi.WriteJSON(w, fakeapi.Object{"next_page": nil, "data": []any{fakeapi.Object{
			"id": "rl_2", "type": "rate_limit", "group_type": "batch", "models": []any{},
			"group":  fakeapi.Object{"id": "batch", "type": "batch"},
			"limits": []any{fakeapi.Object{"type": "enqueued_requests", "value": 100000}},
		}}})
	})

	out := invoke(t, server, "getRateLimits", nil)
	limits := out.Get("rateLimits").AsArray().AsSlice()
	require.Len(t, limits, 2, "every page is read")
	first := limits[0].AsMap()
	assert.Equal(t, "rl_1", first.Get("id").AsString())
	assert.Equal(t, "Claude Opus", first.Get("group").AsMap().Get("displayName").AsString())
	assert.Equal(t, 4000.0, first.Get("limits").AsArray().Get(0).AsMap().Get("value").AsNumber())
}

func TestGetRateLimitsWorkspace(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.Handle("GET /v1/organizations/workspaces/{id}/rate_limits",
		func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
			assert.Equal(t, "wrkspc_1", r.PathValue("id"))
			fakeapi.WriteJSON(w, fakeapi.Object{"next_page": nil, "data": []any{fakeapi.Object{
				"rate_limit_id": "rl_1", "workspace_id": "wrkspc_1", "type": "workspace_rate_limit",
				"group_type": "model_group", "models": []any{"claude-opus-5-5"},
				"group":  fakeapi.Object{"id": "opus", "type": "model_group"},
				"limits": []any{fakeapi.Object{"type": "requests_per_minute", "value": 100, "org_limit": 4000}},
			}}})
		})

	out := invoke(t, server, "getRateLimits", map[string]any{"workspaceId": "wrkspc_1"})
	limit := out.Get("rateLimits").AsArray().Get(0).AsMap()
	assert.Equal(t, "rl_1", limit.Get("id").AsString(), "a workspace limit's rate_limit_id is its id")
	value := limit.Get("limits").AsArray().Get(0).AsMap()
	assert.Equal(t, 100.0, value.Get("value").AsNumber())
	assert.Equal(t, 4000.0, value.Get("orgLimit").AsNumber())
}

func TestGetRateLimitsEmpty(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.Handle("GET /v1/organizations/rate_limits", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"next_page": nil, "data": []any{}})
	})
	out := invoke(t, server, "getRateLimits", nil)
	assert.Empty(t, out.Get("rateLimits").AsArray().AsSlice())
}
