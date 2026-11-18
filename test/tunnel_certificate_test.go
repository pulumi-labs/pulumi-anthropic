//go:build acceptance

package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareTunnelCertificate(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	for _, name := range []string{"firstTunnelId", "secondTunnelId"} {
		setup := c.WorkspaceClient(t)
		tunnel, err := setup.Beta.Tunnels.New(t.Context(), anthropic.BetaTunnelNewParams{DisplayName: anthropic.String(c.RunName() + "-" + name)})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup := c.WorkspaceClient(t)
			_, err := cleanup.Beta.Tunnels.Archive(context.Background(), tunnel.ID, anthropic.BetaTunnelArchiveParams{})
			require.NoError(t, err)
			read := c.WorkspaceClient(t)
			observed, err := read.Beta.Tunnels.Get(context.Background(), tunnel.ID, anthropic.BetaTunnelGetParams{})
			require.NoError(t, err)
			require.False(t, observed.ArchivedAt.IsZero())
		})
		c.SetConfig(t, name, tunnel.ID)
	}

	get := func(t *testing.T, id string) *anthropic.BetaTunnelCertificate {
		parts := strings.Split(id, "/")
		require.Len(t, parts, 2)
		client := c.WorkspaceClient(t)
		r, e := client.Beta.Tunnels.Certificates.Get(context.Background(), parts[1], anthropic.BetaTunnelCertificateGetParams{TunnelID: parts[0]})
		require.NoError(t, e)
		return r
	}
	t.Cleanup(func() {
		for index := range 2 {
			id, e := c.ResourceID("certificate", index)
			require.NoError(t, e)
			r := get(t, id)
			require.False(t, r.ArchivedAt.IsZero())
		}
	})
	return func(t *testing.T, s integration.RuntimeValidationStackInfo) {
		for _, r := range s.Deployment.Resources {
			if r.Type != "anthropic:agents:TunnelCertificate" {
				continue
			}
			require.Empty(t, r.InitErrors)
			v := get(t, string(r.ID))
			require.Equal(t, v.Fingerprint, r.Outputs["fingerprint"])
			require.Equal(t, v.TunnelID, r.Outputs["tunnelId"])
		}
	}
}
func TestLiveTunnelCertificate(t *testing.T) {
	requireBlockedScenario(t, "requires verified Tunnels API access to create the parent tunnel")
	var secondTunnelID string
	external := false
	validate := func(t *testing.T, s integration.RuntimeValidationStackInfo) {
		for _, r := range s.Deployment.Resources {
			if r.Type != "anthropic:agents:TunnelCertificate" {
				continue
			}
			require.NotEmpty(t, r.ID)
			if r.External {
				external = true
				require.Nil(t, r.Outputs["caCertificatePem"])
				require.Equal(t, secondTunnelID, r.Outputs["tunnelId"])
				continue
			}
			require.NotEmpty(t, r.Outputs["caCertificatePem"])
			require.Empty(t, r.InitErrors)
			require.NotEmpty(t, r.Outputs["fingerprint"])
			require.Equal(t, "active", r.Outputs["status"])
			require.Equal(t, "", r.Outputs["expiresAt"])
			if r.Outputs["tunnelId"] == secondTunnelID {
				created, archived := false, false
				for _, event := range s.Events {
					if pre := event.ResourcePreEvent; pre != nil && pre.Metadata.Type == "anthropic:agents:TunnelCertificate" {
						switch string(pre.Metadata.Op) {
						case "update":
							t.Fatal("immutable parent must replace")
						case "create-replacement":
							created = true
						case "delete-replaced":
							archived = true
						}
					}
				}
				require.True(t, created)
				require.True(t, archived)
			}
		}
	}
	edit := func(path string) string {
		d := t.TempDir()
		b, e := os.ReadFile(path)
		require.NoError(t, e)
		b = program(t, b)
		require.NoError(t, os.WriteFile(filepath.Join(d, "Pulumi.yaml"), b, 0600))
		return d
	}
	opts := liveOptions(t, "tunnel-certificate-lifecycle", func(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
		validate := prepareTunnelCertificate(t, c)
		secondTunnelID = c.Config("secondTunnelId")
		return validate
	})
	base := opts.ExtraRuntimeValidation
	opts.ExtraRuntimeValidation = func(t *testing.T, s integration.RuntimeValidationStackInfo) { base(t, s); validate(t, s) }
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/tunnel-certificate-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	require.True(t, external)
}
