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
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

const (
	tunnelToken            = "anthropic:agents:Tunnel"
	tunnelCertificateToken = "anthropic:agents:TunnelCertificate"
)

var (
	tunnelURN            = providertest.URN(tunnelToken)
	tunnelCertificateURN = providertest.URN(tunnelCertificateToken)
)

// tunnelServerBehavior mimics the API: tunnels get a domain, their token is returned
// only by reveal_token, and certificates get a fingerprint.
func tunnelServerBehavior(api *fakeapi.Server) {
	api.SetHooks("tunnels", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) { obj["domain"] = obj["id"].(string) + ".tunnels.example.com" },
	})
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{
			"type": "tunnel_token", "id": "tok_1", "tunnel_token": "secret-" + r.PathValue("id"),
		})
	})
	api.SetHooks("certificates", fakeapi.Hooks{
		OnCreate: func(obj fakeapi.Object) {
			obj["fingerprint"] = "ab:cd"
			obj["expires_at"] = "2030-01-01T00:00:00Z"
		},
	})
}

func TestTunnelLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	tunnelServerBehavior(api)

	integration.LifeCycleTest{
		Resource: tunnelToken,
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"displayName": "corp"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/tunnels")
				assert.Equal(t, "mcp-tunnels-2026-06-22", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{"display_name": "corp"}, req.Body)
				assert.Regexp(t, `/v1/tunnels/tunnel_\d+/reveal_token$`, api.Last().Path)
				token := output.Get("tunnelToken")
				assert.True(t, token.Secret())
				assert.Regexp(t, `^secret-tunnel_\d+$`, token.AsString())
				assert.Regexp(t, `\.tunnels\.example\.com$`, output.Get("domain").AsString())
			},
		},
		Updates: []integration.Operation{{
			// Tunnels cannot be updated, so a new name is a new tunnel.
			Inputs: providertest.Props(map[string]any{"displayName": "corp-2"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, fakeapi.Object{"display_name": "corp-2"},
					api.LastRequest(t, http.MethodPost, "/v1/tunnels").Body)
			},
		}},
	}.Run(t, server)

	archived := 0
	for _, r := range api.Requests() {
		if r.Method == http.MethodPost && len(r.Path) > 8 && r.Path[len(r.Path)-8:] == "/archive" {
			archived++
		}
	}
	assert.Equal(t, 2, archived, "the replaced tunnel and the final one are both archived")
}

func TestTunnelRefreshKeepsToken(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	tunnelServerBehavior(api)
	created, inputs := providertest.Create(t, server, tunnelURN, providertest.Props(map[string]any{"displayName": "corp"}))

	refreshed := providertest.Read(t, server, tunnelURN, created, inputs)
	assert.Equal(t, created.Properties.Get("tunnelToken"), refreshed.Properties.Get("tunnelToken"),
		"the API does not return the token on read")
}

func TestTunnelCertificateLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	tunnelServerBehavior(api)

	const pem = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	created, inputs := providertest.Create(t, server, tunnelCertificateURN, providertest.Props(map[string]any{
		"tunnelId": "tnl_1", "caCertificatePem": pem,
	}))
	req := api.LastRequest(t, http.MethodPost, "/v1/tunnels/tnl_1/certificates")
	assert.Equal(t, "mcp-tunnels-2026-06-22", req.Header.Get("anthropic-beta"))
	assert.Equal(t, fakeapi.Object{"ca_certificate_pem": pem}, req.Body)
	require.Regexp(t, `^tnl_1/certificate_\d+$`, created.ID)
	assert.Equal(t, "ab:cd", created.Properties.Get("fingerprint").AsString())

	refreshed := providertest.Read(t, server, tunnelCertificateURN, created, inputs)
	assert.Equal(t, inputs, refreshed.Inputs)

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: tunnelCertificateURN})
	require.NoError(t, err)
	assert.Equal(t, "tnl_1", imported.Inputs.Get("tunnelId").AsString())

	d := providertest.Diff(t, server, tunnelCertificateURN, created, inputs, providertest.Props(map[string]any{
		"tunnelId": "tnl_1", "caCertificatePem": pem + "\n",
	}))
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["caCertificatePem"].Kind, "certificates cannot be updated")

	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: tunnelCertificateURN,
		Properties: created.Properties}))
	_, certificateID, _ := rest.SplitID(created.ID)
	last := api.Last()
	assert.Equal(t, "/v1/tunnels/tnl_1/certificates/"+certificateID+"/archive", last.Path)
}

// TestTunnelTokenFailureKeepsTheTunnel covers a failure after the tunnel is created: the
// tunnel must be recorded, not orphaned, and the next update must reveal its token.
func TestTunnelTokenFailureKeepsTheTunnel(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	failing := true
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		if failing {
			fakeapi.WriteError(w, http.StatusBadRequest, "token unavailable")
			return
		}
		fakeapi.WriteJSON(w, fakeapi.Object{"tunnel_token": "secret-" + r.PathValue("id")})
	})

	inputs := providertest.Props(map[string]any{"displayName": "corp"})
	created, err := server.Create(p.CreateRequest{Urn: tunnelURN, Properties: inputs})
	require.Error(t, err)
	var initFailed infer.ResourceInitFailedError
	require.ErrorAs(t, err, &initFailed)
	require.NotEmpty(t, created.ID, "the created tunnel is recorded")

	failing = false
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: tunnelURN,
		State: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	assert.Equal(t, "secret-"+created.ID, updated.Properties.Get("tunnelToken").AsString())
}

func TestTunnelImportRevealsToken(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	tunnelServerBehavior(api)

	created, _ := providertest.Create(t, server, tunnelURN, providertest.Props(map[string]any{"displayName": "corp"}))
	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: tunnelURN})
	require.NoError(t, err)
	assert.Equal(t, "secret-"+created.ID, imported.Properties.Get("tunnelToken").AsString())
}
