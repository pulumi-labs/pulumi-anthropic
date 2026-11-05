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

package agents_test

import (
	"net/http"
	"strings"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

const credentialToken = "anthropic:agents:VaultCredential"

var credentialURN = providertest.URN(credentialToken)

// credentialServerBehavior mimics the API's write-only secrets: they are never returned.
func credentialServerBehavior(api *fakeapi.Server) {
	strip := func(obj fakeapi.Object) {
		auth := obj["auth"].(map[string]any)
		// Like the live API, the fake stores the MCP server URL without trailing slashes.
		if url, ok := auth["mcp_server_url"].(string); ok && strings.Count(url, "/") > 3 {
			auth["mcp_server_url"] = strings.TrimRight(url, "/")
		}
		for _, k := range []string{"token", "access_token", "secret_value"} {
			delete(auth, k)
		}
		if refresh, ok := auth["refresh"].(map[string]any); ok {
			delete(refresh, "refresh_token")
			if tea, ok := refresh["token_endpoint_auth"].(map[string]any); ok {
				delete(tea, "client_secret")
			}
		}
	}
	api.SetHooks("credentials", fakeapi.Hooks{
		OnCreate: strip,
		OnUpdate: func(_, after, _ fakeapi.Object) error { strip(after); return nil },
	})
}

func bearerCredential(url, token string) map[string]any {
	return map[string]any{
		"vaultId":     "vlt_1",
		"displayName": "docs",
		"auth": map[string]any{
			"type": "static_bearer", "mcpServerUrl": url, "token": secret(token),
		},
	}
}

func oauthCredential(refreshToken string) map[string]any {
	return map[string]any{
		"vaultId": "vlt_1",
		"auth": map[string]any{
			"type":         "mcp_oauth",
			"mcpServerUrl": "https://mcp.example.com",
			"accessToken":  secret("at"),
			"refresh": map[string]any{
				"clientId":          "client",
				"refreshToken":      secret(refreshToken),
				"tokenEndpoint":     "https://auth.example.com/token",
				"tokenEndpointAuth": map[string]any{"type": "client_secret_post", "clientSecret": secret("cs")},
			},
		},
	}
}

func TestVaultCredentialLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)

	integration.LifeCycleTest{
		Resource: credentialToken,
		Create: integration.Operation{
			Inputs: providertest.Props(bearerCredential("https://mcp.example.com", "t1")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/vaults/vlt_1/credentials")
				assert.Equal(t, fakeapi.Object{
					"display_name": "docs",
					"auth": map[string]any{
						"type": "static_bearer", "mcp_server_url": "https://mcp.example.com/", "token": "t1",
					},
				}, req.Body, "the vault ID is in the path, not the body")
				token := output.Get("auth").AsMap().Get("token")
				assert.True(t, token.Secret())
				assert.Equal(t, "t1", token.AsString(), "write-only secrets are kept in state")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(bearerCredential("https://mcp.example.com", "t2")),
			Hook: func(_, _ property.Map) {
				req := api.Last()
				assert.Regexp(t, `^/v1/vaults/vlt_1/credentials/credential_\d+$`, req.Path)
				assert.Equal(t, fakeapi.Object{"auth": map[string]any{"type": "static_bearer", "token": "t2"}},
					req.Body, "immutable fields are left out of updates")
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Regexp(t, `^/v1/vaults/vlt_1/credentials/credential_\d+$`, last.Path)
}

func TestVaultCredentialOAuthUpdate(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)
	created, inputs := providertest.Create(t, server, credentialURN, providertest.Props(oauthCredential("rt1")))

	check, err := server.Check(p.CheckRequest{Urn: credentialURN, State: created.Properties,
		Inputs: providertest.Props(oauthCredential("rt2"))})
	require.NoError(t, err)
	_, err = server.Update(p.UpdateRequest{ID: created.ID, Urn: credentialURN, State: created.Properties,
		OldInputs: inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"type":         "mcp_oauth",
		"access_token": "at",
		"refresh": map[string]any{
			"refresh_token":       "rt2",
			"token_endpoint_auth": map[string]any{"type": "client_secret_post", "client_secret": "cs"},
		},
	}, api.Last().Body["auth"])
}

func TestVaultCredentialRefreshAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)
	created, inputs := providertest.Create(t, server, credentialURN, providertest.Props(bearerCredential("https://mcp.example.com", "t1")))
	require.Regexp(t, `^vlt_1/credential_\d+$`, created.ID)

	refreshed := providertest.Read(t, server, credentialURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs, "secrets the API never returns are not drift")

	_, credentialID, _ := rest.SplitID(created.ID)
	api.Mutate("/v1/vaults/vlt_1/credentials/"+credentialID, func(obj fakeapi.Object) { obj["display_name"] = "renamed" })
	refreshed = providertest.Read(t, server, credentialURN, created, inputs)
	assert.Equal(t, "renamed", refreshed.Inputs.Get("displayName").AsString())

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: credentialURN})
	require.NoError(t, err)
	assert.Equal(t, created.ID, imported.ID)
	assert.Equal(t, "vlt_1", imported.Inputs.Get("vaultId").AsString(), "an import recovers the vault from the ID")
}

