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
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
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

func TestVaultLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)

	integration.LifeCycleTest{
		Resource: "anthropic:agents:Vault",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{
				"displayName": "prod", "metadata": map[string]any{"env": "prod", "owner": "infra"},
			}),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/vaults")
				assert.Equal(t, "managed-agents-2026-04-01", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{
					"display_name": "prod", "metadata": map[string]any{"env": "prod", "owner": "infra"},
				}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"displayName": "production", "metadata": map[string]any{"env": "prod"}}),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{
					"display_name": "production", "metadata": map[string]any{"env": "prod", "owner": nil},
				}, api.Last().Body)
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method, "deleting a vault deletes it")
	assert.Regexp(t, `^/v1/vaults/vault_\d+$`, last.Path)
}

func TestMemoryStoreLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	memoryStoreDefaults(api)

	integration.LifeCycleTest{
		Resource: "anthropic:agents:MemoryStore",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"name": "notes", "description": "Team notes."}),
			Hook: func(_, _ property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/memory_stores")
				assert.Equal(t, "agent-memory-2026-07-22", req.Header.Get("anthropic-beta"))
				assert.Equal(t, fakeapi.Object{"name": "notes", "description": "Team notes."}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"name": "notes"}),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{"name": "notes", "description": "", "metadata": map[string]any{}}, api.Last().Body,
					"a dropped description is cleared")
			},
		}},
	}.Run(t, server)

	last := api.Last()
	assert.Equal(t, http.MethodDelete, last.Method)
	assert.Regexp(t, `^/v1/memory_stores/memory_store_\d+$`, last.Path)
}

func TestVaultStateUsesServerValues(t *testing.T) {
	var r sdk.BetaManagedAgentsVault
	require.NoError(t, r.UnmarshalJSON([]byte(`{"id":"vlt_test","display_name":"canonical","metadata":{},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","archived_at":null}`)))
	s, err := agents.VaultStateForTest(&r, agents.VaultArgs{DisplayName: "requested", Metadata: map[string]string{"owner": "test"}}, false)
	require.NoError(t, err)
	require.Equal(t, "canonical", s.DisplayName)
	require.NotNil(t, s.Metadata)
	require.Empty(t, s.Metadata)
	s.Metadata["owner"] = "changed"
	require.Empty(t, r.Metadata, "mapping must not alias the SDK response")
}

// Contract VLT-01: unresolved names survive Check and mutation previews do no I/O.
func TestVaultPreview(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Vault")
	inputs := property.NewMap(map[string]property.Value{"displayName": property.New(property.Computed)})
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("displayName").IsComputed())
	preview, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("displayName").IsComputed())
	require.True(t, preview.Properties.Get("createdAt").IsComputed())
	old := providertest.Props(map[string]any{"displayName": "old", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: "vlt_preview", State: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("displayName").IsComputed())
	require.Empty(t, api.Requests())
}

// Contract VLT-02: ID-only reads recover usable inputs; refresh exposes declared drift.
// Nonempty readable metadata is observed even if previously omitted.
func TestVaultImportAndRefresh(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Vault")
	const id = "vlt_0001"
	const path = "/v1/vaults/" + id
	api.Put(path, fakeapi.Object{"id": id, "display_name": "imported", "metadata": map[string]any{"owner": "server"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil})
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Equal(t, id, imported.ID)
	require.Equal(t, "imported", imported.Inputs.Get("displayName").AsString())
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	d, err := s.Diff(p.DiffRequest{Urn: urn, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.False(t, d.HasChanges)
	api.Mutate(path, func(o fakeapi.Object) { o["display_name"] = "outside" })
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: imported.Properties, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Equal(t, "outside", refreshed.Properties.Get("displayName").AsString())
	d, err = s.Diff(p.DiffRequest{Urn: urn, ID: id, State: refreshed.Properties, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.True(t, d.HasChanges)
	// Do not hide nonempty server metadata behind input projection.
	state := providertest.Props(map[string]any{"displayName": "imported", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	projected, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: state})
	require.NoError(t, err)
	require.Equal(t, "server", projected.Inputs.Get("metadata").AsMap().Get("owner").AsString())
	api.Mutate(path, func(o fakeapi.Object) { o["archived_at"] = "2026-01-02T00:00:00Z" })
	archived, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: state})
	require.NoError(t, err)
	require.Empty(t, archived.ID)
	api.Remove(path)
	missing, err := s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Empty(t, missing.ID)
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusForbidden, "denied")
	})
	_, err = s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.Error(t, err, "access denial is not confirmed absence")
}

