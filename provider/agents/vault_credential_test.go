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

	"github.com/pulumi-labs/pulumi-anthropic/provider/agents"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const credentialToken = "anthropic:agents:VaultCredential"

var credentialURN = providertest.URN(credentialToken)

// credentialServerBehavior mimics the API's write-only secrets: they are never returned.
func credentialServerBehavior(api *fakeapi.Server) {
	strip := func(obj fakeapi.Object) {
		if _, ok := obj["metadata"]; !ok {
			obj["metadata"] = map[string]any{}
		} // SDK response requires a nonnullable map.
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
				assert.Equal(t, fakeapi.Object{"display_name": "docs", "metadata": map[string]any{}, "auth": map[string]any{"type": "static_bearer", "token": "t2"}},
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
			"scope":               "",
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

	parent, credentialID, ok := strings.Cut(created.ID, "/")
	require.True(t, ok)
	require.Equal(t, "vlt_1", parent)
	require.NotEmpty(t, credentialID)
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
	assert.Equal(t, map[string]p.PropertyDiff{"auth.token": {Kind: p.Update}}, d.DetailedDiff)
	assert.False(t, d.DeleteBeforeReplace)

	d = providertest.Diff(t, server, credentialURN, created, inputs, providertest.Props(bearerCredential("https://other.example.com", "t1")))
	assert.Equal(t, map[string]p.PropertyDiff{"auth.mcpServerUrl": {Kind: p.UpdateReplace}}, d.DetailedDiff,
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
	assert.Equal(t, map[string]p.PropertyDiff{"auth.refresh": {Kind: p.AddReplace}}, d.DetailedDiff,
		"the update endpoint cannot set the refresh client or token endpoint")
	assert.True(t, d.DeleteBeforeReplace)
}

// TestCanonicalMCPServerURL pins the normalization the live API applies to a vault
// credential's mcp_server_url, as observed against it on 2026-09-26.
func TestCanonicalMCPServerURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://api.example.com/mcp/":   "https://api.example.com/mcp",
		"https://api.example.com/a//":    "https://api.example.com/a",
		"https://API.Example.com/B":      "https://api.example.com/B",
		"https://api.example.com/":       "https://api.example.com/",
		"https://api.example.com":        "https://api.example.com/",
		"https://api.example.com:443/c":  "https://api.example.com/c",
		"https://api.example.com/d?x=1":  "https://api.example.com/d?x=1",
		"https://api.example.com/e/../f": "https://api.example.com/e/../f",
		"https://api.example.com:8443/g": "https://api.example.com:8443/g",
		"not a url":                      "not a url",
	} {
		assert.Equal(t, want, agents.CanonicalMCPServerURLForTest(in), in)
	}
}

func vaultCredentialInputs() map[string]any {
	return map[string]any{"vaultId": "vlt_test", "metadata": map[string]any{}, "auth": map[string]any{
		"type": "environment_variable", "secretName": "AUDIT_TOKEN", "secretValue": "synthetic-secret",
		"networking":        map[string]any{"type": "limited", "allowedHosts": []any{"api.example.com"}},
		"injectionLocation": map[string]any{"header": true, "body": true},
	}}
}

// Contract VC-E1: infer must preserve unknowns and previews must not issue requests.
func TestVaultCredentialUnknownPreview(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:VaultCredential")
	raw := vaultCredentialInputs()
	raw["vaultId"] = property.New(property.Computed)
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: providertest.Props(raw)})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("vaultId").IsComputed())
	preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, preview.Properties.Get("vaultId").IsComputed())
	require.True(t, preview.Properties.Get("auth").HasSecrets())
	require.Empty(t, api.Requests())
	created, inputs := providertest.Create(t, server, urn, providertest.Props(vaultCredentialInputs()))
	before := len(api.Requests())
	_, err = server.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("vaultId").IsComputed())
	require.Len(t, api.Requests(), before)
}

