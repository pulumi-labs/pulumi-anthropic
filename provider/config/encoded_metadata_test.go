package config_test

import (
	"fmt"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// Synthetic Pulumi wire values, serialized by the official SDK. No files or
// credential sources are opened. Exercise both legacy secret/unknown signatures
// and dependency-bearing known/unknown outputs arriving as complex-config JSON.
func TestConfigurationEncodedFederationMetadata(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::assertion"}
	for _, unknown := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			for _, output := range []bool{false, true} {
				for _, parent := range []bool{false, true} {
					t.Run(fmt.Sprintf("unknown=%v/secret=%v/output=%v/parent=%v", unknown, secret, output, parent), func(t *testing.T) {
						leaf := property.New("/explicit/synthetic/nonexistent.jwt")
						if unknown {
							leaf = property.New(property.Computed)
						}
						leaf = leaf.WithSecret(secret)
						if output {
							leaf = leaf.WithDependencies(deps)
						}
						object := property.New(property.NewMap(map[string]property.Value{"organizationId": property.New("org-test"), "ruleId": property.New("fdrl_test"), "identityToken": property.New(map[string]property.Value{"file": leaf})}))
						if parent {
							object = object.WithSecret(true).WithDependencies(deps)
						}
						wire, err := plugin.MarshalProperties(resource.ToResourcePropertyValue(property.New(property.NewMap(map[string]property.Value{"federation": object}))).ObjectValue(), plugin.MarshalOptions{KeepUnknowns: true, KeepSecrets: true, KeepOutputValues: true})
						require.NoError(t, err)
						encoded, err := protojson.Marshal(wire.Fields["federation"])
						require.NoError(t, err)
						raw := property.New(string(encoded))
						if parent {
							raw = raw.WithSecret(true).WithDependencies([]urn.URN{"urn:pulumi:stack::project::test:index:Dependency::outer"})
						}
						inputs := property.NewMap(map[string]property.Value{"version": property.New("0.1.0"), "workspaceId": property.New("default"), "federation": raw})
						s := configTestServer(t)
						checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
						require.NoError(t, err)
						require.Empty(t, checked.Failures)
						got := checked.Inputs.Get("federation")
						require.Equal(t, parent, got.Secret())
						if parent {
							require.Equal(t, []urn.URN{deps[0], "urn:pulumi:stack::project::test:index:Dependency::outer"}, got.Dependencies())
						} else {
							require.Empty(t, got.Dependencies())
						}
						child := got.AsMap().Get("identityToken").AsMap().Get("file")
						require.Equal(t, unknown, child.IsComputed())
						require.True(t, got.AsMap().Get("identityToken").Secret())
						require.True(t, child.Secret())
						require.Equal(t, leaf.Dependencies(), child.Dependencies())
						if !unknown {
							require.Equal(t, leaf.AsString(), child.AsString())
						}
						again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
						require.NoError(t, err)
						require.Empty(t, again.Failures)
						require.Equal(t, checked.Inputs, again.Inputs)
						diff, err := s.DiffConfig(p.DiffRequest{State: checked.Inputs, Inputs: again.Inputs})
						require.NoError(t, err)
						require.False(t, diff.HasChanges)
						require.Equal(t, raw, inputs.Get("federation"), "Check must not mutate original encoded input")
					})
				}
			}
		}
	}
}

func TestConfigurationEncodedUnknownFederation(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::whole-federation"}
	unknown := property.New(property.Computed).WithSecret(true).WithDependencies(deps)
	wire, err := plugin.MarshalProperties(resource.ToResourcePropertyValue(property.New(property.NewMap(map[string]property.Value{"federation": unknown}))).ObjectValue(), plugin.MarshalOptions{KeepUnknowns: true, KeepSecrets: true, KeepOutputValues: true})
	require.NoError(t, err)
	encoded, err := protojson.Marshal(wire.Fields["federation"])
	require.NoError(t, err)
	s := configTestServer(t)
	inputs := property.NewMap(map[string]property.Value{"federation": property.New(string(encoded))})
	checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, unknown, checked.Inputs.Get("federation"))
	again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Empty(t, again.Failures)
	require.Equal(t, checked.Inputs, again.Inputs)
}

func TestConfigurationOrdinaryStringsStayLiteral(t *testing.T) {
	isolateConfigEnvironment(t)
	// These are literal provider strings, not JSON data or Pulumi signatures.
	for _, literal := range []string{`"literal-key"`, `{"4dabf18193072939515e22adb298388d":"unknown-signature"}`, plugin.UnknownStringValue, "null", "true"} {
		s := configTestServer(t)
		inputs := property.NewMap(map[string]property.Value{"apiKey": property.New(literal), "adminKey": property.New(literal), "authToken": property.New(literal), "workspaceId": property.New(literal)})
		checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		for _, key := range []string{"apiKey", "adminKey", "authToken", "workspaceId"} {
			require.Equal(t, literal, checked.Inputs.Get(key).AsString(), key)
		}
		again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Empty(t, again.Failures)
		require.Equal(t, checked.Inputs, again.Inputs)
	}
}

func TestConfigurationUnknownFederationTargetAddition(t *testing.T) {
	isolateConfigEnvironment(t)
	s := configTestServer(t)
	f := property.NewMap(map[string]property.Value{"organizationId": property.New("org-test"), "ruleId": property.New("fdrl_test"), "identityToken": property.New(map[string]property.Value{"file": property.New("/synthetic/nonexistent.jwt")})})
	inputs := property.NewMap(map[string]property.Value{"workspaceId": property.New("default"), "federation": property.New(f)})
	old, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, old.Failures)
	next, err := s.CheckConfig(p.CheckRequest{Inputs: inputs.Set("federation", property.New(f.Set("serviceAccountId", property.New(property.Computed))))})
	require.NoError(t, err)
	require.Empty(t, next.Failures)
	diff, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.Update, diff.DetailedDiff["federation"].Kind)
	metadataOnly := next.Inputs.Set("federation", property.New(next.Inputs.Get("federation").AsMap().Set("serviceAccountId", property.New(property.Computed).WithSecret(true).WithDependencies([]urn.URN{"urn:pulumi:stack::project::test:index:Dependency::target"}))))
	same, err := s.DiffConfig(p.DiffRequest{State: next.Inputs, Inputs: metadataOnly})
	require.NoError(t, err)
	require.False(t, same.HasChanges)
}

func TestConfigurationUnknownCredentialAddition(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::credential"}
	for _, key := range []string{"apiKey", "adminKey", "authToken", "federation"} {
		t.Run(key, func(t *testing.T) {
			s := configTestServer(t)
			old, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(nil)})
			require.NoError(t, err)
			require.Empty(t, old.Failures)
			unknown := property.New(property.Computed).WithSecret(true).WithDependencies(deps)
			next, err := s.CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{key: unknown})})
			require.NoError(t, err)
			require.Empty(t, next.Failures)
			require.Equal(t, unknown, next.Inputs.Get(key))
			diff, err := s.DiffConfig(p.DiffRequest{State: old.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			require.True(t, diff.HasChanges)
			require.Equal(t, p.Update, diff.DetailedDiff[key].Kind)
			same, err := s.DiffConfig(p.DiffRequest{State: next.Inputs, Inputs: next.Inputs})
			require.NoError(t, err)
			require.False(t, same.HasChanges)
			removed, err := s.DiffConfig(p.DiffRequest{State: next.Inputs, Inputs: old.Inputs})
			require.NoError(t, err)
			require.True(t, removed.HasChanges)
			require.Equal(t, p.Update, removed.DetailedDiff[key].Kind)
		})
	}
}