// Contract VLT-03: SDK documents null-per-key patch deletion; name is mutable.
// The fake validates request construction only, not platform removal/cascade effects.
func TestVaultClearUpdateAndDelete(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:Vault")
	created, old := providertest.Create(t, s, urn, providertest.Props(map[string]any{"displayName": "before", "metadata": map[string]any{"a": "", "b": "two"}}))
	next := providertest.Props(map[string]any{"displayName": "after"})
	diff := providertest.Diff(t, s, urn, created, old, next)
	require.True(t, diff.HasChanges)
	require.False(t, diff.DeleteBeforeReplace)
	for _, d := range diff.DetailedDiff {
		require.NotContains(t, []p.DiffKind{p.AddReplace, p.DeleteReplace, p.UpdateReplace}, d.Kind)
	}
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, Inputs: next})
	require.NoError(t, err)
	path := "/v1/vaults/" + created.ID
	require.Equal(t, fakeapi.Object{"display_name": "after", "metadata": map[string]any{"a": nil, "b": nil}}, api.LastRequest(t, http.MethodPost, path).Body)
	require.Equal(t, "after", updated.Properties.Get("displayName").AsString())
	refreshed, err := s.Read(p.ReadRequest{Urn: urn, ID: created.ID, Properties: updated.Properties, Inputs: next})
	require.NoError(t, err)
	d, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: refreshed.Properties, Inputs: next})
	require.NoError(t, err)
	require.False(t, d.HasChanges)
	err = s.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: updated.Properties})
	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, api.Last().Method)
	require.Equal(t, path, api.Last().Path)
}

// VLT-04: an unusable allocation response fails without a checkpoint.
func TestVaultMalformedCreateOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/vaults", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vlt_created", "display_name": "created", "created_at": map[string]any{"unexpected": true}})
	})
	resp, err := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:Vault"), Properties: providertest.Props(map[string]any{"displayName": "created"})})
	require.Error(t, err)
	require.Empty(t, resp.ID)
	require.Nil(t, resp.PartialState)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Zero(t, resp.Properties.Len())
}

// Invalid mutation responses return ordinary errors without state reconstruction.
func TestVaultInvalidMutableResponsesOrdinaryError(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		displayName, metadata any
	}{
		{"null", nil, nil},
		{"wrong-types", map[string]any{"invalid": true}, []any{"invalid"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			u := providertest.URN("anthropic:agents:Vault")
			const id = "vlt_invalid"
			invalid := fakeapi.Object{"id": id, "display_name": tc.displayName, "metadata": tc.metadata, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-02T00:00:00Z"}
			api.Handle("POST /v1/vaults", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, invalid) })
			inputs := providertest.Props(map[string]any{"displayName": "accepted", "metadata": map[string]any{"owner": "test", "remove": "old"}})
			created, e := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
			require.Error(t, e)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.NotContains(t, e.Error(), "failed to initialize")
			require.Zero(t, created.Properties.Len())
			state := inputs.Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))

			complete := false
			api.Handle("POST /v1/vaults/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				if complete {
					fakeapi.WriteJSON(w, fakeapi.Object{"id": id, "display_name": "accepted", "metadata": map[string]any{"owner": "test"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-03T00:00:00Z"})
					return
				}
				// Missing creation time makes this response unusable.
				fakeapi.WriteJSON(w, fakeapi.Object{"id": id, "display_name": tc.displayName, "metadata": tc.metadata, "created_at": nil, "updated_at": "2026-01-02T00:00:00Z"})
			})
			n := len(api.Requests())
			partial, e := s.Update(p.UpdateRequest{Urn: u, ID: id, State: state, Inputs: inputs})
			require.Error(t, e)
			require.Nil(t, partial.PartialState)
			require.NotContains(t, e.Error(), "failed to initialize")
			require.Zero(t, partial.Properties.Len())
			require.Len(t, api.Requests(), n+1)
			require.Equal(t, http.MethodPost, api.Last().Method)

			// Retry with the engine's saved state, not reconstructed failed output.
			complete = true
			desired := providertest.Props(map[string]any{"displayName": "accepted", "metadata": map[string]any{"owner": "test"}})
			n = len(api.Requests())
			recovered, e := s.Update(p.UpdateRequest{Urn: u, ID: id, State: state, Inputs: desired})
			require.NoError(t, e)
			require.Nil(t, recovered.PartialState)
			require.Len(t, api.Requests(), n+1)
			require.Equal(t, http.MethodPost, api.Last().Method)
			require.Equal(t, fakeapi.Object{"display_name": "accepted", "metadata": map[string]any{"owner": "test", "remove": nil}}, api.Last().Body)
			require.Equal(t, desired.Get("metadata"), recovered.Properties.Get("metadata"))
			// Read must never substitute desired inputs for the same invalid observation.
			api.Handle("GET /v1/vaults/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, invalid) })
			_, e = s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: inputs, Properties: state})
			require.Error(t, e)
		})
	}
}