// Contract VC-E2: ID-only reads recover identity. An engine-requested Update
// sends managed mutable state but never fabricates missing write-only secrets.
func TestVaultCredentialImportUpdate(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:VaultCredential")
	api.Handle("GET /v1/vaults/vlt_test/credentials/vcrd_existing", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"vcrd_existing","metadata":{},"auth":{"type":"static_bearer","mcp_server_url":"https://mcp.example.com/"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`))
	})
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: "vlt_test/vcrd_existing"})
	require.NoError(t, err)
	require.Equal(t, "vlt_test/vcrd_existing", imported.ID)
	require.Equal(t, "vlt_test", imported.Inputs.Get("vaultId").AsString())
	require.True(t, imported.Inputs.Get("auth").AsMap().Get("token").IsNull())
	check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	d, err := server.Diff(p.DiffRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	require.False(t, d.HasChanges)
	api.Handle("POST /v1/vaults/vlt_test/credentials/vcrd_existing", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"metadata": map[string]any{}, "auth": map[string]any{"type": "static_bearer"}}, b)
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vcrd_existing", "metadata": map[string]any{}, "auth": map[string]any{"type": "static_bearer", "mcp_server_url": "https://mcp.example.com/"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
	})
	_, err = server.Update(p.UpdateRequest{Urn: urn, ID: imported.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	require.Len(t, api.Requests(), 2)
}

// VC-E3: injection removal uses false; metadata alone uses null tombstones.
func TestVaultCredentialRemovalRequests(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:VaultCredential")
	old := vaultCredentialInputs()
	old["metadata"] = map[string]any{"keep": "yes", "drop": "old"}
	created, inputs := providertest.Create(t, server, urn, providertest.Props(old))
	next := vaultCredentialInputs()
	next["metadata"] = map[string]any{"keep": "yes"}
	delete(next["auth"].(map[string]any)["injectionLocation"].(map[string]any), "body")
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: providertest.Props(next)})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	_, err = server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	req := api.Last()
	require.Equal(t, http.MethodPost, req.Method)
	require.Equal(t, map[string]any{"keep": "yes", "drop": nil}, req.Body["metadata"])
	auth := req.Body["auth"].(map[string]any)
	require.Equal(t, map[string]any{"header": true, "body": false}, auth["injection_location"], "public docs explicitly reject null")
	require.NotContains(t, auth, "secret_name")
}

// Contract VC-E4: physical ID, not state parent, routes deletion; only 404 is gone.
func TestVaultCredentialDeleteIdentityAndErrors(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, api := providertest.New(t)
			urn := providertest.URN("anthropic:agents:VaultCredential")
			api.Handle("DELETE /v1/vaults/vlt_test/credentials/vcrd_existing", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"test fault"}}`))
			})
			state := vaultCredentialInputs()
			state["vaultId"] = "vlt_wrong"
			state["createdAt"] = "2026-01-01T00:00:00Z"
			state["updatedAt"] = "2026-01-01T00:00:00Z"
			err := server.Delete(p.DeleteRequest{Urn: urn, ID: "vlt_test/vcrd_existing", Properties: providertest.Props(state)})
			if status == http.StatusNotFound {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, "/v1/vaults/vlt_test/credentials/vcrd_existing", api.Last().Path)
		})
	}
}

