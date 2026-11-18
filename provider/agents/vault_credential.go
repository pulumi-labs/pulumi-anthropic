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
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// VaultCredential is a credential stored in a vault.
type VaultCredential struct{}

// NewVaultCredential returns the complete VaultCredential resource.
func NewVaultCredential() infer.InferredResource {
	return vaultCredentialResource{infer.Resource(&VaultCredential{})}
}

func (c *VaultCredential) Annotate(a infer.Annotator) {
	a.Describe(c, "A credential in a Claude Managed Agents vault: an OAuth grant or bearer token "+
		"for an MCP server, or a secret injected into outbound requests as an environment "+
		"variable.\n\n"+
		"Secret values are write-only: the API never returns them, so changes made to them "+
		"outside of Pulumi cannot be detected. Changing the credential's type, MCP server URL, "+
		"secret name, or OAuth client or token endpoint replaces it; because these must be "+
		"unique within a vault, the old credential is deleted before the new one is created. "+
		"Its ID is `<vaultId>/<credentialId>`. Deletion permanently removes the credential, not archives it. Running sessions periodically re-resolve credentials; immediate revocation is not guaranteed.")
}

type VaultCredentialArgs struct {
	VaultID     string            `pulumi:"vaultId"              provider:"replaceOnChanges"`
	DisplayName *string           `pulumi:"displayName,optional"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
	Auth        CredentialAuth    `pulumi:"auth"`
}

func (c *VaultCredentialArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.VaultID, "The vault holding the credential.")
	a.Describe(&c.DisplayName, "The credential's display name, 1–255 characters. Removing a name you previously configured replaces the credential.")
	a.Describe(&c.Metadata, "Arbitrary key-value pairs.")
	a.Describe(&c.Auth, "The credential itself.")
}

type VaultCredentialState struct {
	VaultCredentialArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

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
	MCPServerURL      *string               `pulumi:"mcpServerUrl,optional"      provider:"replaceOnChanges"`
	AccessToken       *string               `pulumi:"accessToken,optional"       provider:"secret"`
	ExpiresAt         *string               `pulumi:"expiresAt,optional"`
	Refresh           *OAuthRefresh         `pulumi:"refresh,optional"`
	Token             *string               `pulumi:"token,optional"             provider:"secret"`
	SecretName        *string               `pulumi:"secretName,optional"        provider:"replaceOnChanges"`
	SecretValue       *string               `pulumi:"secretValue,optional"       provider:"secret"`
	Networking        *CredentialNetworking `pulumi:"networking,optional"`
	InjectionLocation *InjectionLocation    `pulumi:"injectionLocation,optional"`
}

func (c *CredentialAuth) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "The kind of credential. Changing it replaces the credential.")
	a.Describe(&c.MCPServerURL, "The MCP server the credential is for, unique within the vault. "+
		"Required for `mcp_oauth` and `static_bearer`. Changing it replaces the credential.")
	a.Describe(&c.AccessToken, "The OAuth access token. Required for `mcp_oauth`.")
	a.Describe(&c.ExpiresAt, "When the OAuth access token expires, in RFC 3339 format. Must be in the future when you create or update the credential. Removing an expiration time you previously configured replaces the credential.")
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
	ClientID          string            `pulumi:"clientId"          provider:"replaceOnChanges"`
	RefreshToken      *string           `pulumi:"refreshToken,optional" provider:"secret"`
	TokenEndpoint     string            `pulumi:"tokenEndpoint"     provider:"replaceOnChanges"`
	TokenEndpointAuth TokenEndpointAuth `pulumi:"tokenEndpointAuth"`
	Resource          *string           `pulumi:"resource,optional" provider:"replaceOnChanges"`
	Scope             *string           `pulumi:"scope,optional"`
}

func (r *OAuthRefresh) Annotate(a infer.Annotator) {
	a.Describe(&r.ClientID, "The OAuth client ID. Changing it replaces the credential.")
	a.Describe(&r.RefreshToken, "The OAuth refresh token, required when adding automatic token refresh. Anthropic does not return this token, so it cannot be recovered by importing a credential. Omit it to keep the current token.")
	a.Describe(&r.TokenEndpoint, "The token endpoint's URL. Changing it replaces the credential.")
	a.Describe(&r.TokenEndpointAuth, "How the client authenticates to the token endpoint.")
	a.Describe(&r.Resource, "The OAuth resource indicator. Changing it replaces the credential.")
	a.Describe(&r.Scope, "The OAuth scope to request. Removing this setting clears the stored scope.")
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
	Type         TokenEndpointAuthType `pulumi:"type" provider:"replaceOnChanges"`
	ClientSecret *string               `pulumi:"clientSecret,optional" provider:"secret"`
}

func (t *TokenEndpointAuth) Annotate(a infer.Annotator) {
	a.Describe(&t.Type, "The authentication method. Changing it replaces the credential.")
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

var (
	_ infer.CustomUpdate[VaultCredentialArgs, VaultCredentialState] = (*VaultCredential)(nil)
	_ infer.CustomRead[VaultCredentialArgs, VaultCredentialState]   = (*VaultCredential)(nil)
	_ infer.CustomDelete[VaultCredentialState]                      = (*VaultCredential)(nil)
)

func (*VaultCredential) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[VaultCredentialArgs], error) {
	a, failures, err := infer.DefaultCheck[VaultCredentialArgs](ctx, req.NewInputs)
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	str := func(path string, required bool) {
		v := credentialPath(req.NewInputs, path)
		if v.IsComputed() {
			return
		}
		if v.IsNull() {
			if required {
				fail(path, "is required")
			}
			return
		}
		if v.IsString() && v.AsString() == "" {
			fail(path, "must not be empty")
		}
	}
	str("vaultId", true)
	if v := req.NewInputs.Get("displayName"); v.IsString() {
		n := utf8.RuneCountInString(v.AsString())
		if n < 1 || n > 255 {
			fail("displayName", "must contain 1–255 characters")
		}
	}
	if v := req.NewInputs.Get("metadata"); v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 pairs")
		}
		for k, v := range v.AsMap().All {
			if len(k) > 64 {
				fail("metadata", "keys must contain at most 64 bytes")
			}
			if v.IsString() && utf8.RuneCountInString(v.AsString()) > 512 {
				fail("metadata", "values must contain at most 512 characters")
			}
		}
	}
	av := req.NewInputs.Get("auth")
	if !av.IsComputed() && av.IsMap() {
		auth := av.AsMap()
		tv := auth.Get("type")
		if tv.IsString() {
			allowed := map[string]bool{"type": true}
			switch tv.AsString() {
			case "static_bearer":
				allowed["mcpServerUrl"] = true
				allowed["token"] = true
				str("auth.mcpServerUrl", true)
				str("auth.token", false)
			case "mcp_oauth":
				for _, k := range []string{"mcpServerUrl", "accessToken", "expiresAt", "refresh"} {
					allowed[k] = true
				}
				str("auth.mcpServerUrl", true)
				str("auth.accessToken", false)
			case "environment_variable":
				for _, k := range []string{"secretName", "secretValue", "networking", "injectionLocation"} {
					allowed[k] = true
				}
				str("auth.secretName", true)
				str("auth.secretValue", false)
				if auth.Get("networking").IsNull() {
					fail("auth.networking", "is required")
				}
			default:
				fail("auth.type", "must select a supported credential variant")
			}
			for k, v := range auth.All {
				if !allowed[k] && !v.IsNull() {
					fail("auth."+k, "does not apply to this credential variant")
				}
			}
		}
		for _, path := range []string{"auth.mcpServerUrl", "auth.refresh.tokenEndpoint"} {
			v := credentialPath(req.NewInputs, path)
			if v.IsString() {
				u, e := url.Parse(v.AsString())
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" {
					fail(path, "must be an absolute HTTP(S) URL without userinfo or fragment")
				}
			}
		}
		if v := auth.Get("expiresAt"); v.IsString() {
			if _, e := time.Parse(time.RFC3339, v.AsString()); e != nil {
				fail("auth.expiresAt", "must be an RFC 3339 timestamp; the platform requires a future expiry on mutation")
			}
		}
		if v := auth.Get("injectionLocation"); v.IsMap() {
			l := v.AsMap()
			h, b := l.Get("header"), l.Get("body")
			if !h.IsComputed() && !b.IsComputed() && (!h.IsBool() || !h.AsBool()) && (!b.IsBool() || !b.AsBool()) {
				fail("auth.injectionLocation", "at least one location must be enabled")
			}
		}
		if v := auth.Get("networking"); v.IsMap() {
			n := v.AsMap()
			t := n.Get("type")
			if t.IsString() {
				switch t.AsString() {
				case "unrestricted":
					if !n.Get("allowedHosts").IsNull() {
						fail("auth.networking.allowedHosts", "must be omitted for unrestricted networking")
					}
				case "limited":
					h := n.Get("allowedHosts")
					if h.IsNull() {
						fail("auth.networking.allowedHosts", "is required for limited networking")
					}
					if h.IsArray() {
						if h.AsArray().Len() > 16 {
							fail("auth.networking.allowedHosts", "must contain at most 16 hosts")
						}
						for i, v := range h.AsArray().All {
							if v.IsString() && (v.AsString() == "" || strings.ContainsAny(v.AsString(), "/:@ \t\n")) {
								fail(fmt.Sprintf("auth.networking.allowedHosts[%d]", i), "must be a bare hostname, IPv4 address or wildcard hostname")
							}
						}
					}
				default:
					fail("auth.networking.type", "must be limited or unrestricted")
				}
			}
		}
		if v := auth.Get("refresh"); v.IsMap() {
			str("auth.refresh.clientId", true)
			str("auth.refresh.tokenEndpoint", true)
			t := credentialPath(req.NewInputs, "auth.refresh.tokenEndpointAuth.type")
			if t.IsString() {
				switch t.AsString() {
				case "none":
					if !credentialPath(req.NewInputs, "auth.refresh.tokenEndpointAuth.clientSecret").IsNull() {
						fail("auth.refresh.tokenEndpointAuth.clientSecret", "must be omitted for none")
					}
				case "client_secret_basic", "client_secret_post":
					str("auth.refresh.tokenEndpointAuth.clientSecret", false)
				default:
					fail("auth.refresh.tokenEndpointAuth.type", "must select a supported authentication mode")
				}
			}
		}
	}
	return infer.CheckResponse[VaultCredentialArgs]{Inputs: a, Failures: failures}, err
}

func (*VaultCredential) Create(ctx context.Context, req infer.CreateRequest[VaultCredentialArgs]) (infer.CreateResponse[VaultCredentialState], error) {
	if req.DryRun {
		return infer.CreateResponse[VaultCredentialState]{Output: VaultCredentialState{VaultCredentialArgs: req.Inputs}}, nil
	}
	params, err := credentialNew(req.Inputs)
	if err != nil {
		return infer.CreateResponse[VaultCredentialState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[VaultCredentialState]{}, err
	}
	r, err := c.Beta.Vaults.Credentials.New(ctx, req.Inputs.VaultID, params)
	if err != nil {
		return infer.CreateResponse[VaultCredentialState]{}, err
	}
	s, err := credentialState(r, req.Inputs.VaultID, req.Inputs)
	if err != nil {
		return infer.CreateResponse[VaultCredentialState]{}, err
	}
	return infer.CreateResponse[VaultCredentialState]{ID: req.Inputs.VaultID + "/" + r.ID, Output: s}, nil
}

func (*VaultCredential) Read(ctx context.Context, req infer.ReadRequest[VaultCredentialArgs, VaultCredentialState]) (infer.ReadResponse[VaultCredentialArgs, VaultCredentialState], error) {
	v, id, err := credentialID(req.ID)
	if err != nil {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, err
	}
	r, err := c.Beta.Vaults.Credentials.Get(ctx, id, anthropic.BetaVaultCredentialGetParams{VaultID: v})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, err
	}
	// Metadata is nonnullable in the SDK response. Missing/null metadata is
	// not an observation that a managed map became empty.
	if r == nil || !apiutil.StringMapField(r.JSON.Metadata) {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, fmt.Errorf("invalid or incomplete credential response: metadata")
	}
	if !r.ArchivedAt.IsZero() && apiutil.StringField(r.JSON.ArchivedAt) {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, nil
	}
	s, err := credentialState(r, v, req.Inputs)
	if err != nil {
		return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{}, err
	}
	return infer.ReadResponse[VaultCredentialArgs, VaultCredentialState]{ID: req.ID, Inputs: s.VaultCredentialArgs, State: s}, nil
}

func (*VaultCredential) Update(ctx context.Context, req infer.UpdateRequest[VaultCredentialArgs, VaultCredentialState]) (infer.UpdateResponse[VaultCredentialState], error) {
	if req.DryRun {
		return infer.UpdateResponse[VaultCredentialState]{Output: VaultCredentialState{VaultCredentialArgs: req.Inputs}}, nil
	}
	v, id, err := credentialID(req.ID)
	if err != nil {
		return infer.UpdateResponse[VaultCredentialState]{}, err
	}
	params, err := credentialUpdate(req.State.VaultCredentialArgs, req.Inputs, v)
	if err != nil {
		return infer.UpdateResponse[VaultCredentialState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[VaultCredentialState]{}, err
	}
	r, err := c.Beta.Vaults.Credentials.Update(ctx, id, params)
	if err != nil {
		return infer.UpdateResponse[VaultCredentialState]{}, err
	}
	s, err := credentialState(r, v, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[VaultCredentialState]{}, err
	}
	return infer.UpdateResponse[VaultCredentialState]{Output: s}, nil
}

func (*VaultCredential) Delete(ctx context.Context, req infer.DeleteRequest[VaultCredentialState]) (infer.DeleteResponse, error) {
	v, id, err := credentialID(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.Vaults.Credentials.Delete(ctx, id, anthropic.BetaVaultCredentialDeleteParams{VaultID: v})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Check retains knownness, secrecy and dependencies on normalized logical inputs.
// Read also marks newly recovered credential secrets, which infer does not wire.
type vaultCredentialResource struct{ infer.InferredResource }

func (r vaultCredentialResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	req.Inputs = credentialNormalize(req.Inputs)
	resp, err := infer.Resource(&VaultCredential{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r vaultCredentialResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	resp, err := infer.Resource(&VaultCredential{}).Diff(ctx, req)
	if err != nil {
		return resp, err
	}
	// The pinned framework only checks replacement annotations on top-level fields.
	// Upgrade detailed default diffs for these specific immutable paths/ancestors.
	immutable := []string{"vaultId", "auth.type", "auth.mcpServerUrl", "auth.secretName", "auth.refresh.clientId", "auth.refresh.tokenEndpoint", "auth.refresh.resource", "auth.refresh.tokenEndpointAuth.type"}
	for path, d := range resp.DetailedDiff {
		replace := false
		for _, i := range immutable {
			if path == i || strings.HasPrefix(path, i+".") || strings.HasPrefix(i, path+".") {
				replace = true
			}
		}
		// Neither display name nor expiry has a documented in-place clear operation.
		for _, i := range []string{"displayName", "auth.expiresAt"} {
			if (path == i || strings.HasPrefix(i, path+".")) && credentialPath(req.Inputs, i).IsNull() && !credentialPath(req.State, i).IsNull() {
				replace = true
			}
		}
		if replace {
			switch d.Kind {
			case p.Add:
				d.Kind = p.AddReplace
			case p.Delete:
				d.Kind = p.DeleteReplace
			case p.Update:
				d.Kind = p.UpdateReplace
			}
			resp.DetailedDiff[path] = d
			resp.DeleteBeforeReplace = true
		}
	}
	return resp, nil
}

func (r vaultCredentialResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&VaultCredential{}).Create(ctx, req)
}

func (r vaultCredentialResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&VaultCredential{}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		resp.Inputs = credentialNormalize(resp.Inputs)
		resp.Properties = credentialNormalize(resp.Properties)
	}
	return resp, err
}

func (r vaultCredentialResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&VaultCredential{}).Update(ctx, req)
}

func (r vaultCredentialResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&VaultCredential{}).Delete(ctx, req)
}

func credentialNormalize(b property.Map) property.Map {
	av := b.Get("auth")
	if !av.IsMap() {
		return b
	}
	a := av.AsMap()
	u := a.Get("mcpServerUrl")
	if u.IsString() {
		a = a.Set("mcpServerUrl", property.WithGoValue(u, canonicalMCPServerURL(u.AsString())))
	}
	for _, k := range []string{"token", "accessToken", "secretValue"} {
		if v, ok := a.GetOk(k); ok && !v.IsNull() {
			a = a.Set(k, v.WithSecret(true))
		}
	}
	if rv := a.Get("refresh"); rv.IsMap() {
		r := rv.AsMap()
		if v, ok := r.GetOk("refreshToken"); ok && !v.IsNull() {
			r = r.Set("refreshToken", v.WithSecret(true))
		}
		if tv := r.Get("tokenEndpointAuth"); tv.IsMap() {
			t := tv.AsMap()
			if v, ok := t.GetOk("clientSecret"); ok && !v.IsNull() {
				t = t.Set("clientSecret", v.WithSecret(true))
			}
			r = r.Set("tokenEndpointAuth", property.WithGoValue(tv, t))
		}
		a = a.Set("refresh", property.WithGoValue(rv, r))
	}
	if lv := a.Get("injectionLocation"); lv.IsMap() {
		l := lv.AsMap()
		for _, k := range []string{"header", "body"} {
			if l.Get(k).IsNull() {
				l = l.Set(k, property.New(false))
			}
		}
		a = a.Set("injectionLocation", property.WithGoValue(lv, l))
	}
	return b.Set("auth", property.WithGoValue(av, a))
}

func credentialPath(b property.Map, path string) property.Value {
	v := property.New(b)
	for _, k := range strings.Split(path, ".") {
		if !v.IsMap() {
			return v
		}
		v = v.AsMap().Get(k)
	}
	return v
}

func credentialOpt(v *string) param.Opt[string] {
	if v != nil {
		return anthropic.String(*v)
	}
	return param.Opt[string]{}
}

func credentialString(v *string) string {
	if v != nil {
		return *v
	}
	return ""
}

func credentialExpiry(v *string) (param.Opt[time.Time], error) {
	if v == nil {
		return param.Opt[time.Time]{}, nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	return anthropic.Time(t), err
}

func credentialNetworking(v *CredentialNetworking) anthropic.BetaManagedAgentsCredentialNetworkingParamsUnion {
	if v == nil {
		return anthropic.BetaManagedAgentsCredentialNetworkingParamsUnion{}
	}
	if v.Type == "limited" {
		return anthropic.BetaManagedAgentsCredentialNetworkingParamsOfLimited(append([]string{}, v.AllowedHosts...))
	}
	return anthropic.BetaManagedAgentsCredentialNetworkingParamsUnion{OfUnrestricted: &anthropic.BetaManagedAgentsUnrestrictedCredentialNetworkingParams{}}
}

func credentialLocations(v *InjectionLocation) (bool, bool) {
	if v == nil {
		return true, true
	}
	return v.Header != nil && *v.Header, v.Body != nil && *v.Body
}

func credentialNew(a VaultCredentialArgs) (anthropic.BetaVaultCredentialNewParams, error) {
	p := anthropic.BetaVaultCredentialNewParams{DisplayName: credentialOpt(a.DisplayName), Metadata: maps.Clone(a.Metadata)}
	switch a.Auth.Type {
	case "static_bearer":
		if a.Auth.Token == nil {
			return p, fmt.Errorf("auth.token is required to create a bearer credential")
		}
		p.Auth.OfStaticBearer = &anthropic.BetaManagedAgentsStaticBearerCreateParams{MCPServerURL: credentialString(a.Auth.MCPServerURL), Token: *a.Auth.Token}
	case "environment_variable":
		if a.Auth.SecretValue == nil {
			return p, fmt.Errorf("auth.secretValue is required to create an environment credential")
		}
		h, b := credentialLocations(a.Auth.InjectionLocation)
		p.Auth.OfEnvironmentVariable = &anthropic.BetaManagedAgentsEnvironmentVariableCreateParams{SecretName: credentialString(a.Auth.SecretName), SecretValue: *a.Auth.SecretValue, Networking: credentialNetworking(a.Auth.Networking), InjectionLocation: anthropic.BetaManagedAgentsInjectionLocationParams{Header: anthropic.Bool(h), Body: anthropic.Bool(b)}}
	case "mcp_oauth":
		if a.Auth.AccessToken == nil {
			return p, fmt.Errorf("auth.accessToken is required to create an OAuth credential")
		}
		expiry, err := credentialExpiry(a.Auth.ExpiresAt)
		if err != nil {
			return p, fmt.Errorf("invalid auth.expiresAt")
		}
		o := &anthropic.BetaManagedAgentsMCPOAuthCreateParams{MCPServerURL: credentialString(a.Auth.MCPServerURL), AccessToken: *a.Auth.AccessToken, ExpiresAt: expiry}
		if r := a.Auth.Refresh; r != nil {
			if r.RefreshToken == nil || *r.RefreshToken == "" {
				return p, fmt.Errorf("auth.refresh.refreshToken is required to create refresh support")
			}
			if r.TokenEndpointAuth.Type != "none" && r.TokenEndpointAuth.ClientSecret == nil {
				return p, fmt.Errorf("auth.refresh.tokenEndpointAuth.clientSecret is required to create a confidential client")
			}
			o.Refresh = anthropic.BetaManagedAgentsMCPOAuthRefreshParams{ClientID: r.ClientID, RefreshToken: *r.RefreshToken, TokenEndpoint: r.TokenEndpoint, Resource: credentialOpt(r.Resource), Scope: credentialOpt(r.Scope)}
			switch r.TokenEndpointAuth.Type {
			case "none":
				o.Refresh.TokenEndpointAuth.OfNone = &anthropic.BetaManagedAgentsTokenEndpointAuthNoneParam{}
			case "client_secret_basic":
				o.Refresh.TokenEndpointAuth.OfClientSecretBasic = &anthropic.BetaManagedAgentsTokenEndpointAuthBasicParam{ClientSecret: credentialString(r.TokenEndpointAuth.ClientSecret)}
			case "client_secret_post":
				o.Refresh.TokenEndpointAuth.OfClientSecretPost = &anthropic.BetaManagedAgentsTokenEndpointAuthPostParam{ClientSecret: credentialString(r.TokenEndpointAuth.ClientSecret)}
			}
		}
		p.Auth.OfMCPOAuth = o
	default:
		return p, fmt.Errorf("invalid auth.type")
	}
	return p, nil
}

func credentialUpdate(old, a VaultCredentialArgs, vault string) (anthropic.BetaVaultCredentialUpdateParams, error) {
	p := anthropic.BetaVaultCredentialUpdateParams{VaultID: vault, DisplayName: credentialOpt(a.DisplayName)}
	metadata := apiutil.MergedStringMap(old.Metadata, a.Metadata)
	p.SetExtraFields(map[string]any{"metadata": metadata})
	switch a.Auth.Type {
	case "static_bearer":
		p.Auth.OfStaticBearer = &anthropic.BetaManagedAgentsStaticBearerUpdateParams{Token: credentialOpt(a.Auth.Token)}
	case "environment_variable":
		h, b := credentialLocations(a.Auth.InjectionLocation)
		p.Auth.OfEnvironmentVariable = &anthropic.BetaManagedAgentsEnvironmentVariableUpdateParams{SecretValue: credentialOpt(a.Auth.SecretValue), Networking: credentialNetworking(a.Auth.Networking), InjectionLocation: anthropic.BetaManagedAgentsInjectionLocationUpdateParams{Header: anthropic.Bool(h), Body: anthropic.Bool(b)}}
	case "mcp_oauth":
		expiry, err := credentialExpiry(a.Auth.ExpiresAt)
		if err != nil {
			return p, fmt.Errorf("invalid auth.expiresAt")
		}
		o := &anthropic.BetaManagedAgentsMCPOAuthUpdateParams{AccessToken: credentialOpt(a.Auth.AccessToken), ExpiresAt: expiry}
		if r := a.Auth.Refresh; r != nil {
			o.Refresh.Scope = anthropic.String(credentialString(r.Scope))
			// An imported credential has no recoverable refresh token. Never manufacture an empty rotation.
			o.Refresh.RefreshToken = credentialOpt(r.RefreshToken)
			switch r.TokenEndpointAuth.Type {
			case "client_secret_basic":
				o.Refresh.TokenEndpointAuth.OfClientSecretBasic = &anthropic.BetaManagedAgentsTokenEndpointAuthBasicUpdateParam{ClientSecret: credentialOpt(r.TokenEndpointAuth.ClientSecret)}
			case "client_secret_post":
				o.Refresh.TokenEndpointAuth.OfClientSecretPost = &anthropic.BetaManagedAgentsTokenEndpointAuthPostUpdateParam{ClientSecret: credentialOpt(r.TokenEndpointAuth.ClientSecret)}
			}
		}
		p.Auth.OfMCPOAuth = o
	default:
		return p, fmt.Errorf("invalid auth.type")
	}
	return p, nil
}

func credentialID(id string) (string, string, error) {
	vaultID, credentialID, ok := resourceutil.SplitID(id)
	if !ok {
		return "", "", fmt.Errorf("credential ID must be <vaultId>/<credentialId>")
	}
	return vaultID, credentialID, nil
}

// Only write-only secrets are carried from prior inputs. Readable values always
// come from the generated SDK response, never from a desired-state projection.
func credentialState(r *anthropic.BetaManagedAgentsCredential, vault string, prior VaultCredentialArgs) (VaultCredentialState, error) {
	s := VaultCredentialState{VaultCredentialArgs: VaultCredentialArgs{VaultID: vault}}
	if r == nil {
		return s, fmt.Errorf("missing credential response")
	}
	var problems []string
	invalid := func(field string) { problems = append(problems, "invalid or incomplete credential response: "+field) }
	malformed := func(raw string, valid bool, field string) {
		if raw != "" && raw != "null" && !valid {
			invalid(field)
		}
	}
	if !apiutil.StringMapField(r.JSON.Metadata) {
		invalid("metadata")
	}
	malformed(r.JSON.DisplayName.Raw(), apiutil.StringField(r.JSON.DisplayName), "displayName")
	malformed(r.JSON.ArchivedAt.Raw(), apiutil.StringField(r.JSON.ArchivedAt), "archivedAt")
	if apiutil.StringField(r.JSON.DisplayName) {
		s.DisplayName = &r.DisplayName
	}
	if apiutil.StringMapField(r.JSON.Metadata) && (prior.Metadata != nil || len(r.Metadata) > 0) {
		s.Metadata = maps.Clone(r.Metadata)
	}
	if apiutil.StringField(r.JSON.CreatedAt) {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	} else {
		invalid("createdAt")
	}
	if apiutil.StringField(r.JSON.UpdatedAt) {
		s.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	} else {
		invalid("updatedAt")
	}
	if !apiutil.StringField(r.JSON.ID) || r.ID == "" {
		invalid("id")
	}
	if !apiutil.ObjectField(r.JSON.Auth) || !apiutil.StringField(r.Auth.JSON.Type) {
		invalid("auth.type")
	}
	a := r.Auth
	if apiutil.StringField(a.JSON.Type) {
		s.Auth.Type = CredentialType(a.Type)
	}
	switch a.Type {
	case "static_bearer", "mcp_oauth":
		if apiutil.StringField(a.JSON.MCPServerURL) {
			u := canonicalMCPServerURL(a.MCPServerURL)
			s.Auth.MCPServerURL = &u
		} else {
			invalid("auth.mcpServerUrl")
		}
		if a.Type == "static_bearer" {
			s.Auth.Token = prior.Auth.Token
			break
		}
		s.Auth.AccessToken = prior.Auth.AccessToken
		malformed(a.JSON.ExpiresAt.Raw(), apiutil.StringField(a.JSON.ExpiresAt), "auth.expiresAt")
		malformed(a.JSON.Refresh.Raw(), apiutil.ObjectField(a.JSON.Refresh), "auth.refresh")
		if apiutil.StringField(a.JSON.ExpiresAt) {
			t := a.ExpiresAt.Format(time.RFC3339Nano)
			s.Auth.ExpiresAt = &t
		}
		if apiutil.ObjectField(a.JSON.Refresh) {
			r := a.Refresh
			refresh := &OAuthRefresh{}
			if apiutil.StringField(r.JSON.ClientID) {
				refresh.ClientID = r.ClientID
			} else {
				invalid("auth.refresh.clientId")
			}
			if apiutil.StringField(r.JSON.TokenEndpoint) {
				refresh.TokenEndpoint = r.TokenEndpoint
			} else {
				invalid("auth.refresh.tokenEndpoint")
			}
			if apiutil.ObjectField(r.JSON.TokenEndpointAuth) && apiutil.StringField(r.TokenEndpointAuth.JSON.Type) && (r.TokenEndpointAuth.Type == "none" || r.TokenEndpointAuth.Type == "client_secret_basic" || r.TokenEndpointAuth.Type == "client_secret_post") {
				refresh.TokenEndpointAuth.Type = TokenEndpointAuthType(r.TokenEndpointAuth.Type)
			} else {
				invalid("auth.refresh.tokenEndpointAuth.type")
			}
			malformed(r.JSON.Resource.Raw(), apiutil.StringField(r.JSON.Resource), "auth.refresh.resource")
			malformed(r.JSON.Scope.Raw(), apiutil.StringField(r.JSON.Scope), "auth.refresh.scope")
			if apiutil.StringField(r.JSON.Resource) {
				refresh.Resource = &r.Resource
			}
			if apiutil.StringField(r.JSON.Scope) && (r.Scope != "" || prior.Auth.Refresh != nil && prior.Auth.Refresh.Scope != nil) {
				refresh.Scope = &r.Scope
			}
			if p := prior.Auth.Refresh; p != nil {
				refresh.RefreshToken = p.RefreshToken
				refresh.TokenEndpointAuth.ClientSecret = p.TokenEndpointAuth.ClientSecret
			}
			s.Auth.Refresh = refresh
		}
	case "environment_variable":
		s.Auth.SecretValue = prior.Auth.SecretValue
		if apiutil.StringField(a.JSON.SecretName) {
			s.Auth.SecretName = &a.SecretName
		} else {
			invalid("auth.secretName")
		}
		n := a.Networking
		if apiutil.ObjectField(a.JSON.Networking) && apiutil.StringField(n.JSON.Type) && (n.Type == "limited" || n.Type == "unrestricted") {
			s.Auth.Networking = &CredentialNetworking{Type: NetworkingType(n.Type)}
			if n.Type == "limited" {
				if apiutil.StringListField(n.JSON.AllowedHosts) {
					s.Auth.Networking.AllowedHosts = append([]string{}, n.AllowedHosts...)
				} else {
					invalid("auth.networking.allowedHosts")
				}
			}
			malformed(n.JSON.AllowedHosts.Raw(), apiutil.StringListField(n.JSON.AllowedHosts), "auth.networking.allowedHosts")
		} else {
			invalid("auth.networking.type")
		}
		l := a.InjectionLocation
		if !apiutil.ObjectField(a.JSON.InjectionLocation) || !apiutil.BoolField(l.JSON.Header) || !apiutil.BoolField(l.JSON.Body) {
			invalid("auth.injectionLocation")
		}
		if apiutil.BoolField(l.JSON.Header) || apiutil.BoolField(l.JSON.Body) {
			location := &InjectionLocation{}
			if apiutil.BoolField(l.JSON.Header) {
				h := l.Header
				location.Header = &h
			}
			if apiutil.BoolField(l.JSON.Body) {
				b := l.Body
				location.Body = &b
			}
			// Only fully observed undeclared defaults may be omitted from state.
			if prior.Auth.InjectionLocation != nil || location.Header == nil || location.Body == nil || !*location.Header || !*location.Body {
				s.Auth.InjectionLocation = location
			}
		}
	default:
		invalid("auth.type")
	}
	if len(problems) > 0 {
		return s, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return s, nil
}
