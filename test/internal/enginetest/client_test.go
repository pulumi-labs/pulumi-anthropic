package enginetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/stretchr/testify/require"
)

func TestSDKCredentialHeaders(t *testing.T) {
	for _, tc := range []struct{ env, plane, key string }{
		{"ANTHROPIC_API_KEY", "workspace", "synthetic-workspace-key"},
		{"ANTHROPIC_ADMIN_KEY", "admin", "synthetic-admin-key"},
		{"ANTHROPIC_ADMIN_OAUTH_KEY", "oauth", "synthetic-oauth-token"},
	} {
		t.Run(tc.env, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.plane == "oauth" {
					require.Equal(t, "Bearer "+tc.key, r.Header.Get("Authorization"))
					require.Empty(t, r.Header.Get("x-api-key"))
					require.Contains(t, r.Header.Get("anthropic-beta"), "oauth-2025-04-20")
				} else {
					require.Empty(t, r.Header.Get("Authorization"))
					require.Equal(t, tc.key, r.Header.Get("x-api-key"))
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("{}"))
			}))
			defer api.Close()
			creds, err := selectedCredentials(tc.env, tc.key)
			require.NoError(t, err)
			c := Context{credentials: creds}
			client := c.client(t, tc.plane)
			_, err = client.Beta.Tunnels.New(t.Context(), sdk.BetaTunnelNewParams{}, option.WithBaseURL(api.URL))
			require.NoError(t, err)
		})
	}
}

func TestSDKFederationCredential(t *testing.T) {
	oldWorkspaceID := *workspaceID
	*workspaceID = "wrkspc_test"
	t.Cleanup(func() { *workspaceID = oldWorkspaceID })
	identity := filepath.Join(t.TempDir(), "identity.jwt")
	require.NoError(t, os.WriteFile(identity, []byte("github-oidc-token"), 0600))
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/oauth/token" {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "github-oidc-token", body["assertion"])
			require.Equal(t, "fdrl_test", body["federation_rule_id"])
			require.Equal(t, "org_test", body["organization_id"])
			require.Equal(t, "wrkspc_test", body["workspace_id"])
			require.Equal(t, "svac_test", body["service_account_id"])
			require.Contains(t, r.Header.Get("anthropic-beta"), "oidc-federation-2026-04-01")
			_, _ = w.Write([]byte(`{"access_token":"exchanged-token"}`))
			return
		}
		require.Equal(t, "Bearer exchanged-token", r.Header.Get("Authorization"))
		require.Empty(t, r.Header.Get("anthropic-workspace-id"))
		require.Contains(t, r.Header.Get("anthropic-beta"), "oauth-2025-04-20")
		_, _ = w.Write([]byte("{}"))
	}))
	defer api.Close()
	creds, err := selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_FILE", identity, federationCredentials{
		RuleID: "fdrl_test", OrganizationID: "org_test", WorkspaceID: "wrkspc_test", ServiceAccountID: "svac_test",
	})
	require.NoError(t, err)
	client := (&Context{credentials: creds}).client(t, "workspace")
	_, err = client.Beta.Tunnels.New(t.Context(), sdk.BetaTunnelNewParams{}, option.WithBaseURL(api.URL))
	require.NoError(t, err)
}
