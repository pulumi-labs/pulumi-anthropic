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

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/pulumi/pulumi-go-provider/infer"
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
	APIKey      string `pulumi:"apiKey,optional"      provider:"secret"`
	AdminKey    string `pulumi:"adminKey,optional"    provider:"secret"`
	AuthToken   string `pulumi:"authToken,optional"   provider:"secret"`
	BaseURL     string `pulumi:"baseUrl,optional"`
	WorkspaceID string `pulumi:"workspaceId,optional"`

	clients map[Plane]*anthropic.Client
}

var _ infer.CustomConfigure = (*Config)(nil)

func (c *Config) Annotate(a infer.Annotator) {
	a.Describe(&c.APIKey, "An Anthropic API key, used for workspace-scoped resources such as "+
		"agents, environments, vaults, and skills. Falls back to `ANTHROPIC_API_KEY`.")
	a.Describe(&c.AdminKey, "An Anthropic Admin API key (`sk-ant-admin...`), used for "+
		"organization resources such as workspaces, members, and invites. "+
		"Falls back to `ANTHROPIC_ADMIN_KEY`.")
	a.Describe(&c.AuthToken, "An OAuth access token, sent as a bearer token. It substitutes for "+
		"either key, and is required for service accounts and workload identity federation, "+
		"which need the `org:admin` scope. Falls back to `ANTHROPIC_AUTH_TOKEN`.")
	a.Describe(&c.BaseURL, "The Anthropic API base URL. Falls back to `ANTHROPIC_BASE_URL`, "+
		"then to `https://api.anthropic.com`.")
	a.Describe(&c.WorkspaceID, "The workspace that workspace-scoped resources are created in, "+
		"sent as the `anthropic-workspace-id` header. Only needed when a credential can act "+
		"on more than one workspace. To manage several workspaces, use one explicit "+
		"provider per workspace.")
	a.SetDefault(&c.APIKey, "", "ANTHROPIC_API_KEY")
	a.SetDefault(&c.AdminKey, "", "ANTHROPIC_ADMIN_KEY")
	a.SetDefault(&c.AuthToken, "", "ANTHROPIC_AUTH_TOKEN")
	a.SetDefault(&c.BaseURL, "", "ANTHROPIC_BASE_URL")
}

// Configure builds one client per plane that has a usable credential.
func (c *Config) Configure(context.Context) error {
	common := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	if c.BaseURL != "" {
		common = append(common, option.WithBaseURL(c.BaseURL))
	}
	if c.WorkspaceID != "" {
		common = append(common, option.WithHeader("anthropic-workspace-id", c.WorkspaceID))
	}
	newClient := func(auth option.RequestOption) *anthropic.Client {
		client := anthropic.NewClient(append(common, auth)...)
		return &client
	}

	c.clients = map[Plane]*anthropic.Client{}
	switch {
	case c.APIKey != "":
		c.clients[Workspace] = newClient(option.WithAPIKey(c.APIKey))
	case c.AuthToken != "":
		c.clients[Workspace] = newClient(option.WithAuthToken(c.AuthToken))
	}
	switch {
	case c.AdminKey != "":
		c.clients[Admin] = newClient(option.WithAPIKey(c.AdminKey))
	case c.AuthToken != "":
		c.clients[Admin] = newClient(option.WithAuthToken(c.AuthToken))
	}
	if c.AuthToken != "" {
		c.clients[OrgAdminOAuth] = newClient(option.WithAuthToken(c.AuthToken))
	}
	return nil
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