func TestVaultSDKPresenceAndNonmutation(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]string
		expected string
	}{
		{nil, `{"display_name":"name"}`},
		{map[string]string{}, `{"display_name":"name","metadata":{}}`},
		{map[string]string{"k": ""}, `{"display_name":"name","metadata":{"k":""}}`},
	} {
		args := agents.VaultArgs{DisplayName: "name", Metadata: tc.metadata}
		b, e := json.Marshal(agents.VaultNewForTest(args))
		require.NoError(t, e)
		require.JSONEq(t, tc.expected, string(b))
	}
	old := agents.VaultArgs{DisplayName: "name", Metadata: map[string]string{"keep": "v", "remove": ""}}
	desired := agents.VaultArgs{DisplayName: "name", Metadata: map[string]string{"keep": "v", "empty": ""}}
	b, e := json.Marshal(agents.VaultUpdateForTest(old, desired))
	require.NoError(t, e)
	require.JSONEq(t, `{"display_name":"name","metadata":{"keep":"v","empty":"","remove":null}}`, string(b))
	require.Equal(t, map[string]string{"keep": "v", "remove": ""}, old.Metadata)
	require.Equal(t, map[string]string{"keep": "v", "empty": ""}, desired.Metadata)
	params := agents.VaultNewForTest(desired)
	params.Metadata["keep"] = "different"
	require.Equal(t, "v", desired.Metadata["keep"])
}

func TestVaultIncompleteReadNeverBecomesAbsent(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("GET /v1/vaults/vlt_bad", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vlt_bad", "display_name": "live", "created_at": map[string]any{"bad": true}})
	})
	_, e := s.Read(p.ReadRequest{Urn: providertest.URN("anthropic:agents:Vault"), ID: "vlt_bad"})
	require.Error(t, e, "malformed observation is not absence")
}

func TestVaultIncompleteMutationOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:Vault")
	api.Handle("POST /v1/vaults", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vlt_partial"})
	})
	inputs := providertest.Props(map[string]any{"displayName": "accepted", "metadata": map[string]any{"owner": "test"}})
	created, e := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.Error(t, e)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, created.Properties.Len())
	// An Update from existing complete state must also fail without a checkpoint.
	state := inputs.Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
	api.Handle("POST /v1/vaults/vlt_partial", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "vlt_partial", "display_name": "accepted", "metadata": map[string]any{"owner": "test"}})
	})
	n := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: u, ID: "vlt_partial", State: state, Inputs: inputs})
	require.Error(t, e)
	require.Nil(t, updated.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Len(t, api.Requests(), n+1)
	require.Zero(t, updated.Properties.Len())
}

func TestVaultCheckConstraintsAndMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:Vault")
	for _, in := range []map[string]any{
		{"displayName": ""}, {"displayName": strings.Repeat("x", 256)},
		{"displayName": "ok", "metadata": map[string]any{strings.Repeat("k", 65): "v"}},
		{"displayName": "ok", "metadata": map[string]any{"k": strings.Repeat("x", 513)}},
	} {
		c, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(in)})
		require.NoError(t, e)
		require.NotEmpty(t, c.Failures)
	}
	pairs := map[string]any{}
	for i := 0; i < 17; i++ {
		pairs[fmt.Sprint(i)] = "v"
	}
	c, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(map[string]any{"displayName": "ok", "metadata": pairs})})
	require.NoError(t, e)
	require.NotEmpty(t, c.Failures)
	for _, unknown := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New("valid")
			if unknown {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::value"})
			for _, metadata := range []any{v, map[string]any{"key": v}} {
				in := providertest.Props(map[string]any{"displayName": v, "metadata": metadata})
				if !unknown {
					in = in.Set("metadata", property.New(property.NewMap(map[string]property.Value{"key": v})))
				}
				c, e := s.Check(p.CheckRequest{Urn: u, Inputs: in})
				require.NoError(t, e)
				require.Empty(t, c.Failures)
				require.Equal(t, in, c.Inputs)
				again, e := s.Check(p.CheckRequest{Urn: u, Inputs: c.Inputs})
				require.NoError(t, e)
				require.Equal(t, c.Inputs, again.Inputs)
				require.Equal(t, v, in.Get("displayName"))
			}
		}
	}
	boundary, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(map[string]any{"displayName": strings.Repeat("é", 255), "metadata": map[string]any{strings.Repeat("é", 64): strings.Repeat("é", 512)}})})
	require.NoError(t, e)
	require.Empty(t, boundary.Failures)
	// Invalid known sibling is still rejected when another field is unknown.
	c, e = s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(map[string]any{"displayName": "", "metadata": property.New(property.Computed)})})
	require.NoError(t, e)
	require.NotEmpty(t, c.Failures)
	require.Empty(t, api.Requests())
}

