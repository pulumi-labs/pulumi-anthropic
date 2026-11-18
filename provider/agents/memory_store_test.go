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
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// MemoryStore fixtures model SDK-documented empty response defaults, not API evidence.
func memoryStoreDefaults(api *fakeapi.Server) {
	api.SetHooks("memory_stores", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		if _, ok := obj["description"]; !ok {
			obj["description"] = ""
		}
		if _, ok := obj["metadata"]; !ok {
			obj["metadata"] = map[string]any{}
		}
	}})
}

// Contract MS-E1: infer must preserve unknowns and previews must not perform I/O.
func TestMemoryStoreUnknownPreviews(t *testing.T) {
	s, api := providertest.New(t)
	memoryStoreDefaults(api)
	urn := providertest.URN("anthropic:agents:MemoryStore")
	inputs := providertest.Props(map[string]any{"name": property.New(property.Computed), "description": property.New(property.Computed)})
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("name").IsComputed())
	preview, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("name").IsComputed())
	require.Empty(t, api.Requests())
	created, old := providertest.Create(t, s, urn, providertest.Props(map[string]any{"name": "notes"}))
	n := len(api.Requests())
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("name").IsComputed())
	require.Len(t, api.Requests(), n)
}

// Contract MS-E2: an ID-only read recovers inputs; defaults do not drift,
// declared fields do drift, and absent/archived stores read as gone.
func TestMemoryStoreImportRefreshAndGone(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:agents:MemoryStore")
	const id = "memstore_9001"
	const path = "/v1/memory_stores/" + id
	api.Put(path, fakeapi.Object{"id": id, "name": "notes", "description": "", "metadata": map[string]any{"owner": "team"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Equal(t, id, imported.ID)
	require.Equal(t, "notes", imported.Inputs.Get("name").AsString())
	require.Equal(t, "team", imported.Inputs.Get("metadata").AsMap().Get("owner").AsString())
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: id, State: imported.Properties, OldInputs: imported.Inputs, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	api.SetHooks("memory_stores", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) { obj["description"] = ""; obj["metadata"] = map[string]any{} }})
	created, inputs := providertest.Create(t, s, urn, providertest.Props(map[string]any{"name": "declared"}))
	refreshed := providertest.Read(t, s, urn, created, inputs)
	require.True(t, refreshed.Inputs.Get("description").IsNull())
	require.False(t, providertest.Diff(t, s, urn, p.CreateResponse{ID: created.ID, Properties: refreshed.Properties}, refreshed.Inputs, inputs).HasChanges)
	api.Mutate("/v1/memory_stores/"+string(created.ID), func(obj fakeapi.Object) { obj["name"] = "external" })
	refreshed = providertest.Read(t, s, urn, created, inputs)
	require.Equal(t, "external", refreshed.Inputs.Get("name").AsString())
	require.True(t, providertest.Diff(t, s, urn, p.CreateResponse{ID: created.ID, Properties: refreshed.Properties}, refreshed.Inputs, inputs).HasChanges)
	api.Mutate(path, func(obj fakeapi.Object) { obj["archived_at"] = "2026-01-02T00:00:00Z" })
	gone, err := s.Read(p.ReadRequest{Urn: urn, ID: id, Properties: imported.Properties, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
	api.Remove(path)
	gone, err = s.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
	err = s.Delete(p.DeleteRequest{Urn: urn, ID: id, Properties: imported.Properties})
	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, api.Last().Method)
}

// Contract MS-E3: SDK documents mutable non-unique names and per-key metadata
// null tombstones. The fake demonstrates request mapping, not server semantics.
func TestMemoryStoreMutablePatchAndConvergence(t *testing.T) {
	s, api := providertest.New(t)
	memoryStoreDefaults(api)
	urn := providertest.URN("anthropic:agents:MemoryStore")
	created, old := providertest.Create(t, s, urn, providertest.Props(map[string]any{"name": "notes", "metadata": map[string]any{"owner": "team", "old": "value"}}))
	next := providertest.Props(map[string]any{"name": "Renamed Notes", "metadata": map[string]any{"owner": "team"}})
	diff := providertest.Diff(t, s, urn, created, old, next)
	require.True(t, diff.HasChanges)
	require.False(t, diff.DeleteBeforeReplace)
	// infer reports the removed metadata key as Delete, not a whole-map Update.
	// Neither kind is replacement; both must remain an in-place mutation.
	for _, d := range diff.DetailedDiff {
		require.Contains(t, []p.DiffKind{p.Update, p.Delete, p.Add}, d.Kind)
	}
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: old, Inputs: next})
	require.NoError(t, err)
	require.Len(t, api.Requests(), n+1, "Update must not refresh its own state")
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, "/v1/memory_stores/"+string(created.ID), api.Last().Path)
	require.Equal(t, fakeapi.Object{"name": "Renamed Notes", "description": "", "metadata": map[string]any{"owner": "team", "old": nil}}, api.Last().Body)
	refreshed := providertest.Read(t, s, urn, p.CreateResponse{ID: created.ID, Properties: updated.Properties}, next)
	require.False(t, providertest.Diff(t, s, urn, p.CreateResponse{ID: created.ID, Properties: refreshed.Properties}, refreshed.Inputs, next).HasChanges)
	err = s.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: updated.Properties})
	require.NoError(t, err)
	require.Equal(t, http.MethodDelete, api.Last().Method)
	require.Nil(t, api.Object("/v1/memory_stores/"+string(created.ID)))
}

