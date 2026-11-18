//go:build acceptance

package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Exercise the CI and example token commands independently with a fake curl.
// No GitHub or Anthropic request is made and no real credentials are needed.
func githubOIDCCommand(t *testing.T, source string) func() ([]byte, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("GitHub OIDC token command requires Bash")
	}
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("GitHub OIDC token command requires jq")
	}
	root := t.TempDir()
	curl := `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_GITHUB_DIR/request"
case "$FAKE_GITHUB_MODE" in
  fail) exit 22 ;;
  malformed) printf 'not-json'; exit 0 ;;
  empty) printf '{"value":""}'; exit 0 ;;
  missing) printf '{}'; exit 0 ;;
esac
n=0
if [ -f "$FAKE_GITHUB_DIR/count" ]; then n=$(cat "$FAKE_GITHUB_DIR/count"); fi
n=$((n + 1))
printf '%s' "$n" > "$FAKE_GITHUB_DIR/count"
printf '{"value":"synthetic-github-token-%s"}' "$n"
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "curl"), []byte(curl), 0700))
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GITHUB_DIR", root)
	t.Setenv("FAKE_GITHUB_MODE", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://github-oidc.example.invalid/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "synthetic-runner-request-token")
	script, err := filepath.Abs(source)
	require.NoError(t, err)
	return func() ([]byte, error) {
		return exec.Command(bash, script).Output()
	}
}

func TestGitHubOIDCTokenScripts(t *testing.T) {
	for _, source := range []struct{ name, path string }{
		{"CI", "../.github/scripts/get-anthropic-oidc-token.sh"},
		{"example", "../examples/github-actions-oidc/get-github-oidc-token.sh"},
	} {
		t.Run(source.name, func(t *testing.T) {
			t.Run("fresh assertions", func(t *testing.T) { testGitHubOIDCFreshAssertions(t, source.path) })
			t.Run("invalid responses", func(t *testing.T) { testGitHubOIDCInvalidResponses(t, source.path) })
			t.Run("missing credentials", func(t *testing.T) { testGitHubOIDCMissingCredentials(t, source.path) })
		})
	}
}

func testGitHubOIDCFreshAssertions(t *testing.T, source string) {
	t.Helper()
	run := githubOIDCCommand(t, source)
	for _, want := range []string{"synthetic-github-token-1", "synthetic-github-token-2"} {
		output, err := run()
		require.NoError(t, err)
		require.Equal(t, want, strings.TrimSpace(string(output)))
		require.NotContains(t, string(output), "synthetic-runner-request-token")
	}
	request, err := os.ReadFile(filepath.Join(os.Getenv("FAKE_GITHUB_DIR"), "request"))
	require.NoError(t, err)
	for _, argument := range []string{
		"--fail", "--connect-timeout", "--max-time",
		"Authorization: Bearer synthetic-runner-request-token",
		"https://github-oidc.example.invalid/token?api-version=2.0&audience=https%3A%2F%2Fapi.anthropic.com",
	} {
		require.Contains(t, string(request), argument)
	}
}

func testGitHubOIDCInvalidResponses(t *testing.T, source string) {
	t.Helper()
	for _, mode := range []string{"fail", "malformed", "empty", "missing"} {
		t.Run(mode, func(t *testing.T) {
			run := githubOIDCCommand(t, source)
			t.Setenv("FAKE_GITHUB_MODE", mode)
			output, err := run()
			require.Error(t, err)
			require.Empty(t, output)
		})
	}
}

func testGitHubOIDCMissingCredentials(t *testing.T, source string) {
	t.Helper()
	for _, variable := range []string{"ACTIONS_ID_TOKEN_REQUEST_URL", "ACTIONS_ID_TOKEN_REQUEST_TOKEN"} {
		t.Run(variable, func(t *testing.T) {
			run := githubOIDCCommand(t, source)
			t.Setenv(variable, "")
			output, err := run()
			require.Error(t, err)
			require.Empty(t, output)
			_, err = os.Stat(filepath.Join(os.Getenv("FAKE_GITHUB_DIR"), "request"))
			require.True(t, os.IsNotExist(err), "must fail before attempting a token request")
		})
	}
}
