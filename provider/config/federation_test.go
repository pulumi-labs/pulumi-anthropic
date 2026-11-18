package config_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

// Synthetic token responses test SDK plumbing, not real grant acceptance.
func TestConfigurationFederationLazyRenewal(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("ANTHROPIC_IDENTITY_TOKEN_FILE", "ambient-source-must-not-open")
	assertion := filepath.Join(t.TempDir(), "assertion.jwt")
	var exchanges, requests int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/oauth/token" {
			exchanges++
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "urn:ietf:params:oauth:grant-type:jwt-bearer", body["grant_type"])
			require.Equal(t, fmt.Sprintf("assertion-%d", exchanges), body["assertion"])
			require.Equal(t, "org-test", body["organization_id"])
			require.Equal(t, "wrkspc_test", body["workspace_id"])
			require.Equal(t, "fdrl_test", body["federation_rule_id"])
			require.Equal(t, "svac_test", body["service_account_id"])
			require.NotContains(t, body, "scope") // scope comes from the server-side rule
			require.Contains(t, r.Header.Get("anthropic-beta"), "oauth-2025-04-20")
			require.Contains(t, r.Header.Get("anthropic-beta"), "oidc-federation-2026-04-01")
			require.Empty(t, r.Header.Get("Authorization"))
			if _, err := fmt.Fprintf(w, `{"access_token":"minted-%d","token_type":"Bearer","expires_in":3600,"scope":"org:admin"}`, exchanges); err != nil {
				t.Error(err)
			}
			return
		}
		requests++
		require.Empty(t, r.Header.Get("anthropic-workspace-id"), "federation workspace belongs in exchange, not per-request header")
		require.Contains(t, r.Header.Get("anthropic-beta"), "managed-agents-2026-04-01")
		require.Contains(t, r.Header.Get("anthropic-beta"), "oauth-2025-04-20")
		require.Equal(t, fmt.Sprintf("Bearer minted-%d", exchanges), r.Header.Get("Authorization"))
		if requests == 2 {
			// Force a credential refresh. The explicit file is re-read for exchange.
			require.NoError(t, os.WriteFile(assertion, []byte("assertion-2"), 0600))
			w.WriteHeader(http.StatusUnauthorized)
			if _, err := fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"synthetic expired token"}}`); err != nil {
				t.Error(err)
			}
			return
		}
		if _, err := fmt.Fprint(w, `{}`); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	c := config.Config{BaseURL: api.URL, WorkspaceID: "wrkspc_test", Federation: &config.Federation{OrganizationID: "org-test", RuleID: "fdrl_test", IdentityToken: config.IdentityToken{File: &assertion}, ServiceAccountID: "svac_test"}}
	// File does not even exist yet: Configure cannot resolve credentials.
	require.NoError(t, c.Configure(t.Context()))
	require.Zero(t, exchanges)
	client, err := c.Client(config.OrgAdminOAuth)
	require.NoError(t, err)
	err = client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithMaxRetries(0))
	require.ErrorContains(t, err, "cannot read configured federation identity token file")
	require.NotContains(t, err.Error(), assertion)
	require.Zero(t, exchanges)
	require.NoError(t, os.WriteFile(assertion, []byte("assertion-1"), 0600))
	for range 2 {
		require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithMaxRetries(0), option.WithHeader("anthropic-beta", "managed-agents-2026-04-01")))
	}
	require.Equal(t, 2, exchanges)
	require.Equal(t, 3, requests)
	// A third operation uses the renewed cached token, without a third exchange.
	require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithHeader("anthropic-beta", "managed-agents-2026-04-01")))
	require.Equal(t, 2, exchanges)
}

func TestConfigurationFederationValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	f := property.New(property.NewMap(map[string]property.Value{"organizationId": property.New("org-test"), "ruleId": property.New("fdrl_test"), "identityToken": property.New(map[string]property.Value{"file": property.New(filepath.Join(t.TempDir(), "never-read"))})}))
	base := property.NewMap(map[string]property.Value{"workspaceId": property.New("default"), "federation": f})
	checked, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("federation").AsMap().Get("identityToken").Secret())
	missingWorkspace, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base.Delete("workspaceId")})
	require.NoError(t, err)
	require.NotEmpty(t, missingWorkspace.Failures)
	require.Equal(t, "workspaceId", missingWorkspace.Failures[0].Property)
	unknownWorkspace, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base.Set("workspaceId", property.New(property.Computed))})
	require.NoError(t, err)
	require.Empty(t, unknownWorkspace.Failures)
	for _, organization := range []property.Value{property.New(""), property.New(property.Computed)} {
		result, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base.Set("federation", property.New(f.AsMap().Set("organizationId", organization)))})
		require.NoError(t, err)
		if organization.IsComputed() {
			require.Empty(t, result.Failures)
			require.True(t, result.Inputs.Get("federation").AsMap().Get("organizationId").IsComputed())
		} else {
			require.NotEmpty(t, result.Failures)
			require.Equal(t, "federation.organizationId", result.Failures[0].Property)
		}
	}
	result, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base.Set("authToken", property.New("literal-token"))})
	require.NoError(t, err)
	require.NotEmpty(t, result.Failures)
	require.Equal(t, "authToken", result.Failures[0].Property)
	// Unknown nested source values do not cause required-field failures.
	result, err = configTestServer(t).CheckConfig(p.CheckRequest{Inputs: base.Set("federation", property.New(property.NewMap(map[string]property.Value{"organizationId": property.New(property.Computed), "ruleId": property.New(property.Computed), "identityToken": property.New(property.Computed)})))})
	require.NoError(t, err)
	require.Empty(t, result.Failures)
}

func TestFederationOrganizationChangesDoNotReplaceProvider(t *testing.T) {
	isolateConfigEnvironment(t)
	s := configTestServer(t)
	federation := property.NewMap(map[string]property.Value{
		"organizationId": property.New("org-old"), "ruleId": property.New("fdrl_test"),
		"identityToken": property.New(map[string]property.Value{"file": property.New(filepath.Join(t.TempDir(), "never-read"))}),
	})
	inputs := property.NewMap(map[string]property.Value{"workspaceId": property.New("wrkspc_test"), "federation": property.New(federation)})
	old, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, old.Failures)
	for _, organization := range []property.Value{property.New("org-new"), property.New(property.Computed).WithSecret(true)} {
		next, err := s.CheckConfig(p.CheckRequest{Inputs: inputs.Set("federation", property.New(federation.Set("organizationId", organization)))})
		require.NoError(t, err)
		require.Empty(t, next.Failures)
		diff, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, OldInputs: old.Inputs, Inputs: next.Inputs})
		require.NoError(t, err)
		require.True(t, diff.HasChanges)
		require.Equal(t, p.Update, diff.DetailedDiff["federation"].Kind)
	}
}