// An unusable allocation response is an ordinary failure, not a checkpoint.
func TestMemoryStoreMalformedCreateOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/memory_stores", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "memstore_created", "name": "notes", "created_at": map[string]any{"unexpected": true}, "updated_at": "2026-01-01T00:00:00Z"})
	})
	response, err := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:MemoryStore"), Properties: providertest.Props(map[string]any{"name": "notes"})})
	require.Error(t, err)
	require.Empty(t, response.ID)
	require.Nil(t, response.PartialState)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Zero(t, response.Properties.Len())
}

// Nil successful responses and rejections both return ordinary errors.
func TestMemoryStoreNullUpdateOrdinaryError(t *testing.T) {
	for _, fields := range []string{"populated", "empty", "omitted"} {
		for _, prior := range []string{"known", "unknown"} {
			for _, outcome := range []string{"success", "rejected"} {
				t.Run(fields+"/"+prior+"/"+outcome, func(t *testing.T) {
					s, api := providertest.New(t)
					u := providertest.URN("anthropic:agents:MemoryStore")
					createdAt := "2026-01-01T00:00:00Z"
					if prior == "unknown" {
						createdAt = ""
					}
					state := providertest.Props(map[string]any{"name": "old", "description": "old", "metadata": map[string]any{"removed": "old"}, "createdAt": createdAt, "updatedAt": "2026-01-02T00:00:00Z"})
					values := map[string]any{"name": "accepted"}
					metadata := map[string]any{"removed": nil}
					description := ""
					switch fields {
					case "populated":
						description = "accepted"
						values["description"] = description
						values["metadata"] = map[string]any{"owner": "accepted"}
						metadata["owner"] = "accepted"
					case "empty":
						values["description"] = ""
						values["metadata"] = map[string]any{}
					}
					desired := providertest.Props(values)
					api.Handle("POST /v1/memory_stores/memstore_partial", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
						require.Equal(t, fakeapi.Object{"name": "accepted", "description": description, "metadata": metadata}, body)
						w.Header().Set("Content-Type", "application/json")
						if outcome == "rejected" {
							w.WriteHeader(http.StatusBadRequest)
						}
						_, _ = w.Write([]byte("null"))
					})
					out, err := s.Update(p.UpdateRequest{Urn: u, ID: "memstore_partial", State: state, Inputs: desired})
					require.Error(t, err)
					require.Len(t, api.Requests(), 1, "no internal refresh or retry")
					require.Equal(t, http.MethodPost, api.Last().Method)
					require.Nil(t, out.PartialState)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Zero(t, out.Properties.Len(), "failed mutation must not reconstruct accepted state")
					require.Equal(t, "old", state.Get("name").AsString())
					require.Equal(t, "accepted", desired.Get("name").AsString())
				})
			}
		}
	}
}

func TestMemoryStoreNullCreateDoesNotInventIdentity(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/memory_stores", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("null"))
	})
	out, err := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:MemoryStore"), Properties: providertest.Props(map[string]any{"name": "accepted"})})
	require.Error(t, err)
	require.Empty(t, out.ID)
	require.Nil(t, out.PartialState, "no usable returned identity exists")
	require.Len(t, api.Requests(), 1)
}

