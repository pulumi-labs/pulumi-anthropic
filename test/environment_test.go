//go:build acceptance

package test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareEnvironment(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, e := c.ResourceID("environment")
		require.NoError(t, e)
		sdk := client()
		_, e = sdk.Beta.Environments.Get(context.Background(), id, anthropic.BetaEnvironmentGetParams{})
		var apiError *anthropic.Error
		require.True(t, errors.As(e, &apiError))
		require.Equal(t, 404, apiError.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		providerRef := ""
		for _, state := range stack.Deployment.Resources {
			if state.Type == "pulumi:providers:anthropic" {
				providerRef = fmt.Sprintf("%s::%s", state.URN, state.ID)
			}
		}
		require.NotEmpty(t, providerRef)
		for _, state := range stack.Deployment.Resources {
			if state.Type == "anthropic:agents:Environment" {
				require.NotContains(t, state.Outputs, "state", "response-only platform state is deliberately unexposed")
				require.Equal(t, providerRef, state.Provider)
			}
		}
		id, e := c.ResourceID("environment")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.Environments.Get(context.Background(), id, anthropic.BetaEnvironmentGetParams{})
		require.NoError(t, e)
		clearing := observations == 1
		observations++
		require.Equal(t, c.RunName(), r.Name)
		require.Equal(t, "self_hosted", r.Config.Type)
		require.Equal(t, anthropic.BetaEnvironmentScopeOrganization, r.Scope)
		if clearing {
			require.Equal(t, r.Name, stack.Outputs["observedName"])
			require.Equal(t, "null", r.JSON.Description.Raw())
			require.Equal(t, map[string]string{"owner": "test"}, r.Metadata)
		} else {
			require.Equal(t, "initial description", r.Description)
			require.Equal(t, map[string]string{"owner": "test", "remove": "old"}, r.Metadata)
		}
		require.Equal(t, r.ID, stack.Outputs["environmentId"])
		require.Equal(t, r.Name, stack.Outputs["environmentName"])
		require.Equal(t, r.CreatedAt, stack.Outputs["createdAt"])
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:Environment" {
				require.Empty(t, s.InitErrors)
				require.Equal(t, r.Name, s.Outputs["name"])
				require.Equal(t, r.UpdatedAt, s.Outputs["updatedAt"])
				if clearing {
					require.Nil(t, s.Outputs["description"])
				} else {
					require.Equal(t, r.Description, s.Outputs["description"])
				}
			}
		}
	}
}
func TestLiveEnvironment(t *testing.T) {
	opts := liveOptions(t, "environment-lifecycle", prepareEnvironment)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/environment-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	t.Run("replacement", liveEnvironmentReplacement)
}

func liveEnvironmentReplacement(t *testing.T) {

	opts := liveOptions(t, "environment-replacement", prepareEnvironmentReplacement)
	opts.SkipRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/environment-replacement-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
func prepareEnvironmentReplacement(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	client := func() anthropic.Client { return c.WorkspaceClient(t) }
	t.Cleanup(func() {
		sdk := client()
		for index := range 2 {
			id, e := c.ResourceID("environment", index)
			require.NoError(t, e)
			_, e = sdk.Beta.Environments.Get(context.Background(), id, anthropic.BetaEnvironmentGetParams{})
			var apiError *anthropic.Error
			require.True(t, errors.As(e, &apiError))
			require.Equal(t, 404, apiError.StatusCode)
		}
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		suffix, variant := "first", "self_hosted"
		if observations == 1 {
			suffix, variant = "second", "cloud"
		}
		require.Less(t, observations, 2)
		id, e := c.ResourceID("environment")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.Environments.Get(context.Background(), id, anthropic.BetaEnvironmentGetParams{})
		require.NoError(t, e)
		require.Equal(t, c.RunName()+"-"+suffix, r.Name)
		require.Equal(t, variant, r.Config.Type)
		require.Equal(t, id, stack.Outputs["environmentId"])
		if observations == 1 {
			first, e := c.ResourceID("environment", 0)
			require.NoError(t, e)
			require.NotEqual(t, first, id)
			created, replaced, deleted := false, false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:agents:Environment" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("immutable type change executed in-place Update")
					case "create-replacement":
						require.False(t, deleted, "default create-before-delete ordering")
						require.Equal(t, "update-replace", string(pre.Metadata.DetailedDiff["config.type"].Kind))
						created = true
					case "replace":
						replaced = true
					case "delete-replaced":
						require.True(t, created)
						require.NotNil(t, pre.Metadata.Old)
						require.Equal(t, first, pre.Metadata.Old.ID)
						deleted = true
					}
				}
			}
			require.True(t, created && replaced && deleted, "missing structured engine replacement operations")
			require.Empty(t, r.Config.Networking.AllowedHosts)
			require.False(t, r.Config.Networking.AllowMCPServers)
			require.False(t, r.Config.Networking.AllowPackageManagers)
		}
		found := 0
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:Environment" {
				found++
				require.False(t, s.Delete)
				require.Equal(t, id, string(s.ID))
				require.Empty(t, s.InitErrors)
			}
		}
		require.Equal(t, 1, found)
		observations++
	}
}
