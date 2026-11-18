//go:build acceptance

package test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCIUsesFederationIdentifiersWithoutStoredCredentials(t *testing.T) {
	for path, identifiers := range map[string][]string{
		"../.github/workflows/ci.yml": {
			"ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_WORKSPACE_ID",
			"ANTHROPIC_FEDERATION_SERVICE_ACCOUNT_ID", "ANTHROPIC_FEDERATION_RULE_ID_MAIN",
		},
		"../examples/github-actions-wif/workload/workflow.yml": {
			"ANTHROPIC_WIF_EXAMPLE_STACK", "ANTHROPIC_WIF_EXAMPLE_ORGANIZATION_ID",
			"ANTHROPIC_WIF_EXAMPLE_WORKSPACE_ID", "ANTHROPIC_WIF_EXAMPLE_SERVICE_ACCOUNT_ID",
			"ANTHROPIC_WIF_EXAMPLE_FEDERATION_RULE_ID",
		},
	} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		workflow := string(raw)
		require.NotContains(t, workflow, "secrets.ANTHROPIC_")
		require.Contains(t, workflow, "id-token: write")
		for _, name := range identifiers {
			require.Contains(t, workflow, "vars."+name)
		}
		if path == "../.github/workflows/ci.yml" {
			require.Contains(t, workflow, "vars.ANTHROPIC_FEDERATION_RULE_ID_PULL_REQUEST")
			require.Contains(t, workflow, "get-anthropic-oidc-token.sh")
			require.Contains(t, workflow, "make test_acceptance")
		}
	}
}