func TestMemoryStoreIncompleteResponseThenRefresh(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:MemoryStore")
	obj := fakeapi.Object{"id": "memstore_partial", "name": "notes", "metadata": map[string]any{}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	api.Handle("POST /v1/memory_stores", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	inputs := providertest.Props(map[string]any{"name": "notes", "description": "accepted"})
	created, e := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.Error(t, e)
	require.Nil(t, created.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	obj["description"] = "accepted"
	api.Put("/v1/memory_stores/memstore_partial", obj)
	read, e := s.Read(p.ReadRequest{Urn: u, ID: "memstore_partial", Inputs: inputs})
	require.NoError(t, e)
	n := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: u, ID: read.ID, State: read.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, e)
	require.Nil(t, updated.PartialState)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, fakeapi.Object{"name": "notes", "description": "accepted", "metadata": map[string]any{}}, api.Last().Body, "Update sends desired mutable values without a GET")
	require.Equal(t, read.Properties.Delete("updatedAt"), updated.Properties.Delete("updatedAt"))
	require.Equal(t, api.Object("/v1/memory_stores/memstore_partial")["updated_at"], updated.Properties.Get("updatedAt").AsString(), "updatedAt is established by the mutation response, not copied from prior state")
}

func TestMemoryStoreManagedEmptyDriftAndRepeatableRead(t *testing.T) {
	s, api := providertest.New(t)
	memoryStoreDefaults(api)
	u := providertest.URN("anthropic:agents:MemoryStore")
	created, inputs := providertest.Create(t, s, u, providertest.Props(map[string]any{"name": "notes", "description": "", "metadata": map[string]any{}}))
	a := providertest.Read(t, s, u, created, inputs)
	b := providertest.Read(t, s, u, created, inputs)
	require.Equal(t, a, b)
	require.Equal(t, inputs, a.Inputs)
	api.Mutate("/v1/memory_stores/"+created.ID, func(obj fakeapi.Object) {
		obj["description"] = "external"
		obj["metadata"] = map[string]any{"external": "value"}
	})
	drift := providertest.Read(t, s, u, created, inputs)
	require.Equal(t, "external", drift.Inputs.Get("description").AsString())
	require.True(t, providertest.Diff(t, s, u, p.CreateResponse{ID: created.ID, Properties: drift.Properties}, drift.Inputs, inputs).HasChanges)
	require.Equal(t, "", inputs.Get("description").AsString(), "caller inputs are immutable")
}

func TestMemoryStoreMalformedReadIsNotAbsence(t *testing.T) {
	for _, body := range []string{
		`null`,
		`{"id":"memstore_1","name":"notes","created_at":"invalid"}`,
		`{"id":"memstore_1","name":"notes","description":{},"metadata":{},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/memory_stores/memstore_1", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			_, e := s.Read(p.ReadRequest{Urn: providertest.URN("anthropic:agents:MemoryStore"), ID: "memstore_1"})
			require.Error(t, e)
		})
	}
}

func TestMemoryStoreCheckConstraintsAndMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:MemoryStore")
	for _, inputs := range []map[string]any{
		{"name": ""}, {"name": strings.Repeat("x", 256)}, {"name": "control\n"},
		{"name": "ok", "description": strings.Repeat("é", 1025)},
		{"name": "ok", "metadata": map[string]any{"": "v"}},
		{"name": "ok", "metadata": map[string]any{"k": strings.Repeat("x", 513)}},
	} {
		c, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(inputs)})
		require.NoError(t, e)
		require.NotEmpty(t, c.Failures)
	}
	pairs := map[string]any{}
	for i := 0; i < 17; i++ {
		pairs[fmt.Sprint(i)] = "v"
	}
	c, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(map[string]any{"name": "ok", "metadata": pairs})})
	require.NoError(t, e)
	require.NotEmpty(t, c.Failures)
	for _, computed := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New("value")
			if computed {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::value"})
			in := providertest.Props(map[string]any{"name": "é合法", "description": v, "metadata": map[string]any{"key": v}})
			c, e := s.Check(p.CheckRequest{Urn: u, Inputs: in})
			require.NoError(t, e)
			require.Empty(t, c.Failures)
			require.Equal(t, in, c.Inputs)
			again, e := s.Check(p.CheckRequest{Urn: u, Inputs: c.Inputs})
			require.NoError(t, e)
			require.Equal(t, c.Inputs, again.Inputs)
			require.Equal(t, v, in.Get("description"))
		}
	}
	whole := property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::whole"})
	unknowns := providertest.Props(map[string]any{"name": whole, "metadata": whole})
	checked, e := s.Check(p.CheckRequest{Urn: u, Inputs: unknowns})
	require.NoError(t, e)
	require.Empty(t, checked.Failures)
	require.Equal(t, unknowns, checked.Inputs)
	boundary, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(map[string]any{"name": strings.Repeat("é", 255), "description": strings.Repeat("é", 1024), "metadata": map[string]any{strings.Repeat("é", 64): strings.Repeat("é", 512)}})})
	require.NoError(t, e)
	require.Empty(t, boundary.Failures)
	require.Empty(t, api.Requests())
}

func TestMemoryStoreFailedCreateRefreshAndReadErrors(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:MemoryStore")
	const id = "memstore_created"
	obj := fakeapi.Object{"id": id, "name": "notes", "description": "", "metadata": map[string]any{}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
	api.Put("/v1/memory_stores/"+id, obj)
	api.Handle("POST /v1/memory_stores", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		copy := fakeapi.Object{}
		for k, v := range obj {
			copy[k] = v
		}
		copy["created_at"] = map[string]any{"invalid": true}
		fakeapi.WriteJSON(w, copy)
	})
	inputs := providertest.Props(map[string]any{"name": "notes"})
	partial, e := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.Empty(t, partial.ID)
	require.NotContains(t, e.Error(), "failed to initialize")
	read, e := s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: inputs, Properties: inputs})
	require.NoError(t, e)
	n := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: u, ID: id, State: read.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, e)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, fakeapi.Object{"name": "notes", "description": "", "metadata": map[string]any{}}, api.Last().Body)
	require.Equal(t, "2026-01-01T00:00:00Z", updated.Properties.Get("createdAt").AsString())
	status := 403
	api.Handle("GET /v1/memory_stores/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"synthetic"}}`))
	})
	for _, code := range []int{403, 400} {
		status = code
		_, e := s.Read(p.ReadRequest{Urn: u, ID: id})
		require.Error(t, e)
	}
}

