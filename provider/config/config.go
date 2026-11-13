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

// Package config defines the provider's configuration and the credential planes that
// resources use to reach the Anthropic API.
package config

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	sdkconfig "github.com/anthropics/anthropic-sdk-go/v2/config"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Plane identifies the class of credential an endpoint accepts. The Anthropic API
// partitions its surface this way: workspace-scoped resources take a regular API key,
// organization administration takes an Admin API key, and a handful of identity
// endpoints accept only an OAuth token carrying the org:admin scope.
type Plane int

const (
	// Workspace endpoints (agents, skills, tunnels, ...) accept an API key or OAuth token.
	Workspace Plane = iota
	// Admin endpoints (/v1/organizations/...) accept an Admin API key or OAuth token.
	Admin
	// OrgAdminOAuth endpoints (service accounts, federation) accept only an OAuth token.
	OrgAdminOAuth
)

// Config is the provider configuration.
type Config struct {
	APIKey      string      `pulumi:"apiKey,optional"      provider:"secret"`
	AdminKey    string      `pulumi:"adminKey,optional"    provider:"secret"`
	AuthToken   string      `pulumi:"authToken,optional"   provider:"secret"`
	BaseURL     string      `pulumi:"baseUrl,optional"`
	WorkspaceID string      `pulumi:"workspaceId,optional"`
	Federation  *Federation `pulumi:"federation,optional"`

	clients map[Plane]*anthropic.Client

	// Only set on a request-local CheckConfig receiver, never the configured client owner.
	checkInputs *property.Map
}

var _ infer.CustomConfigure = (*Config)(nil)

func (c *Config) Annotate(a infer.Annotator) {
	a.Describe(c, "Authentication and organization/workspace settings for the Anthropic provider.")
	a.Describe(&c.APIKey, "An Anthropic API key, used for workspace-scoped resources such as "+
		"agents, environments, vaults, and skills. Falls back to `ANTHROPIC_API_KEY`. Takes precedence over OAuth, including when read from the environment.")
	a.Describe(&c.AdminKey, "An Anthropic Admin API key (`sk-ant-admin...`), used for "+
		"organization resources such as workspaces, members, and invites. "+
		"Falls back to `ANTHROPIC_ADMIN_KEY`. Takes precedence over OAuth, including when read from the environment.")
	a.Describe(&c.AuthToken, "An OAuth access token. Used when the API key or Admin API key for an operation is not set. "+
		"OAuth-only identity operations require the `org:admin` scope. "+
		"Falls back to `ANTHROPIC_AUTH_TOKEN`. Not refreshed automatically; cannot be used together with `federation`.")
	a.Describe(&c.Federation, "Automatically renewed OAuth credentials obtained from an explicitly configured OIDC identity token source. "+
		"Requires `federation.organizationId` and `workspaceId`. Used when the appropriate API key or Admin API key is not set, and for operations that require OAuth. The federation rule determines the available permissions.")
	a.Describe(&c.BaseURL, "The Anthropic API base URL. Falls back to `ANTHROPIC_BASE_URL`, "+
		"then to `https://api.anthropic.com`.")
	a.Describe(&c.WorkspaceID, "The workspace used for workspace-scoped operations. Required with federation, "+
		"where it selects the workspace during token exchange. Otherwise, it is sent in the "+
		"`anthropic-workspace-id` header and is needed when a credential can act on more than one workspace. "+
		"Changing this selection does not automatically replace resources. "+
		"To manage several workspaces, use one explicit provider per workspace.")
	a.SetDefault(&c.APIKey, "", "ANTHROPIC_API_KEY")
	a.SetDefault(&c.AdminKey, "", "ANTHROPIC_ADMIN_KEY")
	a.SetDefault(&c.AuthToken, "", "ANTHROPIC_AUTH_TOKEN")
	a.SetDefault(&c.BaseURL, "", "ANTHROPIC_BASE_URL")
}