func TestVaultFailedCreateRefreshAndErrors(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:Vault")
	const id = "vlt_partial"
	obj := fakeapi.Object{"id": id, "display_name": "desired", "metadata": map[string]any{"owner": "test"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	api.Put("/v1/vaults/"+id, obj)
	api.Handle("POST /v1/vaults", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": id, "display_name": "desired", "metadata": map[string]any{"owner": "test"}, "created_at": map[string]any{"bad": true}, "updated_at": "2026-01-01T00:00:00Z"})
	})
	in := providertest.Props(map[string]any{"displayName": "desired", "metadata": map[string]any{"owner": "test"}})
	partial, e := s.Create(p.CreateRequest{Urn: u, Properties: in})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.Empty(t, partial.ID)
	require.NotContains(t, e.Error(), "failed to initialize")
	read, e := s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: in, Properties: in})
	require.NoError(t, e)
	n := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: u, ID: id, State: read.Properties, OldInputs: in, Inputs: in})
	require.NoError(t, e)
	require.Nil(t, updated.PartialState)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, fakeapi.Object{"display_name": "desired", "metadata": map[string]any{"owner": "test"}}, api.Last().Body)
	require.Equal(t, "2026-01-01T00:00:00Z", updated.Properties.Get("createdAt").AsString())
	a, e := s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: in, Properties: updated.Properties})
	require.NoError(t, e)
	b, e := s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: in, Properties: updated.Properties})
	require.NoError(t, e)
	require.Equal(t, a, b)
	status := 400
	api.Handle("GET /v1/vaults/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic failure")
	})
	api.Handle("DELETE /v1/vaults/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic failure")
	})
	for _, code := range []int{400, 403, 409, 404} {
		status = code
		read, readErr := s.Read(p.ReadRequest{Urn: u, ID: id})
		e = s.Delete(p.DeleteRequest{Urn: u, ID: id, Properties: updated.Properties})
		if code == 404 {
			require.NoError(t, readErr)
			require.Empty(t, read.ID)
			require.NoError(t, e)
		} else {
			require.Error(t, readErr)
			require.Error(t, e)
		}
	}
}

func TestVaultConservativeCreateRetry(t *testing.T) {
	s, api := providertest.New(t)
	calls := 0
	api.Handle("POST /v1/vaults", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		calls++
		fakeapi.WriteError(w, 500, "synthetic possibly committed create")
	})
	_, e := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:Vault"), Properties: providertest.Props(map[string]any{"displayName": "test"})})
	require.Error(t, e)
	require.Equal(t, 1, calls, "generated New uses conservative retries")
}

func TestVaultUnknownDiffAndPresence(t *testing.T) {
	s, api := providertest.New(t)
	api.SetHooks("vaults", fakeapi.Hooks{OnCreate: func(o fakeapi.Object) {
		if _, ok := o["metadata"]; !ok {
			o["metadata"] = map[string]any{}
		}
	}})
	u := providertest.URN("anthropic:agents:Vault")
	created, in := providertest.Create(t, s, u, providertest.Props(map[string]any{"displayName": "test", "metadata": map[string]any{"k": ""}}))
	next := in.Set("metadata", property.New(property.NewMap(map[string]property.Value{"k": property.New(property.Computed)})))
	d, e := s.Diff(p.DiffRequest{Urn: u, ID: created.ID, State: created.Properties, OldInputs: in, Inputs: next})
	require.NoError(t, e)
	require.True(t, d.HasChanges)
	for _, diff := range d.DetailedDiff {
		require.Contains(t, []p.DiffKind{p.Add, p.Delete, p.Update}, diff.Kind)
	}
	for _, meta := range []any{property.Value{}, map[string]any{}} {
		desired := providertest.Props(map[string]any{"displayName": "test", "metadata": meta})
		n := len(api.Requests())
		_, e := s.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: created.Properties, Inputs: desired})
		require.NoError(t, e)
		require.Len(t, api.Requests(), n+1)
		require.Equal(t, fakeapi.Object{"display_name": "test", "metadata": map[string]any{"k": nil}}, api.Last().Body)
	}
}