func TestMemoryStoreConservativeCreateRetry(t *testing.T) {
	s, api := providertest.New(t)
	calls := 0
	api.Handle("POST /v1/memory_stores", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		calls++
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"synthetic processed create"}}`))
	})
	_, e := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:agents:MemoryStore"), Properties: providertest.Props(map[string]any{"name": "notes"})})
	require.Error(t, e)
	require.Equal(t, 1, calls, "generated New must not retry a possibly allocated store")
}

// These response faults are synthetic, not observed platform behavior.
func TestMemoryStoreUpdateIncompleteTimestampsOrdinaryError(t *testing.T) {
	for _, creationField := range []string{"omitted", "malformed"} {
		for _, prior := range []string{"known", "unknown"} {
			for _, updateField := range []string{"valid", "omitted", "malformed"} {
				t.Run(creationField+"/"+prior+"/"+updateField, func(t *testing.T) {
					s, api := providertest.New(t)
					u := providertest.URN("anthropic:agents:MemoryStore")
					createdAt := "2026-01-01T00:00:00Z"
					if prior == "unknown" {
						createdAt = "" // Legacy incomplete state may have an empty timestamp.
					}
					old := providertest.Props(map[string]any{"name": "notes", "description": "old", "metadata": map[string]any{"old": "value"}})
					state := providertest.Props(map[string]any{"name": "notes", "description": "old", "metadata": map[string]any{"old": "value"}, "createdAt": createdAt, "updatedAt": "2026-01-02T00:00:00Z"})
					desired := providertest.Props(map[string]any{"name": "changed", "description": "new", "metadata": map[string]any{"owner": "team"}})
					response := fakeapi.Object{"id": "memstore_partial", "name": "changed", "description": "new", "metadata": map[string]any{"owner": "team"}}
					if creationField == "malformed" {
						response["created_at"] = map[string]any{"synthetic_invalid": true}
					}
					expectedUpdatedAt := ""
					switch updateField {
					case "valid":
						expectedUpdatedAt = "2026-01-03T00:00:00Z"
						response["updated_at"] = expectedUpdatedAt
					case "malformed":
						response["updated_at"] = map[string]any{"synthetic_invalid": true}
					}
					api.Handle("POST /v1/memory_stores/memstore_partial", func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
						require.Equal(t, fakeapi.Object{"name": "changed", "description": "new", "metadata": map[string]any{"owner": "team", "old": nil}}, body)
						fakeapi.WriteJSON(w, response)
					})
					updated, err := s.Update(p.UpdateRequest{Urn: u, ID: "memstore_partial", State: state, OldInputs: old, Inputs: desired})
					require.Error(t, err)
					require.Nil(t, updated.PartialState)
					require.NotContains(t, err.Error(), "failed to initialize")
					require.Zero(t, updated.Properties.Len())
					require.Len(t, api.Requests(), 1, "Update must not perform an internal GET")
					require.Equal(t, http.MethodPost, api.Last().Method)
					require.Equal(t, createdAt, state.Get("createdAt").AsString(), "caller state is immutable")
				})
			}
		}
	}
}
