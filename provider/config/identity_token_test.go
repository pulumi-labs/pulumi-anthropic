package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

func TestIdentityTokenValidation(t *testing.T) {
	token, empty, relative := "literal-jwt", " ", "relative.jwt"
	file := filepath.Join(t.TempDir(), "not-opened.jwt")
	command, emptyCommand, blankCommand := []string{"not-executed", "arg"}, []string{}, []string{""}
	for _, tc := range []struct {
		name   string
		source IdentityToken
		valid  bool
	}{
		{"literal", IdentityToken{Token: &token}, true},
		{"file", IdentityToken{File: &file}, true},
		{"command", IdentityToken{Command: &command}, true},
		{"none", IdentityToken{}, false},
		{"token and file", IdentityToken{Token: &token, File: &file}, false},
		{"token and command", IdentityToken{Token: &token, Command: &command}, false},
		{"file and command", IdentityToken{File: &file, Command: &command}, false},
		{"all", IdentityToken{Token: &token, File: &file, Command: &command}, false},
		{"empty token", IdentityToken{Token: &empty}, false},
		{"relative file", IdentityToken{File: &relative}, false},
		{"empty command", IdentityToken{Command: &emptyCommand}, false},
		{"blank executable", IdentityToken{Command: &blankCommand}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.valid, len(validateIdentityToken(tc.source, nil)) == 0)
			c := Config{WorkspaceID: "wrkspc_test", Federation: &Federation{OrganizationID: "org_test", RuleID: "fdrl_test", IdentityToken: tc.source}}
			if tc.valid {
				require.NoError(t, c.Configure(t.Context()), "Configure must not read the file or execute the command")
			} else {
				require.Error(t, c.Configure(t.Context()), "Configure must also enforce the one-of contract")
			}
		})
	}
}

func TestIdentityTokenValidationUnknowns(t *testing.T) {
	unknown := property.New(property.Computed).WithSecret(true)
	for _, raw := range []property.Value{
		unknown,
		property.New(map[string]property.Value{"token": unknown}),
		property.New(map[string]property.Value{"file": unknown}),
		property.New(map[string]property.Value{"command": unknown}),
		property.New(map[string]property.Value{"token": unknown, "file": unknown}),
	} {
		require.Empty(t, validateIdentityToken(IdentityToken{}, &raw))
	}
	token, file := "jwt", filepath.Join(t.TempDir(), "assertion.jwt")
	raw := property.New(map[string]property.Value{"token": property.New(token), "file": property.New(file), "command": unknown})
	require.NotEmpty(t, validateIdentityToken(IdentityToken{Token: &token, File: &file}, &raw), "two known sources conflict even when a third is unknown")
	argv := []string{""}
	raw = property.New(map[string]property.Value{"command": property.New([]property.Value{unknown})})
	require.Empty(t, validateIdentityToken(IdentityToken{Command: &argv}, &raw), "unknown executable is not an empty executable")
}

func identityCommand(t *testing.T, mode string, args ...string) []string {
	t.Helper()
	t.Setenv("ANTHROPIC_IDENTITY_COMMAND_TEST", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	return append([]string{executable, "-test.run=^TestIdentityTokenCommandHelper$", "--", mode}, args...)
}

// Run only in a child test process; no shell or external CLI is needed.
func TestIdentityTokenCommandHelper(t *testing.T) {
	if os.Getenv("ANTHROPIC_IDENTITY_COMMAND_TEST") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "echo":
		fmt.Print(args[1])
	case "fail":
		if _, err := fmt.Fprint(os.Stdout, args[1]); err != nil {
			os.Exit(9)
		}
		if _, err := fmt.Fprint(os.Stderr, args[1]); err != nil {
			os.Exit(9)
		}
		os.Exit(7)
	case "oversize":
		fmt.Print(strings.Repeat("x", identityTokenOutputLimit+1))
	case "sleep":
		time.Sleep(time.Minute)
	case "sequence":
		raw, _ := os.ReadFile(args[1])
		n, _ := strconv.Atoi(string(raw))
		n++
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(n)), 0600); err != nil {
			os.Exit(9)
		}
		fmt.Printf("assertion-%d", n)
	}
	os.Exit(0)
}