// Configure builds one client per plane that has a usable credential.
func (c *Config) Configure(context.Context) error {
	c.clients = nil
	base, err := normalizeBaseURL(c.BaseURL)
	if err != nil {
		return err
	}
	c.BaseURL = base
	if failures := validateConfig(*c, nil); len(failures) != 0 {
		return fmt.Errorf("%s: %s", failures[0].Property, failures[0].Reason)
	}
	common := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	if c.BaseURL != "" {
		common = append(common, option.WithBaseURL(c.BaseURL))
	}
	newClient := func(oauth, federation bool, auth ...option.RequestOption) *anthropic.Client {
		// Never append to common's backing array: generated services retain options.
		opts := append([]option.RequestOption(nil), common...)
		if c.WorkspaceID != "" && !federation {
			opts = append(opts, option.WithHeader("anthropic-workspace-id", c.WorkspaceID))
		}
		opts = append(opts, auth...)
		if oauth {
			opts = append(opts, option.WithMiddleware(oauthBeta))
		}
		client := anthropic.NewClient(opts...)
		return &client
	}
	var oauth option.RequestOption
	if c.AuthToken != "" {
		oauth = option.WithAuthToken(c.AuthToken)
	} else if c.Federation != nil {
		oauth = option.WithFederationTokenProvider(c.Federation.IdentityToken.provider(), option.FederationOptions{
			FederationRuleID: c.Federation.RuleID, OrganizationID: c.Federation.OrganizationID,
			WorkspaceID: c.WorkspaceID, ServiceAccountID: c.Federation.ServiceAccountID,
		})
	}

	c.clients = map[Plane]*anthropic.Client{}
	switch {
	case c.APIKey != "":
		// Use x-api-key without competing Authorization for API-key credentials.
		c.clients[Workspace] = newClient(false, false, option.WithAPIKey(c.APIKey), option.WithHeader("x-api-key", c.APIKey), option.WithHeaderDel("Authorization"))
	case oauth != nil:
		c.clients[Workspace] = newClient(true, c.Federation != nil, oauth)
	}
	switch {
	case c.AdminKey != "":
		c.clients[Admin] = newClient(false, false, option.WithAPIKey(c.AdminKey), option.WithHeader("x-api-key", c.AdminKey), option.WithHeaderDel("Authorization"))
	case oauth != nil:
		c.clients[Admin] = newClient(true, c.Federation != nil, oauth)
	}
	if oauth != nil {
		c.clients[OrgAdminOAuth] = newClient(true, c.Federation != nil, oauth)
	}
	return nil
}

// oauthBeta runs after generated services have set their per-operation beta headers.
func oauthBeta(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	req = req.Clone(req.Context())
	var betas []string
	seen := map[string]bool{}
	for _, value := range req.Header.Values("anthropic-beta") {
		for _, beta := range strings.Split(value, ",") {
			beta = strings.TrimSpace(beta)
			if beta != "" && !seen[beta] {
				betas = append(betas, beta)
				seen[beta] = true
			}
		}
	}
	if !seen[sdkconfig.OAuthAPIBetaHeader] {
		betas = append(betas, sdkconfig.OAuthAPIBetaHeader)
	}
	req.Header.Set("anthropic-beta", strings.Join(betas, ","))
	return next(req)
}

// Client returns the API client for a plane, or an error naming the credential to set.
func (c Config) Client(plane Plane) (*anthropic.Client, error) {
	if client, ok := c.clients[plane]; ok {
		return client, nil
	}
	switch plane {
	case Workspace:
		return nil, fmt.Errorf("this resource requires an Anthropic API key: set " +
			"`anthropic:apiKey` (or ANTHROPIC_API_KEY), or `anthropic:authToken`")
	case Admin:
		return nil, fmt.Errorf("this resource requires an Anthropic Admin API key: set " +
			"`anthropic:adminKey` (or ANTHROPIC_ADMIN_KEY), or `anthropic:authToken`")
	default:
		return nil, fmt.Errorf("this resource requires an OAuth token with the org:admin " +
			"scope, which the Anthropic API requires in place of an Admin API key: set " +
			"`anthropic:authToken` (or ANTHROPIC_AUTH_TOKEN)")
	}
}

// ClientFor returns the API client for a plane from the provider configuration in ctx.
func ClientFor(ctx context.Context, plane Plane) (*anthropic.Client, error) {
	return infer.GetConfig[Config](ctx).Client(plane)
}