func TestVaultCredentialOAuthImport(t *testing.T) {
	s, api := providertest.New(t)
	object := fakeapi.Object{"id": "vcrd_imported", "type": "vault_credential", "vault_id": "vlt_imported", "display_name": "imported", "metadata": map[string]any{}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil, "auth": map[string]any{"type": "mcp_oauth", "mcp_server_url": "https://mcp.example.com/", "refresh": map[string]any{"client_id": "client", "token_endpoint": "https://auth.example.com/token", "token_endpoint_auth": map[string]any{"type": "client_secret_basic"}, "scope": "read"}}}
	const path = "/v1/vaults/vlt_imported/credentials/vcrd_imported"
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, object) })
	api.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"display_name": "imported", "metadata": map[string]any{}, "auth": map[string]any{"type": "mcp_oauth", "refresh": map[string]any{"scope": "read", "token_endpoint_auth": map[string]any{"type": "client_secret_basic"}}}}, b)
		fakeapi.WriteJSON(w, object)
	})
	read, e := s.Read(p.ReadRequest{Urn: credentialURN, ID: "vlt_imported/vcrd_imported"})
	require.NoError(t, e)
	a := read.Inputs.Get("auth").AsMap()
	require.True(t, a.Get("accessToken").IsNull())
	refresh := a.Get("refresh").AsMap()
	require.True(t, refresh.Get("refreshToken").IsNull())
	require.True(t, refresh.Get("tokenEndpointAuth").AsMap().Get("clientSecret").IsNull())
	check, e := s.Check(p.CheckRequest{Urn: credentialURN, Inputs: read.Inputs})
	require.NoError(t, e)
	require.Empty(t, check.Failures)
	diff, e := s.Diff(p.DiffRequest{Urn: credentialURN, ID: read.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: check.Inputs})
	require.NoError(t, e)
	require.False(t, diff.HasChanges)
	_, e = s.Update(p.UpdateRequest{Urn: credentialURN, ID: read.ID, State: read.Properties, Inputs: check.Inputs})
	require.NoError(t, e)
	require.Len(t, api.Requests(), 2)
}

// Nested secret unknowns must survive Check;
// null is expressly rejected for injection_location fields;
// restoring create-time omitted-body semantics requires false, not null.
func TestVaultCredentialSecretUnknownsAndInjectionRemoval(t *testing.T) {
	t.Run("unknown-secret-check", func(t *testing.T) {
		server, _ := providertest.New(t)
		raw := vaultCredentialInputs()
		raw["auth"].(map[string]any)["secretValue"] = property.New(property.Computed).WithSecret(true)
		checked, err := server.Check(p.CheckRequest{Urn: providertest.URN("anthropic:agents:VaultCredential"), Inputs: providertest.Props(raw)})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.True(t, checked.Inputs.Get("auth").AsMap().Get("secretValue").IsComputed(), "unknown must not become a known empty secret")
		preview, err := server.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:VaultCredential"), Properties: checked.Inputs, DryRun: true})
		require.NoError(t, err)
		require.True(t, preview.Properties.Get("auth").AsMap().Get("secretValue").IsComputed(), "preview must retain nested unknown secrets")
	})
	t.Run("injection-removal", func(t *testing.T) {
		server, api := providertest.New(t)
		urn := providertest.URN("anthropic:agents:VaultCredential")
		created, inputs := providertest.Create(t, server, urn, providertest.Props(vaultCredentialInputs()))
		next := vaultCredentialInputs()
		delete(next["auth"].(map[string]any)["injectionLocation"].(map[string]any), "body")
		check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: providertest.Props(next)})
		require.NoError(t, err)
		require.Empty(t, check.Failures)
		_, err = server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: check.Inputs})
		require.NoError(t, err)
		auth := api.Last().Body["auth"].(map[string]any)
		require.Equal(t, map[string]any{"header": true, "body": false}, auth["injection_location"])
	})
}