func TestIdentityTokenCommandOutputAndErrors(t *testing.T) {
	for _, tc := range []struct{ name, mode, output, want, errorText string }{
		{"trim", "echo", " \nheader.payload.signature\t\n", "header.payload.signature", ""},
		{"no shell", "echo", "$HOME", "$HOME", ""},
		{"empty", "echo", " \n", "", "one nonempty JWT"},
		{"multiple lines", "echo", "jwt-one\njwt-two", "", "one nonempty JWT"},
		{"failure", "fail", "private-token-and-path", "", "command failed"},
		{"output limit", "oversize", "", "", "exceeds 64 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := identityCommand(t, tc.mode, tc.output)
			fn := (IdentityToken{Command: &argv}).provider()
			got, err := fn(t.Context())
			if tc.errorText == "" {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			} else {
				require.ErrorContains(t, err, tc.errorText)
				require.Empty(t, got)
				require.NotContains(t, err.Error(), "private-token-and-path")
				require.NotContains(t, err.Error(), argv[0])
			}
		})
	}
	argv := []string{filepath.Join(t.TempDir(), "sensitive-executable-path"), "sensitive-argument"}
	_, err := (IdentityToken{Command: &argv}).provider()(t.Context())
	require.ErrorContains(t, err, "command failed")
	require.NotContains(t, err.Error(), argv[0])
	require.NotContains(t, err.Error(), argv[1])
}

func TestIdentityTokenCommandTimeoutAndCancellation(t *testing.T) {
	argv := identityCommand(t, "sleep")
	start := time.Now()
	_, err := commandIdentityToken(argv, 50*time.Millisecond)(t.Context())
	require.ErrorContains(t, err, "timed out")
	require.Less(t, time.Since(start), 5*time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = commandIdentityToken(argv, time.Second)(ctx)
	require.True(t, errors.Is(err, context.Canceled))
}

func TestIdentityTokenLiteral(t *testing.T) {
	token := "literal-assertion"
	fn := (IdentityToken{Token: &token}).provider()
	for range 2 {
		got, err := fn(t.Context())
		require.NoError(t, err)
		require.Equal(t, token, got)
	}
}

func TestIdentityTokenCommandSDKCachingAndRenewal(t *testing.T) {
	for _, lifetime := range []int{10, 3600} {
		t.Run(strconv.Itoa(lifetime), func(t *testing.T) {
			counter := filepath.Join(t.TempDir(), "counter")
			argv := identityCommand(t, "sequence", counter)
			exchanges := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/oauth/token" {
					exchanges++
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, fmt.Sprintf("assertion-%d", exchanges), body["assertion"], "every exchange needs a fresh assertion")
					if _, err := fmt.Fprintf(w, `{"access_token":"access-%d","expires_in":%d}`, exchanges, lifetime); err != nil {
						t.Error(err)
					}
					return
				}
				require.Equal(t, fmt.Sprintf("Bearer access-%d", exchanges), r.Header.Get("Authorization"))
				if _, err := fmt.Fprint(w, `{}`); err != nil {
					t.Error(err)
				}
			}))
			defer api.Close()
			c := Config{BaseURL: api.URL, WorkspaceID: "wrkspc_test", Federation: &Federation{OrganizationID: "org_test", RuleID: "fdrl_test", IdentityToken: IdentityToken{Command: &argv}}}
			require.NoError(t, c.Configure(t.Context()))
			_, err := os.Stat(counter)
			require.True(t, os.IsNotExist(err), "Configure must not execute the command")
			client, err := c.Client(Workspace)
			require.NoError(t, err)
			for range 2 {
				require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithMaxRetries(0)))
			}
			want := 1
			if lifetime == 10 {
				want = 2
			}
			require.Equal(t, want, exchanges, "SDK caches access tokens until renewal is needed")
			// A newly configured provider has no cached access token and must fetch a new assertion.
			require.NoError(t, c.Configure(t.Context()))
			client, err = c.Client(Workspace)
			require.NoError(t, err)
			require.NoError(t, client.Execute(t.Context(), http.MethodGet, "v1/test", nil, nil, option.WithMaxRetries(0)))
			require.Equal(t, want+1, exchanges)
		})
	}
}
