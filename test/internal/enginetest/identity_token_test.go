package enginetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/stretchr/testify/require"
)

func TestCommandCredentialSelection(t *testing.T) {
	settings := federationCredentials{RuleID: "fdrl_test", OrganizationID: "org_test", WorkspaceID: "wrkspc_test", ServiceAccountID: "svac_test"}
	command := `["bash","/synthetic/path with spaces/get-token.sh"]`
	creds, err := selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_COMMAND", command, settings)
	require.NoError(t, err)
	require.Equal(t, []string{"bash", "/synthetic/path with spaces/get-token.sh"}, creds.Federation.IdentityTokenCommand)
	require.Empty(t, creds.Federation.IdentityTokenFile)
	var properties map[string]any
	require.NoError(t, json.Unmarshal([]byte(creds.properties()["federation"]), &properties))
	require.Equal(t, map[string]any{"command": []any{"bash", "/synthetic/path with spaces/get-token.sh"}}, properties["identityToken"])
	for _, invalid := range []string{"", "bash get-token.sh", "null", "[]", `[""]`, `[" "]`, `[1]`} {
		_, err := selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_COMMAND", invalid, settings)
		require.Error(t, err)
	}
	_, err = selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_COMMAND", command)
	require.Error(t, err)
	_, err = selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_COMMAND", command, federationCredentials{})
	require.Error(t, err)
}

func TestSDKCommandFederationFetchesFreshAssertions(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("GitHub OIDC example requires Bash")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("GitHub OIDC example requires jq")
	}
	oldWorkspaceID := *workspaceID
	*workspaceID = "wrkspc_test"
	t.Cleanup(func() { *workspaceID = oldWorkspaceID })
	for _, lifetime := range []int{10, 3600} {
		t.Run(strconv.Itoa(lifetime), func(t *testing.T) {
			root := t.TempDir()
			counter := filepath.Join(root, "count")
			curl := `#!/bin/sh
n=0
if [ -f "$FAKE_GITHUB_COUNTER" ]; then n=$(cat "$FAKE_GITHUB_COUNTER"); fi
n=$((n + 1))
printf '%s' "$n" > "$FAKE_GITHUB_COUNTER"
printf '{"value":"assertion-%s"}' "$n"
`
			require.NoError(t, os.WriteFile(filepath.Join(root, "curl"), []byte(curl), 0700))
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_GITHUB_COUNTER", counter)
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://github-oidc.example.invalid/token?api-version=2.0")
			t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "synthetic-github-request-token")
			script, err := filepath.Abs("../../../.github/scripts/get-anthropic-oidc-token.sh")
			require.NoError(t, err)
			raw, err := json.Marshal([]string{"bash", script})
			require.NoError(t, err)
			creds, err := selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_COMMAND", string(raw), federationCredentials{
				RuleID: "fdrl_test", OrganizationID: "org_test", WorkspaceID: "wrkspc_test", ServiceAccountID: "svac_test",
			})
			require.NoError(t, err)
			exchanges := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/oauth/token" {
					exchanges++
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, fmt.Sprintf("assertion-%d", exchanges), body["assertion"], "each exchange must receive a previously unused assertion")
					require.Equal(t, "wrkspc_test", body["workspace_id"])
					fmt.Fprintf(w, `{"access_token":"access-%d","expires_in":%d}`, exchanges, lifetime)
					return
				}
				require.Equal(t, fmt.Sprintf("Bearer access-%d", exchanges), r.Header.Get("Authorization"))
				require.Empty(t, r.Header.Get("anthropic-workspace-id"))
				fmt.Fprint(w, `{}`)
			}))
			defer api.Close()
			c := &Context{credentials: creds}
			client := c.WorkspaceClient(t)
			require.NoFileExists(t, counter, "client construction must not execute the command")
			for range 2 {
				require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithBaseURL(api.URL)))
			}
			want := 1
			if lifetime == 10 {
				want = 2
			}
			require.Equal(t, want, exchanges, "SDK caches access tokens, not the identity assertion")
			independent := c.WorkspaceClient(t)
			require.NoError(t, independent.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithBaseURL(api.URL)))
			require.Equal(t, want+1, exchanges, "independent clients must obtain separate assertions")
		})
	}
}

func TestSDKCommandErrorsDoNotExposeCredentials(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("command error fixture requires Bash")
	}
	for _, command := range [][]string{
		{filepath.Join(t.TempDir(), "private-command-path"), "private-argument"},
		{"bash", "-c", "echo private-token; echo private-stderr >&2; exit 7"},
		{"bash", "-c", "echo 'multiple tokens'"},
	} {
		_, err := (federationCredentials{IdentityTokenCommand: command}).identityTokenProvider()(t.Context())
		require.Error(t, err)
		for _, private := range []string{"private-command-path", "private-argument", "private-token", "private-stderr", "multiple tokens"} {
			require.NotContains(t, err.Error(), private)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (federationCredentials{IdentityTokenCommand: []string{"bash", "-c", "sleep 60"}}).identityTokenProvider()(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestAssertionOutputIsBounded(t *testing.T) {
	var output assertionOutput
	count, err := output.Write(make([]byte, 64*1024+1))
	require.NoError(t, err)
	require.Equal(t, 64*1024+1, count)
	require.Equal(t, 64*1024, output.buffer.Len())
	require.True(t, output.overflow)
	_, err = output.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 64*1024, output.buffer.Len())
}
