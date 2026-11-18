//go:build acceptance

package test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCIUsesFederationIdentifiersWithoutStoredCredentials(t *testing.T) {
	for _, path := range []string{"../.github/workflows/ci.yml", "../examples/github-actions-oidc/workflow.yml"} {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		workflow := string(raw)
		require.NotContains(t, workflow, "secrets.ANTHROPIC_")
		require.Contains(t, workflow, "id-token: write")
		for _, name := range []string{"ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_WORKSPACE_ID",
			"ANTHROPIC_FEDERATION_SERVICE_ACCOUNT_ID", "ANTHROPIC_FEDERATION_RULE_ID_MAIN"} {
			require.Contains(t, workflow, "vars."+name)
		}
		if path == "../.github/workflows/ci.yml" {
			require.Contains(t, workflow, "vars.ANTHROPIC_FEDERATION_RULE_ID_PULL_REQUEST")
			require.Contains(t, workflow, "get-anthropic-oidc-token.sh")
			require.Contains(t, workflow, "make test_acceptance")
		}
	}
}
