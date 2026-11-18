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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const tunnelToken = "anthropic:agents:Tunnel"

var tunnelURN = providertest.URN(tunnelToken)

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
		"reveal returns the same token until rotation")
}

// Token revelation is a second operation; failure is ordinary and may orphan the allocation.
func TestTunnelTokenFailureOrdinaryError(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	api.SetHooks("tunnels", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) { obj["domain"] = "synthetic.example.test" }})
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
	require.NotErrorAs(t, err, &initFailed)
	require.Empty(t, created.ID)
	require.Nil(t, created.PartialState)
	require.Zero(t, created.Properties.Len())
	require.Len(t, api.Requests(), 2)

	// Explicit import/refresh can recover the allocation after reconciliation.
	failing = false
	id := strings.Split(api.Last().Path, "/")[3]
	read, err := server.Read(p.ReadRequest{ID: id, Urn: tunnelURN})
	require.NoError(t, err)
	assert.Equal(t, "secret-"+id, read.Properties.Get("tunnelToken").AsString())
	require.True(t, read.Properties.Get("tunnelToken").Secret())
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

func tunnelObject(id string) fakeapi.Object {
	return fakeapi.Object{"id": id, "type": "tunnel", "display_name": "corp", "domain": id + ".example.test", "created_at": "2026-01-01T00:00:00Z", "archived_at": nil}
}

// Contract T-E1: unresolved inputs survive Check and both previews without I/O.
func TestTunnelUnknownPreviews(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Tunnel")
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: property.NewMap(map[string]property.Value{"displayName": property.New(property.Computed)})})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("displayName").IsComputed())
	preview, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("displayName").IsComputed())
	require.True(t, preview.Properties.Get("domain").IsComputed())
	require.True(t, preview.Properties.Get("tunnelToken").Secret())
	state := providertest.Props(map[string]any{"displayName": "corp", "domain": "tnl_1.example.test", "createdAt": "2026-01-01T00:00:00Z", "tunnelToken": ""})
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: "tnl_1", State: state, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, p.UpdateReplace, diff.DetailedDiff["displayName"].Kind)
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: "tnl_1", State: state, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("displayName").IsComputed())
	require.Empty(t, api.Requests(), "even recovery of an empty token must not reveal in preview")
}

// Contract T-E2: ID-only import recovers usable inputs and the token value;
// repeated equivalent diffs/updates stay stable, while add/remove names replace.
func TestTunnelImportStabilityAndReplacement(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Tunnel")
	const id = "tnl_test_9001"
	api.Put("/v1/tunnels/"+id, tunnelObject(id))
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "ttok_test", "type": "tunnel_token", "tunnel_token": "synthetic-token"})
	})
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Equal(t, id, imported.ID)
	require.Equal(t, "corp", imported.Inputs.Get("displayName").AsString())
	// Import runtime secrecy is asserted separately in TestTunnelImportTokenSecret.
	require.Equal(t, "synthetic-token", imported.Properties.Get("tunnelToken").AsString())
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	for range 2 {
		before := len(api.Requests())
		d, err := s.Diff(p.DiffRequest{Urn: urn, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
		require.NoError(t, err)
		require.False(t, d.HasChanges)
		updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Equal(t, imported.Properties.Get("domain"), updated.Properties.Get("domain"))
		require.Equal(t, imported.Properties.Get("displayName"), updated.Properties.Get("displayName"))
		require.Equal(t, "synthetic-token", updated.Properties.Get("tunnelToken").AsString())
		require.True(t, updated.Properties.Get("tunnelToken").Secret())
		require.Len(t, api.Requests(), before)
		imported, err = s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: updated.Properties, Inputs: checked.Inputs})
		require.NoError(t, err)
	}
	for _, inputs := range []property.Map{property.NewMap(nil), providertest.Props(map[string]any{"displayName": "new"})} {
		d, err := s.Diff(p.DiffRequest{Urn: urn, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: inputs})
		require.NoError(t, err)
		require.True(t, d.HasChanges)
		require.False(t, d.DeleteBeforeReplace)
		require.True(t, d.DetailedDiff["displayName"].Kind == p.DeleteReplace || d.DetailedDiff["displayName"].Kind == p.UpdateReplace)
	}
}

// Contract T-E3: readable name drift is visible,
// archival is exact/retryable, and authorization errors are not disappearance.
func TestTunnelRefreshAndArchive(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Tunnel")
	const id = "tnl_test_9002"
	const path = "/v1/tunnels/" + id
	api.Put(path, tunnelObject(id))
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"tunnel_token": "synthetic-token"})
	})
	state := providertest.Props(map[string]any{"domain": id + ".example.test", "createdAt": "2026-01-01T00:00:00Z", "tunnelToken": property.New("synthetic-token").WithSecret(true)})
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: state})
	require.NoError(t, err)
	require.Equal(t, "corp", refreshed.Inputs.Get("displayName").AsString(), "readable server names are recovered")
	declared := providertest.Props(map[string]any{"displayName": "corp", "domain": id + ".example.test", "createdAt": "2026-01-01T00:00:00Z", "tunnelToken": property.New("synthetic-token").WithSecret(true)})
	api.Mutate(path, func(obj fakeapi.Object) { obj["display_name"] = "external" })
	refreshed, err = s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: declared, Inputs: providertest.Props(map[string]any{"displayName": "corp"})})
	require.NoError(t, err)
	require.Equal(t, "external", refreshed.Inputs.Get("displayName").AsString())
	require.True(t, refreshed.Properties.Get("tunnelToken").Secret())
	for range 2 {
		require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn, ID: id, Properties: refreshed.Properties}))
		require.Equal(t, path+"/archive", api.Last().Path)
		require.Equal(t, http.MethodPost, api.Last().Method)
		require.Equal(t, "mcp-tunnels-2026-06-22", api.Last().Header.Get("anthropic-beta"))
	}
	gone, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: refreshed.Properties})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
	api.Remove(path)
	gone, err = s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
	api.Handle("GET /v1/tunnels/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusForbidden, "denied")
	})
	_, err = s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.Error(t, err)
}