func credentialResponseObject() fakeapi.Object {
	return fakeapi.Object{"id": "vcrd_response", "display_name": "accepted name", "metadata": map[string]any{"owner": "accepted owner"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z", "auth": map[string]any{"type": "mcp_oauth", "mcp_server_url": "https://mcp.example.com/", "expires_at": "2099-01-01T00:00:00Z", "refresh": map[string]any{"client_id": "client", "token_endpoint": "https://auth.example.com/token", "token_endpoint_auth": map[string]any{"type": "none"}, "resource": "https://resource.example.com", "scope": "accepted scope"}}}
}

func credentialResponseInputs() property.Map {
	return providertest.Props(map[string]any{"vaultId": "vlt_response", "displayName": "accepted name", "metadata": map[string]any{"owner": "accepted owner"}, "auth": map[string]any{"type": "mcp_oauth", "mcpServerUrl": "https://mcp.example.com/", "accessToken": secret("synthetic-access"), "expiresAt": "2099-01-01T00:00:00Z", "refresh": map[string]any{"clientId": "client", "tokenEndpoint": "https://auth.example.com/token", "refreshToken": secret("synthetic-refresh"), "tokenEndpointAuth": map[string]any{"type": "none"}, "resource": "https://resource.example.com", "scope": "accepted scope"}}})
}

func responseRefresh(o fakeapi.Object) map[string]any {
	return o["auth"].(map[string]any)["refresh"].(map[string]any)
}

// SDK decoding reports malformed fields through JSON.Valid rather than Go errors.
func TestVaultCredentialMalformedResponses(t *testing.T) {
	cases := map[string]func(fakeapi.Object){
		"metadata omitted":              func(o fakeapi.Object) { delete(o, "metadata") },
		"metadata null":                 func(o fakeapi.Object) { o["metadata"] = nil },
		"metadata array":                func(o fakeapi.Object) { o["metadata"] = []any{} },
		"metadata nested invalid value": func(o fakeapi.Object) { o["metadata"] = map[string]any{"owner": map[string]any{"invalid": true}} },
		"display name":                  func(o fakeapi.Object) { o["display_name"] = map[string]any{"invalid": true} },
		"OAuth scope coerced bool":      func(o fakeapi.Object) { responseRefresh(o)["scope"] = false },
		"OAuth resource coerced number": func(o fakeapi.Object) { responseRefresh(o)["resource"] = 42 },
		"OAuth scope":                   func(o fakeapi.Object) { responseRefresh(o)["scope"] = map[string]any{"invalid": true} },
		"OAuth resource":                func(o fakeapi.Object) { responseRefresh(o)["resource"] = []any{} },
		"OAuth client":                  func(o fakeapi.Object) { responseRefresh(o)["client_id"] = map[string]any{"invalid": true} },
		"OAuth endpoint":                func(o fakeapi.Object) { responseRefresh(o)["token_endpoint"] = false },
		"OAuth mode":                    func(o fakeapi.Object) { responseRefresh(o)["token_endpoint_auth"] = map[string]any{"type": []any{}} },
		"OAuth mode invalid": func(o fakeapi.Object) {
			responseRefresh(o)["token_endpoint_auth"] = map[string]any{"type": "unexpected"}
		},
		"OAuth expiry":         func(o fakeapi.Object) { o["auth"].(map[string]any)["expires_at"] = map[string]any{"invalid": true} },
		"OAuth refresh object": func(o fakeapi.Object) { o["auth"].(map[string]any)["refresh"] = []any{} },
		"OAuth required missing": func(o fakeapi.Object) {
			delete(responseRefresh(o), "client_id")
			delete(responseRefresh(o), "token_endpoint")
			delete(responseRefresh(o), "token_endpoint_auth")
		},
		"auth type invalid": func(o fakeapi.Object) { o["auth"].(map[string]any)["type"] = "unexpected" },
		"timestamps": func(o fakeapi.Object) {
			o["created_at"] = map[string]any{"invalid": true}
			o["updated_at"] = map[string]any{"invalid": true}
		},
		"nested partial": func(o fakeapi.Object) {
			delete(responseRefresh(o), "scope")
			delete(responseRefresh(o), "resource")
			o["created_at"] = nil
			o["display_name"] = "observed name"
			o["metadata"] = map[string]any{"observed": "server"}
		},
		"environment hosts": func(o fakeapi.Object) {
			o["auth"] = map[string]any{"type": "environment_variable", "secret_name": "TEST_SECRET", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{map[string]any{"invalid": true}}}, "injection_location": map[string]any{"header": true, "body": true}}
		},
		"environment injection": func(o fakeapi.Object) {
			o["auth"] = map[string]any{"type": "environment_variable", "secret_name": "TEST_SECRET", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{"api.example.com"}}, "injection_location": map[string]any{"header": "invalid", "body": true}}
		},
	}
	for name, corrupt := range cases {
		for _, operation := range []string{"Read", "Create", "Update"} {
			t.Run(operation+"/"+name, func(t *testing.T) {
				s, api := providertest.New(t)
				object := credentialResponseObject()
				corrupt(object)
				path := "/v1/vaults/vlt_response/credentials/vcrd_response"
				method := "POST"
				if operation == "Read" {
					method = "GET"
				}
				if operation == "Create" {
					path = "/v1/vaults/vlt_response/credentials"
				}
				api.Handle(method+" "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) })
				input := credentialResponseInputs()
				switch operation {
				case "Read":
					_, err := s.Read(p.ReadRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", Inputs: input})
					require.Error(t, err)
				case "Create":
					out, err := s.Create(p.CreateRequest{Urn: credentialURN, Properties: input})
					require.Error(t, err)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Empty(t, out.ID)
					require.Nil(t, out.PartialState)
					require.Zero(t, out.Properties.Len())
				case "Update":
					prior := input.Set("createdAt", property.New("2025-01-01T00:00:00Z")).Set("updatedAt", property.New("2025-01-02T00:00:00Z"))
					out, err := s.Update(p.UpdateRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", State: prior, OldInputs: input, Inputs: input})
					require.Error(t, err)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Nil(t, out.PartialState)
					require.Zero(t, out.Properties.Len())
					require.Equal(t, fakeapi.Object{"display_name": "accepted name", "metadata": map[string]any{"owner": "accepted owner"}, "auth": map[string]any{"type": "mcp_oauth", "access_token": "synthetic-access", "expires_at": "2099-01-01T00:00:00Z", "refresh": map[string]any{"refresh_token": "synthetic-refresh", "scope": "accepted scope"}}}, api.Last().Body)
				}
				require.Len(t, api.Requests(), 1, "no internal refresh or retry")
			})
		}
	}
}

