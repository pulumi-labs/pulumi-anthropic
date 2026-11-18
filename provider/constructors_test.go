// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package provider

import (
	"testing"

	"github.com/blang/semver"
	"github.com/pulumi-labs/pulumi-anthropic/provider/agents"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/identity"
	"github.com/pulumi-labs/pulumi-anthropic/provider/organization"
	"github.com/pulumi-labs/pulumi-anthropic/provider/security"
	"github.com/pulumi-labs/pulumi-anthropic/provider/skills"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Public constructors must be directly registrable, without module assembly or
// another infer.Resource call. Required unknown inputs must retain their metadata
// through Check; preview retains knownness/secrecy but need not forward input
// dependencies onto custom-resource outputs. No usable credentials are provided:
// these operations must not need an API client.
func TestResourceConstructorsPreserveUnknownCreatePreviewInputs(t *testing.T) {
	constructors := map[string]func() infer.InferredResource{
		"anthropic:agents:Agent":                     agents.NewAgent,
		"anthropic:agents:Deployment":                agents.NewDeployment,
		"anthropic:agents:Environment":               agents.NewEnvironment,
		"anthropic:agents:MemoryStore":               agents.NewMemoryStore,
		"anthropic:agents:Tunnel":                    agents.NewTunnel,
		"anthropic:agents:TunnelCertificate":         agents.NewTunnelCertificate,
		"anthropic:agents:UserProfile":               agents.NewUserProfile,
		"anthropic:agents:Vault":                     agents.NewVault,
		"anthropic:agents:VaultCredential":           agents.NewVaultCredential,
		"anthropic:identity:FederationIssuer":        identity.NewFederationIssuer,
		"anthropic:identity:FederationRule":          identity.NewFederationRule,
		"anthropic:identity:FederationRuleWorkspace": identity.NewFederationRuleWorkspace,
		"anthropic:identity:RbacGroup":               identity.NewRbacGroup,
		"anthropic:identity:RbacGroupMember":         identity.NewRbacGroupMember,
		"anthropic:identity:ServiceAccount":          identity.NewServiceAccount,
		"anthropic:identity:ServiceAccountWorkspace": identity.NewServiceAccountWorkspace,
		"anthropic:organization:ApiKey":              organization.NewApiKey,
		"anthropic:organization:ComplianceSettings":  organization.NewComplianceSettings,
		"anthropic:organization:Invite":              organization.NewInvite,
		"anthropic:organization:SpendLimit":          organization.NewSpendLimit,
		"anthropic:organization:User":                organization.NewUser,
		"anthropic:organization:Workspace":           organization.NewWorkspace,
		"anthropic:organization:WorkspaceMember":     organization.NewWorkspaceMember,
		"anthropic:security:ExternalKey":             security.NewExternalKey,
		"anthropic:skills:Skill":                     skills.NewSkill,
	}
	doc := loadSchema(t)
	require.Len(t, constructors, len(doc.Resources))
	for token, spec := range doc.Resources {
		t.Run(token, func(t *testing.T) {
			constructor, ok := constructors[token]
			require.True(t, ok, "missing public constructor")
			provider, err := infer.NewProviderBuilder().
				WithResources(constructor()).
				WithConfig(infer.Config(&config.Config{})).Build()
			require.NoError(t, err)
			server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"), integration.WithProvider(provider))
			require.NoError(t, err)
			resourceURN := resource.NewURN("test", "test", "", tokens.Type(token), "constructed")
			dependency := urn.URN("urn:pulumi:test::test::test:index:Dependency::input")
			unknown := property.New(property.Computed).WithSecret(true).WithDependencies([]urn.URN{dependency})
			inputs := property.Map{}
			for _, key := range spec.RequiredInputs {
				inputs = inputs.Set(key, unknown)
			}
			checked, err := server.Check(p.CheckRequest{Urn: resourceURN, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			preview, err := server.Create(p.CreateRequest{Urn: resourceURN, Properties: checked.Inputs, DryRun: true})
			require.NoError(t, err)
			for _, key := range spec.RequiredInputs {
				require.True(t, unknown.Equals(checked.Inputs.Get(key)), "Check lost input metadata at %s", key)
				require.True(t, unknown.WithDependencies(nil).Equals(preview.Properties.Get(key).WithDependencies(nil)), "preview lost input knownness/secrecy at %s: %#v", key, preview.Properties.Get(key))
			}
		})
	}
}
