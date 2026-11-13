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

func prepareServiceAccount(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() sdk.Client { return c.OAuthClient(t) }
	t.Cleanup(func() {
		s := client()
		for index := range 2 {
			id, err := c.ResourceID("account", index)
			require.NoError(t, err)
			r, err := s.Organization.ServiceAccounts.Get(context.Background(), id)
			require.NoError(t, err)
			require.False(t, r.ArchivedAt.IsZero())
		}
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, ok := stack.Outputs["accountId"].(string)
		require.True(t, ok)
		s := client()
		r, err := s.Organization.ServiceAccounts.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, r.Name, stack.Outputs["name"])
		for _, state := range stack.Deployment.Resources {
			if state.Type == "anthropic:identity:ServiceAccount" {
				require.Empty(t, state.InitErrors)
				if state.External {
					require.Equal(t, r.Name, state.Outputs["name"])
					require.Equal(t, "developer", state.Outputs["organizationRole"])
				}
				require.Equal(t, r.Name, state.Outputs["name"])
				if state.Inputs["description"] == nil {
					require.Equal(t, "", r.Description)
				}
			}
		}
	}
}
func serviceAccountEdit(t *testing.T, path string, validate func(*testing.T, integration.RuntimeValidationStackInfo)) integration.EditDir {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = program(t, raw)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
	return integration.EditDir{Dir: dir, Additive: true, ExtraRuntimeValidation: validate}
}
func TestLiveServiceAccount(t *testing.T) {
	opts := liveOptions(t, "service-account-lifecycle", prepareServiceAccount)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{serviceAccountEdit(t, "testdata/service-account-edit/Pulumi.yaml", opts.ExtraRuntimeValidation), serviceAccountEdit(t, "testdata/service-account-replacement/Pulumi.yaml", opts.ExtraRuntimeValidation)}
	integration.ProgramTest(t, &opts)
}
