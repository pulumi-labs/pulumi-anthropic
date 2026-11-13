package config_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

func TestConfigurationNormalizationAndMetadata(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, raw := range []string{"", "https://api.anthropic.com", "https://API.ANTHROPIC.COM:443/"} {
		s := configTestServer(t)
		inputs := property.NewMap(map[string]property.Value{"baseUrl": property.New(raw).WithSecret(true).WithDependencies([]urn.URN{"urn:pulumi:stack::project::test:index:Dependency::config"})})
		checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, "https://api.anthropic.com/", checked.Inputs.Get("baseUrl").AsString())
		require.True(t, checked.Inputs.Get("baseUrl").Secret())
		require.Equal(t, inputs.Get("baseUrl").Dependencies(), checked.Inputs.Get("baseUrl").Dependencies())
		again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Equal(t, checked.Inputs, again.Inputs)
		require.Equal(t, raw, inputs.Get("baseUrl").AsString())
	}
	for _, raw := range []string{"://invalid", "ftp://host", "https://private:secret@host/", "https://host/?secret=bad", "https://host/#secret"} {
		checked, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"baseUrl": property.New(raw)})})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
		require.Equal(t, "baseUrl", checked.Failures[0].Property)
		require.NotContains(t, checked.Failures[0].Reason, "secret")
		c := config.Config{BaseURL: raw}
		require.Error(t, c.Configure(t.Context()))
	}
	unknown := property.New(property.Computed).WithSecret(true)
	checked, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{
		"baseUrl": unknown, "workspaceId": unknown, "federation": unknown,
	})})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	for _, key := range []string{"baseUrl", "workspaceId", "federation"} {
		require.True(t, checked.Inputs.Get(key).IsComputed(), "%s: %#v", key, checked.Inputs.Get(key))
		require.True(t, checked.Inputs.Get(key).Secret(), key)
	}
}

func TestConfigurationBoundaryPolicy(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, tc := range []struct {
		name, key, old, next string
		boundary             bool
	}{
		{"workspace selection", "workspaceId", "wrkspc_a", "wrkspc_b", false},
		{"backend path", "baseUrl", "https://a.invalid/one/", "https://a.invalid/two/", true},
		{"credential source", "apiKey", "old", "new", false},
		{"add workspace selection", "workspaceId", "", "wrkspc_b", false},
		{"remove workspace selection", "workspaceId", "wrkspc_a", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := configTestServer(t)
			bag := func(v string) property.Map {
				return property.NewMap(map[string]property.Value{
					"workspaceId": property.New("wrkspc_a"), tc.key: property.New(v),
				})
			}
			old, err := s.CheckConfig(p.CheckRequest{Inputs: bag(tc.old)})
			require.NoError(t, err)
			next, err := s.CheckConfig(p.CheckRequest{Inputs: bag(tc.next)})
			require.NoError(t, err)
			d, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, OldInputs: old.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			kind := p.Update
			if tc.boundary {
				kind = p.UpdateReplace
			}
			require.Equal(t, kind, d.DetailedDiff[tc.key].Kind)
		})
	}
	// Unknown new values are possible updates, not evidence of retargeting.
	// Do not turn the typed decoder's zero value into a replacement.
	for _, key := range []string{"workspaceId", "baseUrl"} {
		s := configTestServer(t)
		old, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(nil)})
		require.NoError(t, err)
		next, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{key: property.New(property.Computed)})})
		require.NoError(t, err)
		d, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
		require.NoError(t, err)
		require.True(t, d.HasChanges)
		require.Equal(t, p.Update, d.DetailedDiff[key].Kind)
	}

	s := configTestServer(t)
	// Credential rotation remains an ordinary update.
	old, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"apiKey": property.New("old")})})
	require.NoError(t, err)
	next, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"apiKey": property.New("new")})})
	require.NoError(t, err)
	d, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
	require.NoError(t, err)
	require.Equal(t, p.Update, d.DetailedDiff["apiKey"].Kind)
}

func TestConfigurationGeneratedBetaAndClientIsolation(t *testing.T) {
	isolateConfigEnvironment(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token-must-not-leak")
	t.Setenv("ANTHROPIC_PROFILE", "unread-profile-must-not-load")
	var headers []http.Header
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"data":[],"has_more":false}`); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	c := config.Config{APIKey: "key-a", AdminKey: "key-b", AuthToken: "oauth-c", WorkspaceID: "wrkspc_test", BaseURL: api.URL}
	require.NoError(t, c.Configure(t.Context()))
	for _, plane := range []config.Plane{config.Workspace, config.Admin, config.OrgAdminOAuth, config.Workspace} {
		client, err := c.Client(plane)
		require.NoError(t, err)
		_, err = client.Beta.Agents.List(t.Context(), anthropic.BetaAgentListParams{})
		require.NoError(t, err)
	}
	for i, want := range []struct{ key, bearer string }{{"key-a", ""}, {"key-b", ""}, {"", "Bearer oauth-c"}, {"key-a", ""}} {
		require.Equal(t, want.bearer, headers[i].Get("Authorization"))
		require.Equal(t, want.key, headers[i].Get("x-api-key"), "API keys and OAuth use distinct authentication headers")
		beta := headers[i].Get("anthropic-beta")
		require.Contains(t, beta, "managed-agents-2026-04-01")
		require.Equal(t, i == 2, strings.Contains(beta, "oauth-2025-04-20"))
	}
	// A distinct provider cannot reuse another provider's credential/options.
	other := config.Config{AuthToken: "other", BaseURL: api.URL}
	require.NoError(t, other.Configure(t.Context()))
	client, err := other.Client(config.Workspace)
	require.NoError(t, err)
	require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil))
	require.Equal(t, "Bearer other", headers[4].Get("Authorization"))
	require.Empty(t, headers[4].Get("x-api-key"))
	require.Empty(t, headers[4].Get("anthropic-workspace-id"))
	require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithHeader("anthropic-beta", "beta-a,oauth-2025-04-20,beta-b,oauth-2025-04-20")))
	require.Equal(t, "beta-a,oauth-2025-04-20,beta-b", headers[5].Get("anthropic-beta"))
}

func TestConfiguredClientUsesExplicitEndpointAndClearsFailedConfiguration(t *testing.T) {
	isolateConfigEnvironment(t)
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if _, err := fmt.Fprint(w, "{}"); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	c := config.Config{APIKey: "synthetic-key", BaseURL: api.URL}
	require.NoError(t, c.Configure(t.Context()))
	client, err := c.Client(config.Workspace)
	require.NoError(t, err)
	require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil))
	require.Equal(t, 1, calls)

	c.BaseURL = "not a URL"
	require.Error(t, c.Configure(t.Context()))
	_, err = c.Client(config.Workspace)
	require.Error(t, err, "failed reconfiguration must not leave stale configured clients")
	require.Equal(t, 1, calls)
}