// TestVaultCredentialURLNormalization covers the API storing the MCP server URL in its own
// canonical form. Were that reported as drift, the next update would replace the
// credential, since the URL cannot change in place.
func TestVaultCredentialURLNormalization(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)
	created, inputs := providertest.Create(t, server, credentialURN,
		providertest.Props(bearerCredential("https://MCP.example.com/mcp/", "t1")))
	assert.Equal(t, "https://mcp.example.com/mcp", inputs.Get("auth").AsMap().Get("mcpServerUrl").AsString())

	refreshed := providertest.Read(t, server, credentialURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs, "the API's normalization is not drift")

	for _, spelling := range []string{"https://MCP.example.com/mcp/", "https://mcp.example.com/mcp"} {
		diff := providertest.Diff(t, server, credentialURN, created, inputs,
			providertest.Props(bearerCredential(spelling, "t1")))
		assert.False(t, diff.HasChanges, spelling)
	}
}

func TestVaultCredentialDiff(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)
	created, inputs := providertest.Create(t, server, credentialURN, providertest.Props(bearerCredential("https://mcp.example.com", "t1")))

	d := providertest.Diff(t, server, credentialURN, created, inputs, providertest.Props(bearerCredential("https://mcp.example.com", "t2")))
	assert.Equal(t, map[string]p.PropertyDiff{"auth": {Kind: p.Update, InputDiff: true}}, d.DetailedDiff)
	assert.False(t, d.DeleteBeforeReplace)

	d = providertest.Diff(t, server, credentialURN, created, inputs, providertest.Props(bearerCredential("https://other.example.com", "t1")))
	assert.Equal(t, map[string]p.PropertyDiff{"auth": {Kind: p.UpdateReplace, InputDiff: true}}, d.DetailedDiff,
		"changing the MCP server replaces the credential")
	assert.True(t, d.DeleteBeforeReplace, "a replacement would collide with the original within the vault")

	moved := bearerCredential("https://mcp.example.com", "t1")
	moved["vaultId"] = "vlt_2"
	d = providertest.Diff(t, server, credentialURN, created, inputs, providertest.Props(moved))
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["vaultId"].Kind)
}

func TestVaultCredentialAddingRefreshReplaces(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	credentialServerBehavior(api)

	withoutRefresh := oauthCredential("rt")
	delete(withoutRefresh["auth"].(map[string]any), "refresh")
	created, inputs := providertest.Create(t, server, credentialURN, providertest.Props(withoutRefresh))

	d := providertest.Diff(t, server, credentialURN, created, inputs, providertest.Props(oauthCredential("rt")))
	assert.Equal(t, map[string]p.PropertyDiff{"auth": {Kind: p.UpdateReplace, InputDiff: true}}, d.DetailedDiff,
		"the update endpoint cannot set the refresh client or token endpoint")
	assert.True(t, d.DeleteBeforeReplace)
}
