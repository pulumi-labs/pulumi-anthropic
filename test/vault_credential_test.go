//go:build acceptance

package test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/require"
)

func prepareVaultCredential(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		v, e := c.ResourceID("vault")
		require.NoError(t, e)
		id, e := c.ResourceID("credential")
		require.NoError(t, e)
		id = strings.TrimPrefix(id, v+"/")
		sdk := client()
		_, e = sdk.Beta.Vaults.Credentials.Get(context.Background(), id, anthropic.BetaVaultCredentialGetParams{VaultID: v})
		var ae *anthropic.Error
		require.True(t, errors.As(e, &ae))
		require.Equal(t, 404, ae.StatusCode)
		parent := client()
		_, e = parent.Beta.Vaults.Get(context.Background(), v, anthropic.BetaVaultGetParams{})
		require.True(t, errors.As(e, &ae))
		require.Equal(t, 404, ae.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		v, e := c.ResourceID("vault")
		require.NoError(t, e)
		id, e := c.ResourceID("credential")
		require.NoError(t, e)
		id = strings.TrimPrefix(id, v+"/")
		sdk := client()
		r, e := sdk.Beta.Vaults.Credentials.Get(context.Background(), id, anthropic.BetaVaultCredentialGetParams{VaultID: v})
		require.NoError(t, e)
		require.Equal(t, "environment_variable", r.Auth.Type)
		expectedName := "PULUMI_TEST_SECRET"
		require.Equal(t, expectedName, r.Auth.SecretName)
		require.True(t, r.Auth.InjectionLocation.Header)
		require.Equal(t, observations == 0, r.Auth.InjectionLocation.Body)
		expected := map[string]string{"keep": "yes", "remove": "old"}
		if observations > 0 {
			delete(expected, "remove")
		}
		require.Equal(t, expected, r.Metadata)
		require.Equal(t, v+"/"+id, stack.Outputs["credentialId"])
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:VaultCredential" {
				require.Empty(t, s.InitErrors)
				a := s.Outputs["auth"].(map[string]any)
				if !s.External {
					secret := a["secretValue"].(map[string]any)
					require.Equal(t, resource.SecretSig, secret[resource.SigKey])
					require.NotEmpty(t, secret["ciphertext"], "checkpoint must encrypt the write-only value")
				} else {
					require.NotContains(t, a, "secretValue")
				}
			}
		}
		observations++
	}
}
func TestLiveVaultCredential(t *testing.T) {
	opts := liveOptions(t, "vault-credential-lifecycle", prepareVaultCredential)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/vault-credential-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	t.Run("replacement", liveVaultCredentialReplacement)
}

