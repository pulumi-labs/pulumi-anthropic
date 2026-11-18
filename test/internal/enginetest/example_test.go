package enginetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/engine"
	"github.com/pulumi/pulumi/pkg/v3/secrets/passphrase"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/config"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceExampleHonorsSelectedFederation(t *testing.T) {
	for _, env := range []string{"ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_TOKEN_COMMAND"} {
		t.Run(env, func(t *testing.T) {
			oldLive, oldEnv := *liveExamples, *credentialEnv
			oldRule, oldAccount, oldOrg, oldWorkspace := *federationRuleID, *federationServiceAccountID, *organizationID, *workspaceID
			t.Cleanup(func() {
				*liveExamples, *credentialEnv = oldLive, oldEnv
				*federationRuleID, *federationServiceAccountID, *organizationID, *workspaceID = oldRule, oldAccount, oldOrg, oldWorkspace
			})
			*liveExamples, *credentialEnv = true, env
			*federationRuleID, *federationServiceAccountID = "fdrl_test", "svac_test"
			*organizationID, *workspaceID = "org_test", "wrkspc_test"
			t.Setenv("ANTHROPIC_API_KEY", "ambient-key-must-not-win")
			t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-token-must-not-leak")
			t.Setenv("ANTHROPIC_IDENTITY_TOKEN_FILE", filepath.Join(t.TempDir(), "identity.jwt"))
			t.Setenv("ANTHROPIC_IDENTITY_TOKEN_COMMAND", `["bash","/synthetic/get-token.sh"]`)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "synthetic-github-request-token")

			root := t.TempDir()
			prepared := false
			opts := integration.ProgramTestOptions{
				Dir:               root,
				PrePrepareProject: func(*engine.Projinfo) error { prepared = true; return nil },
			}
			ConfigureWorkspaceExample(t, &opts)
			require.Empty(t, opts.Secrets, "credentials must not enter logged config-set arguments")
			require.Contains(t, opts.Env, "ANTHROPIC_API_KEY=")
			require.Contains(t, opts.Env, "ANTHROPIC_AUTH_TOKEN=")
			require.NotContains(t, opts.Env, "ACTIONS_ID_TOKEN_REQUEST_TOKEN=", "the token command needs GitHub's runner environment")
			require.NoError(t, opts.PrePrepareProject(&engine.Projinfo{Root: root}))
			require.True(t, prepared, "existing example preparation must still run")
			path := filepath.Join(root, "Pulumi."+string(opts.GetStackName())+".yaml")
			stack, err := workspace.LoadProjectStack(nil, nil, path)
			require.NoError(t, err)
			federation := stack.Config[config.MustMakeKey("anthropic", "federation")]
			require.True(t, federation.Secure())
			manager, err := passphrase.GetPassphraseSecretsManager("correct horse battery staple", stack.EncryptionSalt)
			require.NoError(t, err)
			apiKey, err := stack.Config[config.MustMakeKey("anthropic", "apiKey")].Value(manager.Decrypter())
			require.NoError(t, err)
			require.Empty(t, apiKey, "ambient API key must not override explicitly selected federation")
			value, err := federation.Value(manager.Decrypter())
			require.NoError(t, err)
			var decoded map[string]any
			require.NoError(t, json.Unmarshal([]byte(value), &decoded))
			identityToken := map[string]any{"file": os.Getenv("ANTHROPIC_IDENTITY_TOKEN_FILE")}
			if env == "ANTHROPIC_IDENTITY_TOKEN_COMMAND" {
				identityToken = map[string]any{"command": []any{"bash", "/synthetic/get-token.sh"}}
			}
			require.Equal(t, map[string]any{
				"organizationId": "org_test", "ruleId": "fdrl_test", "identityToken": identityToken, "serviceAccountId": "svac_test",
			}, decoded)
			workspaceValue, err := stack.Config[config.MustMakeKey("anthropic", "workspaceId")].Value(config.NopDecrypter)
			require.NoError(t, err)
			require.Equal(t, "wrkspc_test", workspaceValue)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "ambient-key-must-not-win")
			require.NotContains(t, string(raw), "ambient-token-must-not-leak")
			require.NotContains(t, string(raw), "synthetic-github-request-token")
		})
	}
}
