//go:build acceptance

package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/require"
)

func prepareTunnel(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	t.Cleanup(func() {
		for index := range 2 {
			id, err := c.ResourceID("tunnel", index)
			require.NoError(t, err)
			client := c.WorkspaceClient(t)
			r, err := client.Beta.Tunnels.Get(context.Background(), id, anthropic.BetaTunnelGetParams{})
			require.NoError(t, err)
			require.False(t, r.ArchivedAt.IsZero(), "independent SDK readback must observe archive")
		}
	})
	return func(t *testing.T, s integration.RuntimeValidationStackInfo) {
		for _, state := range s.Deployment.Resources {
			if state.Type != "anthropic:agents:Tunnel" {
				continue
			}
			require.Empty(t, state.InitErrors)
			client := c.WorkspaceClient(t)
			r, err := client.Beta.Tunnels.Get(context.Background(), string(state.ID), anthropic.BetaTunnelGetParams{})
			require.NoError(t, err)
			require.Equal(t, r.CreatedAt.Format(time.RFC3339), state.Outputs["createdAt"])
			require.Equal(t, r.Domain, state.Outputs["domain"])
			require.Equal(t, r.DisplayName, state.Outputs["displayName"])
		}
	}
}
func TestLiveTunnel(t *testing.T) {
	requireBlockedScenario(t, "requires verified Tunnels API access")
	var runName string
	sawExternal := false
	validate := func(t *testing.T, s integration.RuntimeValidationStackInfo) {
		found := false
		for _, r := range s.Deployment.Resources {
			if r.Type != "anthropic:agents:Tunnel" {
				continue
			}
			found = true
			require.NotEmpty(t, r.ID)
			require.NotEmpty(t, r.Outputs["domain"])

			if !r.External && r.Outputs["displayName"] == runName+"-replacement" {
				create, archive := false, false
				for _, event := range s.Events {
					if pre := event.ResourcePreEvent; pre != nil && pre.Metadata.Type == "anthropic:agents:Tunnel" {
						switch string(pre.Metadata.Op) {
						case "update":
							t.Fatal("immutable name must replace, not update")
						case "create-replacement":
							create = true
						case "delete-replaced":
							archive = true
						}
					}
				}
				require.True(t, create)
				require.True(t, archive)
			}
			if r.External {
				sawExternal = true
				// External get checkpoints do not retain Read's recovered inputs.
				require.Equal(t, runName+"-replacement", r.Outputs["displayName"])
			}
			require.Empty(t, r.InitErrors)
			token, ok := r.Outputs["tunnelToken"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, resource.SecretSig, token[string(resource.SigKey)])
		}
		require.True(t, found)
	}
	editDir := func(path string) string {
		dir := t.TempDir()
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		raw = program(t, raw)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
		return dir
	}
	opts := liveOptions(t, "tunnel-lifecycle", func(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
		runName = c.RunName()
		return prepareTunnel(t, c)
	})
	base := opts.ExtraRuntimeValidation
	opts.ExtraRuntimeValidation = func(t *testing.T, s integration.RuntimeValidationStackInfo) { base(t, s); validate(t, s) }
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: editDir("testdata/tunnel-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	require.True(t, sawExternal, "ID-only engine get must return a secret token and recovered name")
}
