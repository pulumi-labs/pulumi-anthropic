// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0
package provider

import (
	"fmt"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Input dependency annotations belong on the checked inputs. Each custom resource
// is itself a dependency-graph node; its returned properties must not forward those
// annotations. Secrecy and unknown preview values are independent obligations.
func TestCustomResourcePreviewsDoNotForwardInputDependencies(t *testing.T) {
	doc := loadSchema(t)
	for resourceType, spec := range doc.Resources {
		for _, secret := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/secret=%t", resourceType, secret), func(t *testing.T) {
				provider, err := Provider()
				require.NoError(t, err)
				server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"), integration.WithProvider(provider))
				require.NoError(t, err)
				u := resource.NewURN("test", "test", "", tokens.Type(resourceType), "preview")
				value := property.New(property.Computed).WithSecret(secret).WithDependencies([]urn.URN{"urn:pulumi:test::test::test:index:Dependency::input"})
				inputs := property.Map{}
				for key := range spec.InputProperties {
					inputs = inputs.Set(key, value)
				}
				checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				for key := range spec.InputProperties {
					require.True(t, value.Equals(checked.Inputs.Get(key)), "Check must preserve input annotations at %s", key)
				}
				preview, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs, DryRun: true})
				require.NoError(t, err)
				for key := range spec.InputProperties {
					actual := preview.Properties.Get(key)
					require.True(t, actual.IsComputed(), "preview must retain unknown input %s", key)
					if secret {
						require.True(t, actual.Secret(), "preview must protect secret input %s", key)
					}
				}
				assertNoOutputDependencies(t, property.New(preview.Properties), "outputs")
			})
		}
	}
}

func assertNoOutputDependencies(t *testing.T, value property.Value, path string) {
	t.Helper()
	require.Empty(t, value.Dependencies(), "custom-resource output forwards dependencies at %s", path)
	if value.IsMap() {
		for key, child := range value.AsMap().All {
			assertNoOutputDependencies(t, child, path+"."+key)
		}
	} else if value.IsArray() {
		for index, child := range value.AsArray().All {
			assertNoOutputDependencies(t, child, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}
