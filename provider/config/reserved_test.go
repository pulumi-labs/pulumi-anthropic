package config_test

import (
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Exercise the real provider.Provider() wrapper, not a test-only adapter.
// The engine's providers.Registry.Check forwards version and disallows changing
// its value. Other engine metadata is nested in __internal and filtered there.
func TestConfigurationReservedVersion(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::provider-version"}
	for _, tc := range []struct {
		name  string
		value property.Value
	}{
		{"known", property.New("0.1.0")},
		{"secret", property.New("0.1.0").WithSecret(true)},
		{"unknown", property.New(property.Computed)},
		{"secret unknown dependencies", property.New(property.Computed).WithSecret(true).WithDependencies(deps)},
		{"known secret dependencies", property.New("0.1.0").WithSecret(true).WithDependencies(deps)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := configTestServer(t)
			key := property.New(property.Computed).WithSecret(true).WithDependencies(deps)
			inputs := property.NewMap(map[string]property.Value{"version": tc.value, "apiKey": key, "baseUrl": property.New("https://API.ANTHROPIC.COM:443")})
			checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, tc.value, checked.Inputs.Get("version"))
			require.Equal(t, key, checked.Inputs.Get("apiKey"))
			require.Equal(t, "https://api.anthropic.com/", checked.Inputs.Get("baseUrl").AsString())
			again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Empty(t, again.Failures)
			require.Equal(t, checked.Inputs, again.Inputs)
			require.Equal(t, tc.value, inputs.Get("version"), "request must not be mutated")
			diff, err := s.DiffConfig(p.DiffRequest{State: checked.Inputs, OldInputs: checked.Inputs, Inputs: again.Inputs})
			require.NoError(t, err)
			require.False(t, diff.HasChanges)
			require.Empty(t, diff.DetailedDiff)
		})
	}
	s := configTestServer(t)
	old, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"version": property.New("0.1.0"), "apiKey": property.New("synthetic")})})
	require.NoError(t, err)
	require.Empty(t, old.Failures)
	next, err := s.CheckConfig(p.CheckRequest{Inputs: old.Inputs.Set("version", property.New("0.2.0"))})
	require.NoError(t, err)
	require.Empty(t, next.Failures)
	diff, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	require.NoError(t, s.Configure(p.ConfigureRequest{Args: next.Inputs})) // local only
	// Filtering version must not turn resource decoding into accept-anything.
	bad, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{"version": property.New("0.1.0"), "apiKeey": property.New("synthetic")})})
	require.NoError(t, err)
	require.NotEmpty(t, bad.Failures)
	require.Equal(t, "apiKeey", bad.Failures[0].Property)
}

func TestConfigurationUnknownBoundaryMetadataDiff(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::boundary"}
	for key, known := range map[string]string{"baseUrl": "https://different.invalid/prefix/", "workspaceId": "wrkspc_known"} {
		t.Run(key, func(t *testing.T) {
			s := configTestServer(t)
			inputs := property.NewMap(map[string]property.Value{"version": property.New("0.1.0"), key: property.New(known)})
			old, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, old.Failures)
			unknown := property.New(property.Computed).WithSecret(true).WithDependencies(deps)
			next, err := s.CheckConfig(p.CheckRequest{Inputs: inputs.Set(key, unknown)})
			require.NoError(t, err)
			require.Empty(t, next.Failures)
			require.Equal(t, unknown, next.Inputs.Get(key))
			d, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			require.Equal(t, p.Update, d.DetailedDiff[key].Kind)
			same, err := s.DiffConfig(p.DiffRequest{State: next.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			require.False(t, same.HasChanges)
			resolved, err := s.DiffConfig(p.DiffRequest{State: next.Inputs, Inputs: old.Inputs})
			require.NoError(t, err)
			require.Equal(t, p.Update, resolved.DetailedDiff[key].Kind)
			require.Equal(t, unknown, next.Inputs.Get(key), "Diff must not mutate metadata")
		})
	}
}

func TestConfigurationUnassertedCredentialRenewal(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, key := range []string{"apiKey", "adminKey", "authToken"} {
		t.Run(key, func(t *testing.T) {
			s := configTestServer(t)
			bag := func(v property.Value) property.Map {
				return property.NewMap(map[string]property.Value{"version": property.New("0.1.0"), key: v})
			}
			old, err := s.CheckConfig(p.CheckRequest{Inputs: bag(property.New("old"))})
			require.NoError(t, err)
			require.Empty(t, old.Failures)
			for _, value := range []property.Value{property.New("renewed"), property.New(property.Computed).WithSecret(true)} {
				next, err := s.CheckConfig(p.CheckRequest{Inputs: bag(value)})
				require.NoError(t, err)
				require.Empty(t, next.Failures)
				d, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
				require.NoError(t, err)
				require.True(t, d.HasChanges)
				require.Equal(t, p.Update, d.DetailedDiff[key].Kind)
			}
		})
	}
}