func TestVaultCredentialNullableAndEmptyObservations(t *testing.T) {
	for _, nullable := range []bool{false, true} {
		for _, operation := range []string{"Read", "Create", "Update"} {
			t.Run(operation+map[bool]string{true: "/null", false: "/empty"}[nullable], func(t *testing.T) {
				s, api := providertest.New(t)
				object := credentialResponseObject()
				var value any = ""
				if nullable {
					value = nil
				}
				responseRefresh(object)["scope"], responseRefresh(object)["resource"] = value, value
				object["display_name"] = "observed name"
				object["metadata"] = map[string]any{}
				responseRefresh(object)["client_id"] = "observed-client"
				api.Handle("GET /v1/vaults/vlt_response/credentials/vcrd_response", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) })
				api.Handle("POST /v1/vaults/vlt_response/credentials", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) })
				api.Handle("POST /v1/vaults/vlt_response/credentials/vcrd_response", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) })
				input := credentialResponseInputs()
				var state property.Map
				switch operation {
				case "Read":
					out, err := s.Read(p.ReadRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", Inputs: input})
					require.NoError(t, err)
					state = out.Properties
				case "Create":
					out, err := s.Create(p.CreateRequest{Urn: credentialURN, Properties: input})
					require.NoError(t, err)
					state = out.Properties
				case "Update":
					prior := input.Set("createdAt", property.New("2025-01-01T00:00:00Z")).Set("updatedAt", property.New("2025-01-02T00:00:00Z"))
					out, err := s.Update(p.UpdateRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", State: prior, Inputs: input})
					require.NoError(t, err)
					state = out.Properties
				}
				auth := state.Get("auth").AsMap()
				refresh := auth.Get("refresh").AsMap()
				for _, key := range []string{"scope", "resource"} {
					if nullable {
						require.True(t, refresh.Get(key).IsNull())
					} else {
						require.Equal(t, "", refresh.Get(key).AsString())
					}
				}
				require.Equal(t, "observed name", state.Get("displayName").AsString())
				require.Equal(t, "observed-client", refresh.Get("clientId").AsString())
				require.Equal(t, "https://auth.example.com/token", refresh.Get("tokenEndpoint").AsString())
				require.Equal(t, "none", refresh.Get("tokenEndpointAuth").AsMap().Get("type").AsString())
				require.Equal(t, "mcp_oauth", auth.Get("type").AsString())
				require.Equal(t, "https://mcp.example.com/", auth.Get("mcpServerUrl").AsString())
				require.Equal(t, "2099-01-01T00:00:00Z", auth.Get("expiresAt").AsString())
				require.Equal(t, "2026-01-01T00:00:00Z", state.Get("createdAt").AsString())
				require.Equal(t, "2026-01-02T00:00:00Z", state.Get("updatedAt").AsString())
				require.Zero(t, state.Get("metadata").AsMap().Len())
				require.Equal(t, "synthetic-access", auth.Get("accessToken").AsString())
				require.True(t, auth.Get("accessToken").Secret())
				require.True(t, refresh.Get("refreshToken").Secret())
			})
		}
	}
}

