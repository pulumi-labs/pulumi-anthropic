package config

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

const (
	identityTokenCommandTimeout   = 30 * time.Second
	identityTokenCommandWaitDelay = time.Second
	identityTokenOutputLimit      = 64 * 1024
)

// IdentityToken selects exactly one source of an OIDC assertion.
type IdentityToken struct {
	Token   *string   `pulumi:"token,optional" provider:"secret"`
	File    *string   `pulumi:"file,optional" provider:"secret"`
	Command *[]string `pulumi:"command,optional" provider:"secret"`
}

func (s *IdentityToken) Annotate(a infer.Annotator) {
	a.SetToken("index", "IdentityToken")
	a.Describe(s, "An OIDC identity token source. Specify exactly one of token, file or command.")
	a.Describe(&s.Token, "A literal OIDC JWT. It is reused for each exchange and cannot be renewed by the provider. A JWT with a jti claim can be exchanged only once when issuer replay protection is enabled.")
	a.Describe(&s.File, "Absolute path to an OIDC JWT file, reread whenever a new access token is needed. Rotate the file before each exchange if its JWT carries a single-use jti claim. The file contents are not saved in provider state.")
	a.Describe(&s.Command, "Executable followed by zero or more arguments, invoked whenever a new access token is needed. Must print one nonempty JWT to stdout, at most 64 KiB; surrounding whitespace is trimmed. Runs directly without a shell, inherits the provider process environment and working directory, and is stopped after 30 seconds or operation cancellation. Specify a shell explicitly for shell syntax. Command stdout and stderr are never logged or saved in provider state.")
}

func validateIdentityToken(source IdentityToken, raw *property.Value) []p.CheckFailure {
	var failures []p.CheckFailure
	fail := func(field, reason string) {
		failures = append(failures, p.CheckFailure{Property: "federation.identityToken" + field, Reason: reason})
	}
	if raw != nil && raw.IsComputed() {
		return failures
	}
	present, unknown := 0, false
	for _, field := range []struct {
		name string
		set  bool
	}{
		{"token", source.Token != nil}, {"file", source.File != nil}, {"command", source.Command != nil},
	} {
		if raw != nil && raw.IsMap() {
			v := raw.AsMap().Get(field.name)
			if v.IsComputed() {
				unknown = true
				continue
			}
			field.set = !v.IsNull()
		}
		if !field.set {
			continue
		}
		present++
		switch field.name {
		case "token":
			if source.Token != nil && strings.TrimSpace(*source.Token) == "" {
				fail(".token", "must not be empty")
			}
		case "file":
			if source.File != nil && !filepath.IsAbs(*source.File) {
				fail(".file", "an absolute identity token file path is required")
			}
		case "command":
			if source.Command == nil {
				continue
			}
			argv := *source.Command
			if len(argv) == 0 {
				fail(".command", "must contain an executable followed by optional arguments")
				continue
			}
			if raw != nil && raw.IsMap() {
				v := raw.AsMap().Get("command")
				if v.IsArray() && v.AsArray().Get(0).IsComputed() {
					continue
				}
			}
			if strings.TrimSpace(argv[0]) == "" {
				fail(".command", "executable must not be empty")
			}
		}
	}
	if present > 1 || (present == 0 && !unknown) {
		fail("", "specify exactly one of token, file or command")
	}
	return failures
}

// provider is lazy: Configure and CheckConfig must neither read a file nor run a command.
func (s IdentityToken) provider() option.IdentityTokenFunc {
	switch {
	case s.Token != nil:
		token := strings.TrimSpace(*s.Token)
		return func(context.Context) (string, error) { return token, nil }
	case s.File != nil:
		read := option.IdentityTokenFile(*s.File)
		return func(ctx context.Context) (string, error) {
			token, err := read(ctx)
			if err != nil {
				// SDK errors include the configured path, which may be sensitive.
				return "", fmt.Errorf("cannot read configured federation identity token file (missing, unreadable, or empty)")
			}
			return token, nil
		}
	default:
		argv := append([]string(nil), (*s.Command)...)
		return commandIdentityToken(argv, identityTokenCommandTimeout)
	}
}

// boundedTokenOutput discards excess stdout without allocating unbounded memory.
// Keep draining it so a noisy command cannot block on a full pipe before timeout.
type boundedTokenOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *boundedTokenOutput) Write(data []byte) (int, error) {
	count := len(data)
	remaining := identityTokenOutputLimit - b.buffer.Len()
	if count > remaining {
		data = data[:remaining]
		b.overflow = true
	}
	_, _ = b.buffer.Write(data)
	return count, nil
}

func commandIdentityToken(argv []string, timeout time.Duration) option.IdentityTokenFunc {
	return func(ctx context.Context) (string, error) {
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		var stdout boundedTokenOutput
		cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = &stdout, io.Discard
		cmd.WaitDelay = identityTokenCommandWaitDelay
		err := cmd.Run()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if runCtx.Err() != nil {
			return "", fmt.Errorf("federation identity token command timed out")
		}
		if err != nil {
			// Command paths, arguments and process errors can contain credentials.
			return "", fmt.Errorf("federation identity token command failed")
		}
		if stdout.overflow {
			return "", fmt.Errorf("federation identity token command output exceeds 64 KiB")
		}
		token := strings.TrimSpace(stdout.buffer.String())
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return "", fmt.Errorf("federation identity token command must print one nonempty JWT")
		}
		return token, nil
	}
}
