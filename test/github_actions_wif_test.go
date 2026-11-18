//go:build acceptance

package test

import (
	"os"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/test/internal/enginetest"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/encoding"
	"github.com/stretchr/testify/require"
)

// Preview both programs using the real engine and installed provider, without up,
// real credentials, token commands or Anthropic requests.
func TestGitHubWIFExamplePreviews(t *testing.T) {
	for dir, inputs := range map[string]map[string]string{
		"configure": {
			"anthropic:authToken": "synthetic-preview-token",
			"organizationId":      "preview-org", "workspaceId": "wrkspc_preview",
			"githubSubject": "repo:example@123/demo@456:ref:refs/heads/main",
		},
		"workload": {
			"organizationId": "preview-org", "workspaceId": "wrkspc_preview",
			"federationRuleId": "fdrl_preview", "serviceAccountId": "svac_preview",
		},
	} {
		t.Run(dir, func(t *testing.T) {
			opts := enginetest.PreviewOptions(t, "../examples/github-actions-wif/"+dir, inputs)
			require.True(t, opts.SkipUpdate, "example checks must never run up")
			require.True(t, opts.SkipRefresh)
			require.True(t, opts.SkipExportImport)
			require.True(t, opts.SkipEmptyPreviewUpdate)
			require.Empty(t, opts.EditDirs)
			integration.ProgramTest(t, &opts)
		})
	}
}

// Check the two programs' handoff without credentials or platform operations.
func TestGitHubWIFExamplePrograms(t *testing.T) {
	type resource struct {
		Type       string         `yaml:"type"`
		Properties map[string]any `yaml:"properties"`
		Options    struct {
			Provider       string   `yaml:"provider"`
			DependsOn      []string `yaml:"dependsOn"`
			RetainOnDelete bool     `yaml:"retainOnDelete"`
		} `yaml:"options"`
	}
	type project struct {
		Config    map[string]map[string]any `yaml:"config"`
		Resources map[string]resource       `yaml:"resources"`
		Outputs   map[string]string         `yaml:"outputs"`
	}
	load := func(path string) project {
		t.Helper()
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var p project
		require.NoError(t, encoding.YAML.Unmarshal(raw, &p))
		return p
	}
	setup := load("../examples/github-actions-wif/configure/Pulumi.yaml")
	workload := load("../examples/github-actions-wif/workload/Pulumi.yaml")
	require.Len(t, setup.Resources, 4)
	issuer := setup.Resources["github"]
	require.Equal(t, "anthropic:identity:FederationIssuer", issuer.Type)
	require.Equal(t, "https://token.actions.githubusercontent.com", issuer.Properties["issuerUrl"])
	require.Equal(t, true, issuer.Properties["checkJti"])
	require.Equal(t, map[string]any{"type": "discovery"}, issuer.Properties["jwks"])
	require.True(t, issuer.Options.RetainOnDelete, "a shared issuer must not be archived with the workload's trust configuration")
	require.Equal(t, "developer", setup.Resources["deployer"].Properties["organizationRole"])
	access := setup.Resources["deployerAccess"]
	require.Equal(t, "anthropic:identity:ServiceAccountWorkspace", access.Type)
	require.Equal(t, "workspace_developer", access.Properties["workspaceRole"])
	require.Equal(t, "${workspaceId}", access.Properties["workspaceId"])
	require.Equal(t, "${deployer.id}", access.Properties["serviceAccountId"])
	rule := setup.Resources["workload"]
	require.Equal(t, "anthropic:identity:FederationRule", rule.Type)
	require.Equal(t, "${github.id}", rule.Properties["issuerId"])
	require.Equal(t, "workspace:developer", rule.Properties["oauthScope"])
	require.Equal(t, "${workspaceId}", rule.Properties["workspaceId"])
	require.Equal(t, map[string]any{"serviceAccountId": "${deployer.id}"}, rule.Properties["target"])
	require.Equal(t, map[string]any{
		"audience": "https://api.anthropic.com", "subjectPrefix": "${githubSubject}",
	}, rule.Properties["match"])
	require.Equal(t, []string{"${deployerAccess}"}, rule.Options.DependsOn)
	require.NotContains(t, setup.Config["githubSubject"], "default", "the exact subject must be supplied, not assumed")
	for name, value := range map[string]string{
		"organizationId": "${organizationId}", "workspaceId": "${workspaceId}",
		"federationRuleId": "${workload.id}", "serviceAccountId": "${deployer.id}",
	} {
		require.Equal(t, value, setup.Outputs[name])
		require.Contains(t, workload.Config, name)
	}
	provider := workload.Resources["anthropic"]
	require.Equal(t, "pulumi:providers:anthropic", provider.Type)
	require.Equal(t, "${workspaceId}", provider.Properties["workspaceId"])
	require.Equal(t, map[string]any{
		"organizationId": "${organizationId}", "ruleId": "${federationRuleId}",
		"serviceAccountId": "${serviceAccountId}",
		"identityToken":    map[string]any{"command": []any{"bash", "${pulumi.cwd}/get-github-oidc-token.sh"}},
	}, provider.Properties["federation"])
	require.NotContains(t, provider.Properties, "authToken")
	require.Equal(t, "${anthropic}", workload.Resources["vault"].Options.Provider)
	workflow, err := os.ReadFile("../examples/github-actions-wif/workload/workflow.yml")
	require.NoError(t, err)
	require.Contains(t, string(workflow), "working-directory: examples/github-actions-wif/workload")
}
