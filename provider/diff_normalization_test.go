// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0
package provider

import (
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

func TestDiffIgnoresGraphDependenciesWithoutChangingInputs(t *testing.T) {
	provider, err := Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"), integration.WithProvider(provider))
	require.NoError(t, err)
	u := resource.NewURN("test", "test", "", "anthropic:organization:User", "user")
	old := property.NewMap(map[string]property.Value{"userId": property.New("user_1"), "role": property.New("developer").WithSecret(true).WithDependencies([]urn.URN{"urn:old"})})
	for _, changed := range []bool{false, true} {
		role := "developer"
		if changed {
			role = "billing"
		}
		next := old.Set("role", property.New(role).WithSecret(true).WithDependencies([]urn.URN{"urn:new"}))
		state := old.Set("role", property.New("developer").WithSecret(true))
		diff, err := server.Diff(p.DiffRequest{Urn: u, ID: "user_1", State: state, OldInputs: old, Inputs: next})
		require.NoError(t, err)
		require.Equal(t, changed, diff.HasChanges, "only a platform-value change needs an update")
		require.Equal(t, []urn.URN{"urn:old"}, old.Get("role").Dependencies())
		require.Equal(t, []urn.URN{"urn:new"}, next.Get("role").Dependencies())
		require.True(t, next.Get("role").Secret())
	}
}

func TestDiffComparisonNormalizationPreservesValuesAndKnownness(t *testing.T) {
	dep := []urn.URN{"urn:dependency"}
	values := []property.Value{property.New(false), property.New(0.0), property.New(""), property.Value{}, property.New(property.Computed), property.New([]property.Value{}), property.New(property.Map{})}
	for _, value := range values {
		input := property.New(property.NewMap(map[string]property.Value{"items": property.New([]property.Value{value.WithSecret(true).WithDependencies(dep)}).WithSecret(true).WithDependencies(dep)})).WithDependencies(dep)
		got := diffWithoutDependencies(input)
		assertNoOutputDependencies(t, got, "comparison")
		leaf := got.AsMap().Get("items").AsArray().Get(0)
		require.True(t, leaf.Equals(value.WithSecret(true)))
		require.True(t, got.AsMap().Get("items").Secret())
		require.Equal(t, dep, input.Dependencies())
		require.Equal(t, dep, input.AsMap().Get("items").Dependencies())
		require.Equal(t, dep, input.AsMap().Get("items").AsArray().Get(0).Dependencies())
	}
}
