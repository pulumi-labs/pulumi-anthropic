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

package agents

import (
	"context"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// VaultCredential is a credential stored in a vault.
type VaultCredential struct {
	rest.Child[VaultCredentialArgs, VaultCredentialState, *VaultCredentialState]
}

// NewVaultCredential returns the VaultCredential resource controller.
func NewVaultCredential() *VaultCredential {
	return &VaultCredential{rest.Child[VaultCredentialArgs, VaultCredentialState, *VaultCredentialState]{
		Resource: rest.Resource[VaultCredentialArgs, VaultCredentialState, *VaultCredentialState]{
			Collection: vaultCollection,
			EndOfLife:  rest.Delete,
			Tombstones: map[string]any{"metadata": nil},
			// The fields that force replacement must be unique within the vault, so the
			// replacement could not be created while the original exists.
			DeleteBeforeReplace: true,
		},
		Name:     "credentials",
		ParentID: func(a *VaultCredentialArgs) *string { return &a.VaultID },
	}}
}

func (c *VaultCredential) Annotate(a infer.Annotator) {
	a.Describe(c, "A credential in a Claude Managed Agents vault: an OAuth grant or bearer token "+
		"for an MCP server, or a secret injected into outbound requests as an environment "+
		"variable.\n\n"+
		"Secret values are write-only: the API never returns them, so changes made to them "+
		"outside of Pulumi cannot be detected. Changing the credential's type, MCP server URL, "+
		"secret name, or OAuth client or token endpoint replaces it; because these must be "+
		"unique within a vault, the old credential is deleted before the new one is created. "+
		"Its ID is `<vaultId>/<credentialId>`.")
}

type VaultCredentialArgs struct {
	VaultID     string            `pulumi:"vaultId"              provider:"replaceOnChanges" wire:"-"`
	DisplayName *string           `pulumi:"displayName,optional"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
	Auth        CredentialAuth    `pulumi:"auth"`
}

func (c *VaultCredentialArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.VaultID, "The vault holding the credential.")
	a.Describe(&c.DisplayName, "The credential's display name.")
	a.Describe(&c.Metadata, "Arbitrary key-value pairs.")
	a.Describe(&c.Auth, "The credential itself.")
}

type VaultCredentialState struct {
	VaultCredentialArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *VaultCredentialState) Inputs() *VaultCredentialArgs { return &s.VaultCredentialArgs }

func (s *VaultCredentialState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the credential was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the credential was last updated, in RFC 3339 format.")
}

// CredentialType discriminates the kinds of vault credential.
type CredentialType string

func (CredentialType) Values() []infer.EnumValue[CredentialType] {
	return []infer.EnumValue[CredentialType]{
		{Value: "mcp_oauth", Description: "An OAuth access token for an MCP server, optionally refreshed."},
		{Value: "static_bearer", Description: "A static bearer token for an MCP server."},
		{Value: "environment_variable", Description: "A secret exposed to the sandbox as an " +
			"environment variable and injected into requests to allowed hosts."},
	}
}

// CredentialAuth is a vault credential. Which fields apply depends on the type.
type CredentialAuth struct {
	Type              CredentialType        `pulumi:"type"                      provider:"replaceOnChanges"`
	MCPServerURL      *string               `pulumi:"mcpServerUrl,optional"      provider:"replaceOnChanges" wire:"createOnly"`
	AccessToken       *string               `pulumi:"accessToken,optional"       provider:"secret"`
	ExpiresAt         *string               `pulumi:"expiresAt,optional"`
	Refresh           *OAuthRefresh         `pulumi:"refresh,optional"`
	Token             *string               `pulumi:"token,optional"             provider:"secret"`
	SecretName        *string               `pulumi:"secretName,optional"        provider:"replaceOnChanges" wire:"createOnly"`
	SecretValue       *string               `pulumi:"secretValue,optional"       provider:"secret"`
	Networking        *CredentialNetworking `pulumi:"networking,optional"`
	InjectionLocation *InjectionLocation    `pulumi:"injectionLocation,optional"`
}

func (c *CredentialAuth) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "The kind of credential. Changing it replaces the credential.")
	a.Describe(&c.MCPServerURL, "The MCP server the credential is for, unique within the vault. "+
		"Required for `mcp_oauth` and `static_bearer`. Changing it replaces the credential.")
	a.Describe(&c.AccessToken, "The OAuth access token. Required for `mcp_oauth`.")
	a.Describe(&c.ExpiresAt, "When the access token expires, in RFC 3339 format. For `mcp_oauth`.")
	a.Describe(&c.Refresh, "How to refresh the access token. For `mcp_oauth`.")
	a.Describe(&c.Token, "The bearer token. Required for `static_bearer`.")
	a.Describe(&c.SecretName, "The environment variable's name, unique within the vault. Required "+
		"for `environment_variable`. Changing it replaces the credential.")
	a.Describe(&c.SecretValue, "The secret's value. Required for `environment_variable`.")
	a.Describe(&c.Networking, "The hosts the secret may be sent to. Required for `environment_variable`.")
	a.Describe(&c.InjectionLocation, "Where in outbound requests the secret is injected. For "+
		"`environment_variable`; both locations are enabled when omitted.")
}

// OAuthRefresh configures refreshing an MCP OAuth access token.
type OAuthRefresh struct {
	ClientID          string            `pulumi:"clientId"          provider:"replaceOnChanges" wire:"createOnly"`
	RefreshToken      string            `pulumi:"refreshToken"      provider:"secret"`
	TokenEndpoint     string            `pulumi:"tokenEndpoint"     provider:"replaceOnChanges" wire:"createOnly"`
	TokenEndpointAuth TokenEndpointAuth `pulumi:"tokenEndpointAuth"`
	Resource          *string           `pulumi:"resource,optional" provider:"replaceOnChanges" wire:"createOnly"`
	Scope             *string           `pulumi:"scope,optional"`
}

func (r *OAuthRefresh) Annotate(a infer.Annotator) {
	a.Describe(&r.ClientID, "The OAuth client ID. Changing it replaces the credential.")
	a.Describe(&r.RefreshToken, "The OAuth refresh token.")
	a.Describe(&r.TokenEndpoint, "The token endpoint's URL. Changing it replaces the credential.")
	a.Describe(&r.TokenEndpointAuth, "How the client authenticates to the token endpoint.")
	a.Describe(&r.Resource, "The OAuth resource indicator. Changing it replaces the credential.")
	a.Describe(&r.Scope, "The OAuth scope to request.")
}

// TokenEndpointAuthType is how an OAuth client authenticates to the token endpoint.
type TokenEndpointAuthType string

func (TokenEndpointAuthType) Values() []infer.EnumValue[TokenEndpointAuthType] {
	return []infer.EnumValue[TokenEndpointAuthType]{
		{Value: "none", Description: "A public client, with no client secret."},
		{Value: "client_secret_basic", Description: "The client secret, in an HTTP Basic header."},
		{Value: "client_secret_post", Description: "The client secret, in the request body."},
	}
}

// TokenEndpointAuth is how an OAuth client authenticates to the token endpoint.
type TokenEndpointAuth struct {
	Type         TokenEndpointAuthType `pulumi:"type"`
	ClientSecret *string               `pulumi:"clientSecret,optional" provider:"secret"`
}

func (t *TokenEndpointAuth) Annotate(a infer.Annotator) {
	a.Describe(&t.Type, "The authentication method.")
	a.Describe(&t.ClientSecret, "The client secret. Required unless the type is `none`.")
}

// CredentialNetworking is the hosts an environment-variable secret may be sent to.
type CredentialNetworking struct {
	Type         NetworkingType `pulumi:"type"`
	AllowedHosts []string       `pulumi:"allowedHosts,optional"`
}

func (n *CredentialNetworking) Annotate(a infer.Annotator) {
	a.Describe(&n.Type, "Whether the secret may be sent to any host or only to the listed ones.")
	a.Describe(&n.AllowedHosts, "The hosts the secret may be sent to, up to 16. Applies to `limited`.")
}

// InjectionLocation is where in outbound requests a secret is injected.
type InjectionLocation struct {
	Header *bool `pulumi:"header,optional"`
	Body   *bool `pulumi:"body,optional"`
}

func (l *InjectionLocation) Annotate(a infer.Annotator) {
	a.Describe(&l.Header, "Whether the secret is substituted into request headers.")
	a.Describe(&l.Body, "Whether the secret is substituted into request bodies.")
}

var _ infer.CustomCheck[VaultCredentialArgs] = (*VaultCredential)(nil)

// Check declares the MCP server URL in the form the API stores it; see
// canonicalMCPServerURL.
func (c *VaultCredential) Check(
	ctx context.Context, req infer.CheckRequest,
) (infer.CheckResponse[VaultCredentialArgs], error) {
	args, failures, err := infer.DefaultCheck[VaultCredentialArgs](ctx, req.NewInputs)
	if url := args.Auth.MCPServerURL; url != nil {
		canonical := canonicalMCPServerURL(*url)
		args.Auth.MCPServerURL = &canonical
	}
	return infer.CheckResponse[VaultCredentialArgs]{Inputs: args, Failures: failures}, err
}

var (
	_ infer.CustomUpdate[VaultCredentialArgs, VaultCredentialState] = (*VaultCredential)(nil)
	_ infer.CustomRead[VaultCredentialArgs, VaultCredentialState]   = (*VaultCredential)(nil)
	_ infer.CustomDelete[VaultCredentialState]                      = (*VaultCredential)(nil)
)
