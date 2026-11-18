package enginetest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
)

func TestCredentialPlanes(t *testing.T) {
	for _, test := range []struct{ env, property string }{{"ANTHROPIC_API_KEY", "apiKey"}, {"ANTHROPIC_ADMIN_KEY", "adminKey"}, {"ANTHROPIC_ADMIN_OAUTH_KEY", "authToken"}} {
		got, err := selectedCredentials(test.env, "unit-credential")
		if err != nil {
			t.Fatal(err)
		}
		for property, value := range got.properties() {
			want := ""
			if property == "federation" {
				want = "null"
			} else if property == test.property {
				want = "unit-credential"
			}
			if value != want {
				t.Fatal("credential mapped to wrong plane", property)
			}
		}
	}
	if _, err := selectedCredentials("OTHER_ENV", "unit-credential"); err == nil {
		t.Fatal("unapproved credential source accepted")
	}
	if _, err := selectedCredentials("ANTHROPIC_API_KEY", ""); err == nil {
		t.Fatal("missing credential accepted")
	}
	identity := filepath.Join(t.TempDir(), "identity.jwt")
	federation := federationCredentials{RuleID: "fdrl_test", OrganizationID: "org_test", WorkspaceID: "wrkspc_test", ServiceAccountID: "svac_test"}
	got, err := selectedCredentials("ANTHROPIC_IDENTITY_TOKEN_FILE", identity, federation)
	if err != nil || got.Federation == nil {
		t.Fatal("federation credential rejected", err)
	}
	if got.Federation.IdentityTokenFile != identity || !strings.Contains(got.properties()["federation"], "fdrl_test") {
		t.Fatal("federation credential mapped incorrectly")
	}
}

func TestCheckpointIdentitiesTrackManagedReplacementHistory(t *testing.T) {
	c := &Context{ids: map[string][]string{}, runName: "unit-run"}
	state := func(name, id string, external, deleted bool) apitype.ResourceV3 {
		return apitype.ResourceV3{URN: resource.URN("urn:pulumi:test::project::anthropic:agents:VaultCredential::" + name), Type: "anthropic:agents:VaultCredential", ID: resource.ID(id), External: external, Delete: deleted}
	}
	checkpoint := func(states ...apitype.ResourceV3) integration.RuntimeValidationStackInfo {
		return integration.RuntimeValidationStackInfo{Deployment: &apitype.DeploymentV3{Resources: states}}
	}
	c.observe(checkpoint(state("credential", "vault/first", false, false), state("lookup", "vault/first", true, false)))
	c.observe(checkpoint(state("credential", "vault/second", false, false), state("credential", "vault/first", false, true)))
	c.observe(checkpoint(state("credential", "vault/second", false, false)))
	first, err := c.ResourceID("credential", 0)
	if err != nil || first != "vault/first" {
		t.Fatal("first identity was lost")
	}
	current, err := c.ResourceID("credential")
	if err != nil || current != "vault/second" {
		t.Fatal("current identity is not replacement")
	}
	if len(c.ResourceIDs("credential")) != 2 {
		t.Fatal("duplicate or deleted checkpoints polluted identity history")
	}
	if _, err := c.ResourceID("lookup"); err == nil {
		t.Fatal("external get treated as managed allocation")
	}
	if _, err := c.ResourceID("unknown"); err == nil {
		t.Fatal("unobserved identity fabricated")
	}
	ids := c.ResourceIDs("credential")
	ids[0] = "changed"
	if again, _ := c.ResourceID("credential", 0); again != first {
		t.Fatal("caller mutated identity history")
	}
}

func TestTemporaryCredentialConfigurationIsEncrypted(t *testing.T) {
	root := t.TempDir()
	c := &Context{credentials: credentials{APIKey: "unit-private-api-value"}, config: map[string]string{"seedId": "public-unit-id"}, runName: "unit-run"}
	if err := writeScenarioConfig(root, "stack", "project", c); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "Pulumi.stack.yaml")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), c.credentials.APIKey) {
		t.Fatal("credential written as plaintext")
	}
	if !strings.Contains(string(raw), "secure:") || !strings.Contains(string(raw), "public-unit-id") {
		t.Fatal("encrypted config/public inputs missing")
	}
	if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("temporary credential config is not private")
	}
}