func TestVaultCredentialEnvironmentServerMapping(t *testing.T) {
	for _, operation := range []string{"Create", "Update", "Read"} {
		t.Run(operation, func(t *testing.T) {
			s, api := providertest.New(t)
			input := providertest.Props(map[string]any{"vaultId": "vlt_response", "auth": map[string]any{"type": "environment_variable", "secretName": "REQUESTED_NAME", "secretValue": secret("synthetic-value"), "networking": map[string]any{"type": "limited", "allowedHosts": []any{}}, "injectionLocation": map[string]any{"header": true, "body": false}}})
			object := credentialResponseObject()
			object["auth"] = map[string]any{"type": "environment_variable", "secret_name": "OBSERVED_NAME", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{}}, "injection_location": map[string]any{"header": false, "body": true}}
			serve := func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) }
			api.Handle("POST /v1/vaults/vlt_response/credentials", serve)
			api.Handle("POST /v1/vaults/vlt_response/credentials/vcrd_response", serve)
			api.Handle("GET /v1/vaults/vlt_response/credentials/vcrd_response", serve)
			var state property.Map
			switch operation {
			case "Create":
				out, err := s.Create(p.CreateRequest{Urn: credentialURN, Properties: input})
				require.NoError(t, err)
				state = out.Properties
			case "Update":
				prior := input.Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-02T00:00:00Z"))
				out, err := s.Update(p.UpdateRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", State: prior, Inputs: input})
				require.NoError(t, err)
				state = out.Properties
			case "Read":
				out, err := s.Read(p.ReadRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", Inputs: input})
				require.NoError(t, err)
				state = out.Properties
			}
			auth := state.Get("auth").AsMap()
			require.Equal(t, "OBSERVED_NAME", auth.Get("secretName").AsString())
			locations := auth.Get("injectionLocation").AsMap()
			require.False(t, locations.Get("header").AsBool())
			require.True(t, locations.Get("body").AsBool())
			hosts := auth.Get("networking").AsMap().Get("allowedHosts")
			require.True(t, hosts.IsArray())
			require.Zero(t, hosts.AsArray().Len())
			require.Equal(t, "synthetic-value", auth.Get("secretValue").AsString())
			require.True(t, auth.Get("secretValue").Secret())
			require.Len(t, api.Requests(), 1)
		})
	}
}

