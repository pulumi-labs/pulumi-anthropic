//go:build acceptance

package test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareVault(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, e := c.ResourceID("vault")
		require.NoError(t, e)
		sdk := client()
		_, e = sdk.Beta.Vaults.Get(context.Background(), id, anthropic.BetaVaultGetParams{})
		var apiError *anthropic.Error
		require.True(t, errors.As(e, &apiError), "independent delete observation must establish absence")
		require.Equal(t, 404, apiError.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, e := c.ResourceID("vault")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.Vaults.Get(context.Background(), id, anthropic.BetaVaultGetParams{})
		require.NoError(t, e)
		clearing := observations == 1
		require.NoError(t, validateVaultEffect(r, c.RunName(), clearing))
		observations++
		require.Equal(t, id, r.ID)
		require.Equal(t, r.ID, stack.Outputs["vaultId"])
		require.Equal(t, r.DisplayName, stack.Outputs["displayName"])
		if clearing {
			require.Equal(t, r.DisplayName, stack.Outputs["observedName"])
			found := false
			for _, s := range stack.Deployment.Resources {
				if s.Type == "anthropic:agents:Vault" && s.External {
					found = true
					require.Equal(t, r.ID, string(s.ID))
					// Engine ReadStep adopts result.Outputs but not result.Inputs for
					// external get (step.go). Input recovery is tested at Read/import.
					require.Equal(t, map[string]any{"owner": "test"}, s.Outputs["metadata"])
				}
			}
			require.True(t, found, "YAML ID-only get must checkpoint an external resource with observed outputs")
		}
		require.Equal(t, r.CreatedAt.Format(time.RFC3339Nano), stack.Outputs["createdAt"])
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:Vault" {
				require.Empty(t, s.InitErrors)
				expected := map[string]any{}
				for k, v := range r.Metadata {
					expected[k] = v
				}
				require.Equal(t, expected, s.Outputs["metadata"])
			}
		}
	}
}
func validateVaultEffect(r *anthropic.BetaManagedAgentsVault, name string, clearing bool) error {
	expected := map[string]string{"owner": "test", "remove": "old"}
	if clearing {
		delete(expected, "remove")
	}
	if r == nil || name == "" || r.DisplayName != name || !maps.Equal(r.Metadata, expected) {
		return errors.New("independent vault observation differs from intended name/metadata")
	}
	return nil
}
func TestLiveVault(t *testing.T) {
	opts := liveOptions(t, "vault-lifecycle", prepareVault)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/vault-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
