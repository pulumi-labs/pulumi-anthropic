//go:build acceptance

package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareWorkspace(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() sdk.Client { return c.AdminClient(t) }
	t.Cleanup(func() {
		s := client()
		id, err := c.ResourceID("workspace")
		require.NoError(t, err)
		observed, err := s.Organization.Workspaces.Get(context.Background(), id)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero(), "the test workspace must be archived")
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, ok := stack.Outputs["workspaceId"].(string)
		require.True(t, ok)
		s := client()
		r, err := s.Organization.Workspaces.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, r.ID, id)
		require.Equal(t, r.Name, stack.Outputs["workspaceName"])
		require.Equal(t, r.DisplayColor, stack.Outputs["color"])
		require.True(t, r.ArchivedAt.IsZero())
		foundLookup := false
		for _, state := range stack.Deployment.Resources {
			if state.Type == "anthropic:organization:Workspace" && state.External {
				foundLookup = true
				require.Equal(t, id, string(state.ID))
				require.Empty(t, state.Inputs)
				require.Empty(t, state.InitErrors)
				require.Equal(t, r.Name, state.Outputs["name"])
				require.Empty(t, r.ExternalKeyID)
				require.Nil(t, state.Outputs["externalKeyId"])
			}
		}
		require.True(t, foundLookup, "get must read the still-live managed workspace")
		d := stack.Outputs["residency"].(map[string]any)
		require.Equal(t, string(r.DataResidency.WorkspaceGeo), d["workspaceGeo"])
		require.Equal(t, string(r.DataResidency.DefaultInferenceGeo), d["defaultInferenceGeo"])
		if r.Name == c.RunName()+"-edited" {
			require.Equal(t, `"unrestricted"`, r.DataResidency.AllowedInferenceGeos.RawJSON())
			require.Equal(t, "global", d["defaultInferenceGeo"])
			require.Equal(t, map[string]string{"keep": "yes-value"}, r.Tags)
		}
	}
}
func TestLiveWorkspace(t *testing.T) {
	edit := func(path string) string {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		raw = program(t, raw)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
		return dir
	}
	opts := liveOptions(t, "workspace-lifecycle", prepareWorkspace)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/workspace-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
