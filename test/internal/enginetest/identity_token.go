package enginetest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Keep SDK readbacks independent of provider implementation code. Each SDK
// exchange invokes the same configured command, obtaining its own fresh assertion.
func (f federationCredentials) identityTokenProvider() option.IdentityTokenFunc {
	if len(f.IdentityTokenCommand) == 0 {
		return option.IdentityTokenFile(f.IdentityTokenFile)
	}
	argv := append([]string(nil), f.IdentityTokenCommand...)
	return func(ctx context.Context) (string, error) {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		var output assertionOutput
		cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = &output, io.Discard
		cmd.WaitDelay = time.Second
		err := cmd.Run()
		if runCtx.Err() != nil {
			return "", runCtx.Err()
		}
		if err != nil {
			return "", errors.New("SDK identity token command failed")
		}
		if output.overflow {
			return "", errors.New("SDK identity token command output exceeds 64 KiB")
		}
		token := strings.TrimSpace(output.buffer.String())
		if token == "" || strings.ContainsAny(token, " \t\r\n") {
			return "", errors.New("SDK identity token command must print one nonempty JWT")
		}
		return token, nil
	}
}

// Bound memory while continuing to drain a noisy child process until it exits
// or its timeout elapses. Do not embed bytes.Buffer: its ReadFrom bypasses Write.
type assertionOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *assertionOutput) Write(data []byte) (int, error) {
	count := len(data)
	remaining := 64*1024 - b.buffer.Len()
	if count > remaining {
		data = data[:remaining]
		b.overflow = true
	}
	_, _ = b.buffer.Write(data)
	return count, nil
}