func TestVaultCredentialEnvironmentUnusableMembers(t *testing.T) {
	for _, operation := range []string{"Create", "Update"} {
		for _, variant := range []string{"missing-object", "missing-list", "omitted-locations", "observed-false"} {
			t.Run(operation+"/"+variant, func(t *testing.T) {
				s, api := providertest.New(t)
				input := providertest.Props(map[string]any{"vaultId": "vlt_response", "auth": map[string]any{"type": "environment_variable", "secretName": "REQUESTED_NAME", "secretValue": secret("synthetic-value"), "networking": map[string]any{"type": "limited", "allowedHosts": []any{}}, "injectionLocation": map[string]any{"header": true, "body": false}}})
				object := credentialResponseObject()
				auth := map[string]any{"type": "environment_variable", "secret_name": "OBSERVED_NAME", "networking": map[string]any{"type": "limited"}, "injection_location": map[string]any{"header": true}}
				object["auth"] = auth
				if variant == "missing-object" {
					delete(auth, "networking")
				}
				if variant == "omitted-locations" {
					a := input.Get("auth")
					input = input.Set("auth", property.WithGoValue(a, a.AsMap().Delete("injectionLocation")))
				}
				if variant == "observed-false" {
					auth["injection_location"] = map[string]any{"header": false}
				}
				path := "/v1/vaults/vlt_response/credentials"
				if operation == "Update" {
					path += "/vcrd_response"
				}
				api.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, object) })
				if operation == "Create" {
					out, err := s.Create(p.CreateRequest{Urn: credentialURN, Properties: input})
					require.Error(t, err)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Nil(t, out.PartialState)
					require.Empty(t, out.ID)
					require.Zero(t, out.Properties.Len())
				} else {
					state := input.Set("createdAt", property.New("2025-01-01T00:00:00Z")).Set("updatedAt", property.New("2025-01-02T00:00:00Z"))
					out, err := s.Update(p.UpdateRequest{Urn: credentialURN, ID: "vlt_response/vcrd_response", State: state, Inputs: input})
					require.Error(t, err)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Nil(t, out.PartialState)
					require.Zero(t, out.Properties.Len())
				}
				require.Len(t, api.Requests(), 1)
			})
		}
	}
}