func TestTunnelCheckMetadataAndBounds(t *testing.T) {
	s, api := providertest.New(t)
	dep := []resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::name"}
	for _, v := range []property.Value{property.New("corp"), property.New("界" + strings.Repeat("a", 254)), property.New(property.Computed), property.Value{}} {
		for _, secret := range []bool{false, true} {
			input := property.NewMap(map[string]property.Value{"displayName": v.WithSecret(secret).WithDependencies(dep)})
			checked, err := s.Check(p.CheckRequest{Urn: tunnelURN, Inputs: input})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, input, checked.Inputs)
			again, err := s.Check(p.CheckRequest{Urn: tunnelURN, Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Equal(t, checked.Inputs, again.Inputs)
		}
	}
	for _, name := range []string{"", strings.Repeat("界", 256)} {
		resp, err := s.Check(p.CheckRequest{Urn: tunnelURN, Inputs: providertest.Props(map[string]any{"displayName": name})})
		require.NoError(t, err)
		require.NotEmpty(t, resp.Failures)
		require.Equal(t, "displayName", resp.Failures[0].Property)
	}
	require.Empty(t, api.Requests())
}

func TestTunnelIncompleteAllocationAndUpdate(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/tunnels", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "tnl_partial", "display_name": "corp", "archived_at": nil})
	})
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"tunnel_token": "synthetic-current-token"})
	})
	input := providertest.Props(map[string]any{"displayName": "corp"})
	created, err := s.Create(p.CreateRequest{Urn: tunnelURN, Properties: input})
	require.Error(t, err)
	require.Empty(t, created.ID)
	require.Nil(t, created.PartialState)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Zero(t, created.Properties.Len())
	require.Len(t, api.Requests(), 1, "do not reveal a token for an unusable allocation response")
	api.Put("/v1/tunnels/tnl_partial", tunnelObject("tnl_partial"))
	read, err := s.Read(p.ReadRequest{Urn: tunnelURN, ID: "tnl_partial", Inputs: input, Properties: input})
	require.NoError(t, err)
	require.True(t, read.Properties.Get("tunnelToken").Secret())
	before := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: tunnelURN, ID: read.ID, State: read.Properties, Inputs: input})
	require.NoError(t, err)
	require.Equal(t, read.Properties, updated.Properties)
	require.Len(t, api.Requests(), before)
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: tunnelURN, ID: read.ID, Properties: read.Properties}))
}

func TestTunnelRevealErrorsAndIdentitySafety(t *testing.T) {
	for _, response := range []fakeapi.Object{{}, {"tunnel_token": nil}, {"tunnel_token": 42}, {"tunnel_token": ""}} {
		t.Run("invalid-token", func(t *testing.T) {
			s, api := providertest.New(t)
			api.Put("/v1/tunnels/tnl_9000", tunnelObject("tnl_9000"))
			api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, response) })
			_, err := s.Read(p.ReadRequest{Urn: tunnelURN, ID: "tnl_9000"})
			require.Error(t, err)
		})
	}
	s, api := providertest.New(t)
	api.Put("/v1/tunnels/tnl_9000", tunnelObject("tnl_foreign"))
	_, err := s.Read(p.ReadRequest{Urn: tunnelURN, ID: "tnl_9000"})
	require.ErrorContains(t, err, "identity mismatch")
	require.Len(t, api.Requests(), 1)
}

func TestTunnelAllocationNotRetried(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/tunnels", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 500, "synthetic ambiguous allocation")
	})
	created, err := s.Create(p.CreateRequest{Urn: tunnelURN, Properties: property.NewMap(nil)})
	require.Error(t, err)
	require.Empty(t, created.ID)
	require.Len(t, api.Requests(), 1)
}

// Contract T-E4: refresh must report the currently usable connector credential.
// SDK betatunnel.go:199-202,236-239 explicitly permits out-of-band rotation.
func TestTunnelRotatedTokenRefresh(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Tunnel")
	const id = "tnl_test_9003"
	api.Put("/v1/tunnels/"+id, tunnelObject(id))
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "ttok_new", "type": "tunnel_token", "tunnel_token": "new-synthetic-token"})
	})
	state := providertest.Props(map[string]any{"displayName": "corp", "domain": id + ".example.test", "createdAt": "2026-01-01T00:00:00Z", "tunnelToken": "old-synthetic-token"})
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: state, Inputs: providertest.Props(map[string]any{"displayName": "corp"})})
	require.NoError(t, err)
	// Existing state markers are preserved; import secrecy has its own regression.
	require.NotEmpty(t, refreshed.ID)
	require.Equal(t, "new-synthetic-token", refreshed.Properties.Get("tunnelToken").AsString())
}

// Contract T-E5: imported API credentials must be runtime secrets, not only schema secrets.
func TestTunnelImportTokenSecret(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Tunnel")
	const id = "tnl_test_9004"
	api.Put("/v1/tunnels/"+id, tunnelObject(id))
	api.Handle("POST /v1/tunnels/{id}/reveal_token", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "ttok_test", "type": "tunnel_token", "tunnel_token": "synthetic-token"})
	})
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Equal(t, "synthetic-token", imported.Properties.Get("tunnelToken").AsString())
	require.True(t, imported.Properties.Get("tunnelToken").Secret(), "schema secrecy must also apply on ID-only Read")
}
