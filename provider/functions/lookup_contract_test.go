package functions_test

import (
	"net/http"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

// Contract: unavailable lookup data must not become a successful empty result.
// These are loopback request/response mapping tests, not platform authorization tests.
func TestFunctionsAccessFailures(t *testing.T) {
	for _, tc := range []struct {
		name, route string
	}{
		{"getModels", "GET /v1/models"},
		{"getOrganization", "GET /v1/organizations/me"},
		{"getRateLimits", "GET /v1/organizations/rate_limits"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle(tc.route, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				w.WriteHeader(http.StatusForbidden)
				fakeapi.WriteJSON(w, fakeapi.Object{"type": "error", "error": fakeapi.Object{"type": "permission_error", "message": "denied"}})
			})
			_, err := s.Invoke(p.InvokeRequest{Token: tokens.Type("anthropic:index:" + tc.name), Args: property.Map{}})
			require.Error(t, err)
			require.Len(t, api.Requests(), 1, "403 is not a transient retry condition")
		})
	}
}

// Contract: an empty model inventory is an array, not null, for generated SDK users.
func TestFunctionsEmptyModels(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("GET /v1/models", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{}, "has_more": false})
	})
	out := invoke(t, s, "getModels", nil)
	require.True(t, out.Get("models").IsArray())
	require.Empty(t, out.Get("models").AsArray().AsSlice())
}
