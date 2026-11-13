package config_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

func configTestServer(t *testing.T) integration.Server {
	t.Helper()
	prov, err := provider.Provider()
	require.NoError(t, err)
	s, err := integration.NewServer(t.Context(), provider.Name, semver.MustParse("0.1.0"), integration.WithProvider(prov))
	require.NoError(t, err)
	return s
}

func isolateConfigEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_ADMIN_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		t.Setenv(key, "")
	}
}

// Normative: credential material updates within asserted boundaries; routing
// and asserted identity changes replace. No server identity inspection occurs.
func TestConfigurationDiff(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, tc := range []struct {
		name, key, old, next string
		changes              bool
	}{
		{"unchanged", "apiKey", "old", "old", false},
		{"api rotation", "apiKey", "old", "new", true},
		{"admin rotation", "adminKey", "old", "new", true},
		{"token rotation", "authToken", "old", "new", true},
		{"workspace selection", "workspaceId", "workspace-a", "workspace-b", true},
		{"origin boundary", "baseUrl", "https://a.invalid", "https://b.invalid", true},
		{"equivalent URL", "baseUrl", "https://api.anthropic.com", "https://api.anthropic.com/", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := configTestServer(t)
			old, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"workspaceId": property.New("workspace-a"), tc.key: property.New(tc.old)})})
			require.NoError(t, err)
			require.Empty(t, old.Failures)
			next, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"workspaceId": property.New("workspace-a"), tc.key: property.New(tc.next)})})
			require.NoError(t, err)
			require.Empty(t, next.Failures)
			diff, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, OldInputs: old.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			require.Equal(t, tc.changes, diff.HasChanges)
			if tc.changes {
				want := p.UpdateReplace
				if tc.key != "baseUrl" {
					want = p.Update
				}
				require.Equal(t, want, diff.DetailedDiff[tc.key].Kind)
			}
		})
	}
}

// Contract: config defaults must preserve explicit values, secrets, and unknowns;
// CheckConfig/Configure need no reachable platform. No request is made here.
func TestConfigurationCheck(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("ANTHROPIC_API_KEY", "environment-key")
	s := configTestServer(t)
	checked, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{
		"authToken": property.New("explicit-token"),
		"baseUrl":   property.New("http://127.0.0.1:1"),
	})})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, "environment-key", checked.Inputs.Get("apiKey").AsString())
	require.True(t, checked.Inputs.Get("apiKey").Secret())
	require.True(t, checked.Inputs.Get("authToken").Secret())
	require.NoError(t, s.Configure(p.ConfigureRequest{Args: checked.Inputs}))
	explicit, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"apiKey": property.New("explicit-key")})})
	require.NoError(t, err)
	require.Equal(t, "explicit-key", explicit.Inputs.Get("apiKey").AsString())
	unknown, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"apiKey": property.New(property.Computed)})})
	require.NoError(t, err)
	require.Empty(t, unknown.Failures)
	require.True(t, unknown.Inputs.Get("apiKey").IsComputed())
	require.True(t, unknown.Inputs.Get("apiKey").Secret())
}

// Missing credentials remain lazy, but malformed URLs fail local validation.
func TestConfigurationDeferredValidation(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, inputs := range []property.Map{property.NewMap(nil), property.NewMap(map[string]property.Value{"apiKey": property.New("test-key"), "baseUrl": property.New("://invalid")})} {
		s := configTestServer(t)
		checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
		require.NoError(t, err)
		if inputs.Get("baseUrl").IsString() {
			require.Equal(t, "baseUrl", checked.Failures[0].Property)
		} else {
			require.Empty(t, checked.Failures)
			require.NoError(t, s.Configure(p.ConfigureRequest{Args: checked.Inputs}))
		}
	}
}

// Synthetic client construction observations only: local HTTP observations establish
// which headers are sent, NOT which credentials Anthropic accepts.
func TestConfigurationCredentialPlanes(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("ANTHROPIC_API_KEY", "ambient-key-must-not-leak")
	for _, tc := range []struct {
		name        string
		cfg         config.Config
		plane       config.Plane
		key, bearer string
	}{
		{"workspace key wins", config.Config{APIKey: "workspace-key", AuthToken: "token"}, config.Workspace, "workspace-key", ""},
		{"admin key wins", config.Config{AdminKey: "admin-key", AuthToken: "token"}, config.Admin, "admin-key", ""},
		{"workspace OAuth fallback", config.Config{AuthToken: "token"}, config.Workspace, "", "Bearer token"},
		{"admin OAuth fallback", config.Config{AuthToken: "token"}, config.Admin, "", "Bearer token"},
		{"identity OAuth only", config.Config{APIKey: "workspace-key", AdminKey: "admin-key", AuthToken: "token"}, config.OrgAdminOAuth, "", "Bearer token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var header http.Header
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				header = r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer api.Close()
			c := tc.cfg
			c.BaseURL = api.URL
			c.WorkspaceID = "workspace-test"
			require.NoError(t, c.Configure(context.Background()))
			client, err := c.Client(tc.plane)
			require.NoError(t, err)
			require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil))
			// API keys use only x-api-key. OAuth uses only Authorization.
			wantBearer := tc.bearer
			require.Equal(t, tc.key, header.Get("x-api-key"))
			require.Equal(t, wantBearer, header.Get("Authorization"))
			require.Equal(t, "workspace-test", header.Get("anthropic-workspace-id"))
			beta := ""
			if tc.key == "" {
				beta = "oauth-2025-04-20"
			}
			require.Equal(t, beta, header.Get("anthropic-beta"))
			require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil,
				option.WithHeader("anthropic-beta", "managed-agents-2026-04-01")))
			wantBeta := "managed-agents-2026-04-01"
			if beta != "" {
				wantBeta += "," + beta
			}
			require.Equal(t, wantBeta, header.Get("anthropic-beta"))
		})
	}
	c := config.Config{APIKey: "workspace-key", AdminKey: "admin-key"}
	require.NoError(t, c.Configure(t.Context()))
	_, err := c.Client(config.OrgAdminOAuth)
	require.ErrorContains(t, err, "org:admin")
	empty := config.Config{}
	require.NoError(t, empty.Configure(t.Context()))
	for _, plane := range []config.Plane{config.Workspace, config.Admin, config.OrgAdminOAuth} {
		_, err := empty.Client(plane)
		require.Error(t, err)
	}
}
