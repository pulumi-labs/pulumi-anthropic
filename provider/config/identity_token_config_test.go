package config_test

import (
	"path/filepath"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

func TestIdentityTokenCheckMetadataAndUnknowns(t *testing.T) {
	isolateConfigEnvironment(t)
	deps := []urn.URN{"urn:pulumi:stack::project::test:index:Dependency::identity"}
	for _, unknown := range []bool{false, true} {
		for _, source := range []string{"token", "file", "command"} {
			t.Run(source+map[bool]string{false: "/known", true: "/unknown"}[unknown], func(t *testing.T) {
				leaf := property.New("synthetic-jwt")
				if source == "file" {
					leaf = property.New(filepath.Join(t.TempDir(), "not-read.jwt"))
				}
				if unknown {
					leaf = property.New(property.Computed)
				}
				leaf = leaf.WithSecret(true).WithDependencies(deps)
				value := leaf
				if source == "command" {
					value = property.New([]property.Value{property.New("not-executed"), leaf})
				}
				identity := property.New(map[string]property.Value{source: value})
				inputs := property.NewMap(map[string]property.Value{
					"workspaceId": property.New("wrkspc_test"),
					"federation": property.New(map[string]property.Value{
						"organizationId": property.New("org_test"), "ruleId": property.New("fdrl_test"), "identityToken": identity,
					}),
				})
				s := configTestServer(t)
				checked, err := s.CheckConfig(p.CheckRequest{Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				got := checked.Inputs.Get("federation").AsMap().Get("identityToken")
				require.True(t, got.Secret())
				child := got.AsMap().Get(source)
				if source == "command" {
					child = child.AsArray().Get(1)
				}
				require.Equal(t, leaf, child, "preserve unknownness, secrecy and dependencies on the logical value")
				again, err := s.CheckConfig(p.CheckRequest{Inputs: checked.Inputs})
				require.NoError(t, err)
				require.Empty(t, again.Failures)
				require.Equal(t, checked.Inputs, again.Inputs)
				var withoutMetadata property.Map
				if source == "command" {
					withoutMetadata = got.AsMap().Set(source, property.New([]property.Value{property.New("not-executed"), leaf.WithSecret(false).WithDependencies(nil)}))
				} else {
					withoutMetadata = got.AsMap().Set(source, leaf.WithSecret(false).WithDependencies(nil))
				}
				metadataOnly := checked.Inputs.Set("federation", property.New(checked.Inputs.Get("federation").AsMap().Set("identityToken", property.New(withoutMetadata))))
				diff, err := s.DiffConfig(p.DiffRequest{State: checked.Inputs, Inputs: metadataOnly})
				require.NoError(t, err)
				require.False(t, diff.HasChanges, "command argument metadata must not manufacture configuration changes")
				require.Equal(t, identity, inputs.Get("federation").AsMap().Get("identityToken"), "Check must not mutate inputs")
			})
		}
	}
}

func TestIdentityTokenCheckRequiresOneSource(t *testing.T) {
	isolateConfigEnvironment(t)
	for _, identity := range []property.Value{
		property.New(map[string]property.Value{}),
		property.New(map[string]property.Value{"token": property.New("jwt"), "file": property.New("/synthetic.jwt")}),
		property.New(map[string]property.Value{"command": property.New([]property.Value{})}),
		property.New(map[string]property.Value{"token": property.New("")}),
	} {
		checked, err := configTestServer(t).CheckConfig(p.CheckRequest{Inputs: property.NewMap(map[string]property.Value{
			"workspaceId": property.New("wrkspc_test"), "federation": property.New(map[string]property.Value{
				"organizationId": property.New("org_test"), "ruleId": property.New("fdrl_test"), "identityToken": identity,
			}),
		})})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
		require.Contains(t, checked.Failures[0].Property, "federation.identityToken")
	}
}