func TestVaultCredentialMetadataAndValidation(t *testing.T) {
	s, api := providertest.New(t)
	for _, unknown := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New("synthetic")
			if unknown {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::x"})
			for _, field := range []string{"token", "accessToken", "secretValue"} {
				var raw map[string]any
				switch field {
				case "token":
					raw = bearerCredential("https://MCP.example.com/mcp/", "t")
				case "accessToken":
					raw = oauthCredential("rt")
				case "secretValue":
					raw = vaultCredentialInputs()
				}
				raw["auth"].(map[string]any)[field] = v
				in := providertest.Props(raw)
				c, e := s.Check(p.CheckRequest{Urn: credentialURN, Inputs: in})
				require.NoError(t, e)
				require.Empty(t, c.Failures)
				require.Equal(t, v.WithSecret(true), c.Inputs.Get("auth").AsMap().Get(field))
				require.Equal(t, v, in.Get("auth").AsMap().Get(field), "request must not mutate")
				again, e := s.Check(p.CheckRequest{Urn: credentialURN, Inputs: c.Inputs})
				require.NoError(t, e)
				require.Equal(t, c.Inputs, again.Inputs)
			}
			for _, field := range []string{"auth", "vaultId"} {
				in := providertest.Props(vaultCredentialInputs()).Set(field, v)
				if !unknown {
					continue
				}
				c, e := s.Check(p.CheckRequest{Urn: credentialURN, Inputs: in})
				require.NoError(t, e)
				require.Empty(t, c.Failures)
				require.Equal(t, v, c.Inputs.Get(field))
			}
		}
	}
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["type"] = "static_bearer" },
		func(a map[string]any) { a["injectionLocation"] = map[string]any{"header": false, "body": false} },
		func(a map[string]any) {
			a["networking"] = map[string]any{"type": "limited", "allowedHosts": []any{"https://example.com"}}
		},
		func(a map[string]any) { a["expiresAt"] = "not-a-date" },
	} {
		raw := vaultCredentialInputs()
		change(raw["auth"].(map[string]any))
		c, e := s.Check(p.CheckRequest{Urn: credentialURN, Inputs: providertest.Props(raw)})
		require.NoError(t, e)
		require.NotEmpty(t, c.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestVaultCredentialOAuthModeReplacementAndScopeClear(t *testing.T) {
	s, api := providertest.New(t)
	credentialServerBehavior(api)
	raw := oauthCredential("rt")
	raw["auth"].(map[string]any)["refresh"].(map[string]any)["scope"] = "read write"
	created, inputs := providertest.Create(t, s, credentialURN, providertest.Props(raw))
	for _, mode := range []string{"none", "client_secret_basic"} {
		next := oauthCredential("rt")
		tea := next["auth"].(map[string]any)["refresh"].(map[string]any)["tokenEndpointAuth"].(map[string]any)
		tea["type"] = mode
		if mode == "none" {
			delete(tea, "clientSecret")
		}
		d := providertest.Diff(t, s, credentialURN, created, inputs, providertest.Props(next))
		require.Equal(t, p.UpdateReplace, d.DetailedDiff["auth.refresh.tokenEndpointAuth.type"].Kind)
		require.True(t, d.DeleteBeforeReplace)
	}
	next := providertest.Props(oauthCredential("rt"))
	n := len(api.Requests())
	_, e := s.Update(p.UpdateRequest{Urn: credentialURN, ID: created.ID, State: created.Properties, Inputs: next})
	require.NoError(t, e)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	auth := api.Last().Body["auth"].(map[string]any)
	refresh := auth["refresh"].(map[string]any)
	require.Equal(t, "", refresh["scope"])
	require.NotContains(t, refresh, "client_id")
	require.NotContains(t, refresh, "token_endpoint")
	require.Equal(t, "at", auth["access_token"])
}

func TestVaultCredentialUnusableCreateThenRefresh(t *testing.T) {
	s, api := providertest.New(t)
	input := providertest.Props(bearerCredential("https://mcp.example.com/", "synthetic-token"))
	const path = "/v1/vaults/vlt_1/credentials/vcrd_partial"
	full := fakeapi.Object{"id": "vcrd_partial", "metadata": map[string]any{}, "auth": map[string]any{"type": "static_bearer", "mcp_server_url": "https://mcp.example.com/"}, "display_name": "docs", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	api.Handle("POST /v1/vaults/vlt_1/credentials", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vcrd_partial", "metadata": map[string]any{}, "auth": full["auth"], "display_name": "docs", "created_at": map[string]any{"invalid": true}, "updated_at": "2026-01-01T00:00:00Z"})
	})
	api.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"display_name": "docs", "metadata": map[string]any{}, "auth": map[string]any{"type": "static_bearer", "token": "synthetic-token"}}, b)
		fakeapi.WriteJSON(w, full)
	})
	partial, e := s.Create(p.CreateRequest{Urn: credentialURN, Properties: input})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.Empty(t, partial.ID)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, partial.Properties.Len())
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, full) })
	read, e := s.Read(p.ReadRequest{Urn: credentialURN, ID: "vlt_1/vcrd_partial", Inputs: input, Properties: input})
	require.NoError(t, e)
	require.True(t, read.Properties.Get("auth").AsMap().Get("token").Secret())
	n := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: credentialURN, ID: read.ID, State: read.Properties, OldInputs: input, Inputs: input})
	require.NoError(t, e)
	require.Nil(t, updated.PartialState)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, "2026-01-01T00:00:00Z", updated.Properties.Get("createdAt").AsString())
}

func TestVaultCredentialReadErrorsAndDrift(t *testing.T) {
	s, api := providertest.New(t)
	raw := vaultCredentialInputs()
	created, inputs := providertest.Create(t, s, credentialURN, providertest.Props(raw))
	path := "/v1/vaults/" + created.ID[:len("vlt_test")] + "/credentials/" + created.ID[len("vlt_test/"):]
	api.Mutate(path, func(o fakeapi.Object) {
		o["metadata"] = map[string]any{}
		o["auth"].(map[string]any)["injection_location"] = map[string]any{"header": false, "body": true}
	})
	a, e := s.Read(p.ReadRequest{Urn: credentialURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, e)
	b, e := s.Read(p.ReadRequest{Urn: credentialURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, e)
	require.Equal(t, a, b)
	require.False(t, a.Inputs.Get("auth").AsMap().Get("injectionLocation").AsMap().Get("header").AsBool())
	status := 403
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic failure")
	})
	for _, code := range []int{403, 400, 404} {
		status = code
		r, e := s.Read(p.ReadRequest{Urn: credentialURN, ID: created.ID})
		if code == 404 {
			require.NoError(t, e)
			require.Empty(t, r.ID)
		} else {
			require.Error(t, e)
		}
	}
}