func liveVaultCredentialReplacement(t *testing.T) {

	opts := liveOptions(t, "vault-credential-replacement", prepareVaultCredentialReplacement)
	opts.SkipRefresh = true // Refresh/import are covered by the unchanged lifecycle case.
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/vault-credential-replacement-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
func prepareVaultCredentialReplacement(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	client := func() anthropic.Client { return c.WorkspaceClient(t) }
	assertAbsent := func(t *testing.T, vault, id string) {
		sdk := client()
		_, e := sdk.Beta.Vaults.Credentials.Get(context.Background(), id, anthropic.BetaVaultCredentialGetParams{VaultID: vault})
		var apiErr *anthropic.Error
		require.True(t, errors.As(e, &apiErr))
		require.Equal(t, 404, apiErr.StatusCode)
	}
	t.Cleanup(func() {
		v, e := c.ResourceID("vault")
		require.NoError(t, e)
		for index := range 2 {
			id, e := c.ResourceID("credential", index)
			require.NoError(t, e)
			id = strings.TrimPrefix(id, v+"/")
			assertAbsent(t, v, id)
		}
		parent := client()
		_, err := parent.Beta.Vaults.Get(context.Background(), v, anthropic.BetaVaultGetParams{})
		var apiError *anthropic.Error
		require.ErrorAs(t, err, &apiError)
		require.Equal(t, 404, apiError.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		require.Less(t, observations, 2)
		v, e := c.ResourceID("vault")
		require.NoError(t, e)
		suffix, url := "first", "https://first.example.com/mcp"
		if observations == 1 {
			suffix, url = "second", "https://second.example.com/mcp"
		}
		id, e := c.ResourceID("credential")
		require.NoError(t, e)
		id = strings.TrimPrefix(id, v+"/")
		sdk := client()
		r, e := sdk.Beta.Vaults.Credentials.Get(context.Background(), id, anthropic.BetaVaultCredentialGetParams{VaultID: v})
		require.NoError(t, e)
		require.Equal(t, id, r.ID)
		require.Equal(t, c.RunName()+"-"+suffix, r.DisplayName)
		require.Equal(t, "static_bearer", r.Auth.Type)
		require.Equal(t, url, r.Auth.MCPServerURL)
		require.Equal(t, v+"/"+id, stack.Outputs["credentialId"])
		found := 0
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:VaultCredential" {
				found++
				require.Empty(t, s.InitErrors)
				require.False(t, s.Delete)
				require.Equal(t, v+"/"+id, string(s.ID))
				require.Equal(t, r.DisplayName, s.Inputs["displayName"])
				require.Equal(t, r.DisplayName, s.Outputs["displayName"])
				a := s.Outputs["auth"].(map[string]any)
				require.Equal(t, url, a["mcpServerUrl"])
				token := a["token"].(map[string]any)
				require.Equal(t, resource.SecretSig, token[resource.SigKey])
				require.NotEmpty(t, token["ciphertext"])
			}
		}
		require.Equal(t, 1, found, "replacement must leave exactly one active checkpoint resource")
		if observations == 1 {
			first, e := c.ResourceID("credential", 0)
			require.NoError(t, e)
			first = strings.TrimPrefix(first, v+"/")
			require.NotEqual(t, first, id)
			require.NoError(t, validateVaultCredentialReplacementEvents(stack.Events, v+"/"+first, v+"/"+id))
			assertAbsent(t, v, first)
		}
		observations++
	}
}

// Assert the real engine's structured operations, not CLI text or a resource's
// Diff callback alone. Require deletion of the first identity before replacement allocation.
func validateVaultCredentialReplacementEvents(events []apitype.EngineEvent, first, second string) error {
	deleted, created, replaced, completed := false, false, false, false
	if first == second {
		return errors.New("replacement reused the first allocation identity")
	}
	for _, event := range events {
		if pre := event.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:agents:VaultCredential" {
			m := pre.Metadata
			switch string(m.Op) {
			case "update":
				return errors.New("immutable edit executed in-place Update")
			case "delete-replaced":
				if deleted || created || m.Old == nil || m.Old.ID != first {
					return errors.New("replacement deletion did not precede allocation of a distinct identity")
				}
				deleted = true
			case "create-replacement":
				if !deleted || created {
					return errors.New("replacement allocation did not follow deletion")
				}
				created = true
				// Changing the MCP URL, not the mutable display name, must cause replacement.
				hasKey := false
				for _, key := range m.Keys {
					if key == "auth" {
						hasKey = true
					}
				}
				// pulumi-go-provider v1.5 provider.go:240 reduces Replaces to
				// the first path segment, retaining leaf paths in DetailedDiff.
				if !hasKey || m.DetailedDiff["auth.mcpServerUrl"].Kind != apitype.DiffUpdateReplace || m.DetailedDiff["displayName"].Kind != apitype.DiffUpdate {
					return errors.New("replacement cause does not identify immutable MCP URL versus mutable display name")
				}
			case "replace":
				replaced = true
			}
		}
		if output := event.ResOutputsEvent; output != nil && !output.Planning && output.Metadata.Type == "anthropic:agents:VaultCredential" && string(output.Metadata.Op) == "create-replacement" {
			if output.Metadata.New == nil || output.Metadata.New.ID != second {
				return errors.New("replacement result did not checkpoint second identity")
			}
			completed = true
		}
	}
	if !deleted || !created || !replaced || !completed {
		return errors.New("missing actual-engine replacement operations")
	}
	return nil
}
