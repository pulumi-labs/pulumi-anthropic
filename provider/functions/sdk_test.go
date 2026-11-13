package functions_test

import (
	"net/http"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

func TestModelsPaginationFailureDoesNotReturnPartialInventory(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("GET /v1/models", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		if r.URL.Query().Get("after_id") == "" {
			fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{fakeapi.Object{
				"id": "model-one", "display_name": "One", "created_at": "2026-01-01T00:00:00Z",
			}}, "last_id": "model-one", "has_more": true})
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fakeapi.WriteJSON(w, fakeapi.Object{"error": fakeapi.Object{"type": "permission_error", "message": "denied"}})
	})
	_, err := s.Invoke(p.InvokeRequest{Token: tokens.Type("anthropic:index:getModels")})
	require.Error(t, err)
	require.Len(t, api.Requests(), 2)
}

func TestWorkspaceRateLimitsIncludeInheritedAndUseSDKShape(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("GET /v1/organizations/workspaces/{id}/rate_limits", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		require.Equal(t, "true", r.URL.Query().Get("include_inherited"))
		require.Empty(t, r.URL.Query().Get("beta"))
		if r.URL.Query().Get("page") == "" {
			fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{fakeapi.Object{
				"rate_limit_id": "rl_one", "workspace_id": "wrkspc_1", "type": "workspace_rate_limit",
				"group":  fakeapi.Object{"id": "opus", "type": "model_group", "display_name": "Opus"},
				"models": []any{"model-one"},
				"limits": []any{fakeapi.Object{"type": "requests_per_minute", "value": 0, "org_limit": 0}},
			}}, "next_page": "page_two"})
			return
		}
		require.Equal(t, "page_two", r.URL.Query().Get("page"))
		fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{fakeapi.Object{
			"rate_limit_id": "rl_two", "group": fakeapi.Object{"id": "batch", "type": "batch"},
			"models": nil, "limits": []any{},
		}}, "next_page": nil})
	})
	out := invoke(t, s, "getRateLimits", map[string]any{"workspaceId": "wrkspc_1"})
	limits := out.Get("rateLimits").AsArray()
	require.Equal(t, 2, limits.Len())
	first := limits.Get(0).AsMap()
	require.Equal(t, "rl_one", first.Get("id").AsString())
	require.Equal(t, "model_group", first.Get("groupType").AsString())
	require.Equal(t, 0.0, first.Get("limits").AsArray().Get(0).AsMap().Get("orgLimit").AsNumber())
	require.True(t, limits.Get(1).AsMap().Get("models").IsArray())
	require.Empty(t, limits.Get(1).AsMap().Get("models").AsArray().AsSlice())
}
