//go:build acceptance

package test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/test/internal/enginetest"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
)

type ScenarioContext = enginetest.Context

func liveOptions(t *testing.T, dir string, prepare ...enginetest.Prepare) integration.ProgramTestOptions {
	return enginetest.Options(t, dir, prepare...)
}
func program(t *testing.T, raw []byte) []byte { return enginetest.Program(t, raw) }

func requireBlockedScenario(t *testing.T, reason string) {
	t.Helper()
	enginetest.RequireBlockedLive(t, reason)
}

// A fresh public key is sufficient for configuration-only federation scenarios.
// The private key is discarded; these scenarios do not exchange federation tokens.
func freshIssuerPublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(public)
}

func issuerPublicKeys(t *testing.T, c *ScenarioContext) []map[string]any {
	t.Helper()
	x := c.Config("issuerKeyX")
	if x == "" {
		x = freshIssuerPublicKey(t)
		c.SetConfig(t, "issuerKeyX", x)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(x)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		t.Fatal("issuerKeyX must be a valid Ed25519 public key")
	}
	return []map[string]any{{"kty": "OKP", "crv": "Ed25519", "kid": c.RunName() + "-key", "x": x}}
}
