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

func prepareFederationIssuer(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	issuerURL := "https://" + c.RunName() + "-issuer.invalid"
	c.SetConfig(t, "issuerUrl", issuerURL)
	issuerPublicKeys(t, c)
	c.SetConfig(t, "editedIssuerKeyX", freshIssuerPublicKey(t))

	client := func() sdk.Client { return c.OAuthClient(t) }
	t.Cleanup(func() {
		s := client()
		id, err := c.ResourceID("issuer")
		require.NoError(t, err)
		r, err := s.Organization.Federation.Issuers.Get(context.Background(), id)
		require.NoError(t, err)
		require.False(t, r.ArchivedAt.IsZero())
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, ok := stack.Outputs["issuerId"].(string)
		require.True(t, ok)
		createdID, err := c.ResourceID("issuer")
		require.NoError(t, err)
		require.Equal(t, createdID, id)
		s := client()
		r, err := s.Organization.Federation.Issuers.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, r.Name, stack.Outputs["name"])
		require.Equal(t, r.IssuerURL, stack.Outputs["issuerUrl"])
		require.True(t, r.ArchivedAt.IsZero())
		require.Equal(t, "inline", r.JWKS.Type)
		require.Len(t, r.JWKS.Keys, 1)
		if r.Name == c.RunName()+"-renamed" {
			require.True(t, r.CheckJTI)
			require.Equal(t, int64(3600), r.MaxJWTLifetimeSeconds)
			require.Equal(t, c.Config("editedIssuerKeyX"), r.JWKS.Keys[0]["x"])
		} else {
			require.Equal(t, c.Config("issuerKeyX"), r.JWKS.Keys[0]["x"])
		}
	}
}
func issuerEdit(t *testing.T, path string, validate func(*testing.T, integration.RuntimeValidationStackInfo)) integration.EditDir {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = program(t, raw)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
	return integration.EditDir{Dir: dir, Additive: true, ExtraRuntimeValidation: validate}
}
func TestLiveFederationIssuer(t *testing.T) {
	opts := liveOptions(t, "federation-issuer-lifecycle", prepareFederationIssuer)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{issuerEdit(t, "testdata/federation-issuer-edit/Pulumi.yaml", opts.ExtraRuntimeValidation)}
	integration.ProgramTest(t, &opts)
}
