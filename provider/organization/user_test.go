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

package organization_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	"github.com/pulumi-labs/pulumi-anthropic/provider/organization"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const userPath = "/v1/organizations/users/user_1"

func seedUser(api *fakeapi.Server) {
	api.Put(userPath, fakeapi.Object{
		"id": "user_1", "type": "user", "email": "ada@example.com", "name": "Ada",
		"role": "user", "added_at": "2026-01-01T00:00:00Z",
	})
}

func TestUserLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:User",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"userId": "user_1", "role": "developer"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "ada@example.com", output.Get("email").AsString())
				assert.Equal(t, "user_1", output.Get("userId").AsString())
				req := api.LastRequest(t, http.MethodPost, userPath)
				assert.Equal(t, fakeapi.Object{"role": "developer"}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"userId": "user_1", "role": "billing"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, userPath)
				assert.Equal(t, fakeapi.Object{"role": "billing"}, req.Body)
			},
		}},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, userPath)
	assert.Nil(t, api.Object(userPath), "deleting the resource removes the user from the organization")
}

func TestUserAdoptsWithoutChange(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	created, _ := providertest.Create(t, server, urn("User"), providertest.Props(map[string]any{"userId": "user_1", "role": "user"}))
	assert.Equal(t, "user_1", created.ID)
	for _, req := range api.Requests() {
		assert.NotEqual(t, http.MethodPost, req.Method, "a user already in the declared role is not updated")
	}
}

func TestUserMissing(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)
	_, err := server.Create(p.CreateRequest{Urn: urn("User"),
		Properties: providertest.Props(map[string]any{"userId": "user_9", "role": "user"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "user user_9 does not exist")
	assert.Contains(t, err.Error(), "Invite")
}

func TestUserRefreshAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedUser(api)

	created, checked := providertest.Create(t, server, urn("User"), providertest.Props(map[string]any{"userId": "user_1", "role": "user"}))
	resp := providertest.Read(t, server, urn("User"), created, checked)
	assert.Equal(t, checked, resp.Inputs)

	api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "developer" })
	resp = providertest.Read(t, server, urn("User"), created, checked)
	assert.Equal(t, "developer", resp.Inputs.Get("role").AsString())

	imported, err := server.Read(p.ReadRequest{ID: "user_1", Urn: urn("User")})
	require.NoError(t, err)
	assert.Equal(t, providertest.Props(map[string]any{"userId": "user_1", "role": "developer"}), imported.Inputs)
}

func TestUserCheckMetadataAndPreview(t *testing.T) {
	server, api := providertest.New(t)
	dep := resourceurn.URN("urn:pulumi:stack::project::anthropic:organization:User::dependency")
	for _, unknown := range []bool{false, true} {
		role := property.New("developer")
		if unknown {
			role = property.New(property.Computed)
		}
		inputs := property.NewMap(map[string]property.Value{"userId": property.New("user_1").WithSecret(true).WithDependencies([]resourceurn.URN{dep}), "role": role.WithSecret(true).WithDependencies([]resourceurn.URN{dep})})
		checked, err := server.Check(p.CheckRequest{Urn: urnUser(), Inputs: inputs})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, inputs, checked.Inputs)
		again, err := server.Check(p.CheckRequest{Urn: urnUser(), Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Equal(t, checked, again)
		created, err := server.Create(p.CreateRequest{Urn: urnUser(), Properties: inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, inputs.Get("role"), created.Properties.Get("role"))
		requireOutputValue(t, inputs.Get("userId"), created.Properties.Get("userId"))
		updated, err := server.Update(p.UpdateRequest{Urn: urnUser(), ID: "user_1", State: providertest.Props(map[string]any{"userId": "user_1", "role": "user"}), Inputs: inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, inputs.Get("role"), updated.Properties.Get("role"))
	}
	require.Empty(t, api.Requests())
	for _, input := range []map[string]any{{"userId": "", "role": "developer"}, {"userId": "user_1", "role": "owner"}, {"userId": "user_1", "role": ""}} {
		checked, err := server.Check(p.CheckRequest{Urn: urnUser(), Inputs: providertest.Props(input)})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
	}
}

func urnUser() resourceurn.URN { return providertest.URN("anthropic:organization:User") }

func TestUserAdoptionErrorsAndRefreshedUpdate(t *testing.T) {
	for _, fault := range []string{"malformed", "malformed-json", "foreign", "denied", "disconnect"} {
		t.Run(fault, func(t *testing.T) {
			server, api := providertest.New(t)
			seedUser(api)
			fail := true
			api.Handle("POST "+userPath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
				require.Empty(t, r.URL.RawQuery)
				require.Equal(t, fakeapi.Object{"role": "developer"}, body)
				if fail {
					if fault == "malformed-json" {
						api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "developer" })
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":`))
						return
					}
					if fault == "disconnect" {
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						_ = conn.Close()
						return
					}
					if fault == "denied" {
						fakeapi.WriteError(w, 403, "denied")
						return
					}
					id := "user_1"
					if fault == "foreign" {
						id = "user_foreign"
					}
					fakeapi.WriteJSON(w, fakeapi.Object{"id": id, "type": "user", "role": "developer", "email": 42, "name": "", "added_at": "2026-01-01T00:00:00Z"})
					return
				}
				api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "developer" })
				fakeapi.WriteJSON(w, api.Object(userPath))
			})
			inputs := providertest.Props(map[string]any{"userId": "user_1", "role": "developer"})
			created, err := server.Create(p.CreateRequest{Urn: urnUser(), Properties: inputs})
			requireOrdinaryError(t, err)
			require.Empty(t, created.ID)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.Properties.AsMap())
			require.Len(t, api.Requests(), 2, "adoption reads then assigns the role once")
			fail = false
			refreshed, err := server.Read(p.ReadRequest{Urn: urnUser(), ID: "user_1", Properties: inputs.Delete("role"), Inputs: inputs})
			require.NoError(t, err)
			require.Equal(t, "Ada", refreshed.Properties.Get("name").AsString())
			before := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: urnUser(), ID: refreshed.ID, State: refreshed.Properties, Inputs: inputs})
			require.NoError(t, err)
			require.Nil(t, updated.PartialState)
			require.Equal(t, "developer", updated.Properties.Get("role").AsString())
			require.Equal(t, "ada@example.com", updated.Properties.Get("email").AsString())
			requests := api.Requests()[before:]
			require.Len(t, requests, 1)
			require.Equal(t, "POST", requests[0].Method)
		})
	}
}

func TestUserPrivilegedImportAbsenceAndReplacement(t *testing.T) {
	server, api := providertest.New(t)
	seedUser(api)
	api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "primary_owner" })
	imported, err := server.Read(p.ReadRequest{Urn: urnUser(), ID: "user_1"})
	require.NoError(t, err)
	require.Equal(t, "primary_owner", imported.Inputs.Get("role").AsString())
	require.Equal(t, "primary_owner", imported.Properties.Get("role").AsString())
	inputs := providertest.Props(map[string]any{"userId": "user_2", "role": "user"})
	diff, err := server.Diff(p.DiffRequest{Urn: urnUser(), ID: imported.ID, State: imported.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, p.UpdateReplace, diff.DetailedDiff["userId"].Kind)
	require.False(t, diff.DeleteBeforeReplace)
	status := 0
	api.Handle("GET "+userPath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic failure")
	})
	for _, code := range []int{403, 404, 500} {
		status = code
		result, err := server.Read(p.ReadRequest{Urn: urnUser(), ID: "user_1"})
		if status == 404 {
			require.NoError(t, err)
			require.Empty(t, result.ID)
		} else {
			require.Error(t, err)
		}
	}
	api.Handle("DELETE "+userPath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteError(w, 409, "conflict") })
	require.Error(t, server.Delete(p.DeleteRequest{Urn: urnUser(), ID: "user_1", Properties: imported.Properties}))
}

func TestUserRuntimeMetadataAndNonmutation(t *testing.T) {
	server, api := providertest.New(t)
	seedUser(api)
	dep := resourceurn.URN("urn:pulumi:stack::project::anthropic:organization:User::dependency")
	inputs := property.NewMap(map[string]property.Value{"userId": property.New("user_1").WithSecret(true).WithDependencies([]resourceurn.URN{dep}), "role": property.New("developer").WithSecret(true).WithDependencies([]resourceurn.URN{dep})})
	before := inputs.AsMap()
	created, err := server.Create(p.CreateRequest{Urn: urnUser(), Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, before, inputs.AsMap())
	for _, k := range []string{"userId", "role"} {
		requireOutputValue(t, inputs.Get(k), created.Properties.Get(k))
	}
	prior := created.Properties.AsMap()
	updated, err := server.Update(p.UpdateRequest{Urn: urnUser(), ID: created.ID, State: created.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, before, inputs.AsMap())
	require.Equal(t, prior, created.Properties.AsMap())
	for _, k := range []string{"userId", "role"} {
		requireOutputValue(t, inputs.Get(k), updated.Properties.Get(k))
	}
	read, err := server.Read(p.ReadRequest{Urn: urnUser(), ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, err)
	require.Equal(t, before, inputs.AsMap())
	require.Equal(t, prior, created.Properties.AsMap())
	for _, k := range []string{"userId", "role"} {
		requireOutputValue(t, inputs.Get(k), read.Inputs.Get(k))
		requireOutputValue(t, inputs.Get(k), read.Properties.Get(k))
	}
	diff, err := server.Diff(p.DiffRequest{Urn: urnUser(), ID: created.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
}

func TestUserResponseValidation(t *testing.T) {
	for _, role := range []string{"admin", "billing", "claude_code_user", "developer", "managed", "membership_admin", "owner", "primary_owner", "user"} {
		out, err := organization.UserObservationForTest(json.RawMessage(`{"id":"u","type":"user","role":"`+role+`","email":"e","name":"","added_at":"2026-01-01T02:03:04.123456789Z"}`), "u")
		require.NoError(t, err)
		require.Equal(t, role, out.Role)
		require.NotNil(t, out.Name)
		require.Equal(t, "", *out.Name)
		require.Equal(t, "2026-01-01T02:03:04.123456789Z", *out.AddedAt)
	}
	for _, raw := range []string{
		`{"id":"foreign","type":"user","role":"user","email":"foreign","name":"foreign","added_at":"2026-01-01T00:00:00Z"}`,
		`{"id":"u","type":"user","role":42,"email":42,"name":false,"added_at":"invalid"}`,
		`{"id":"u","type":"wrong"}`,
		`null`, `42`, `{`,
	} {
		out, err := organization.UserObservationForTest(json.RawMessage(raw), "u")
		require.Error(t, err)
		require.Equal(t, "u", out.UserID)
		require.Empty(t, out.Role)
		require.Nil(t, out.Email)
		require.Nil(t, out.Name)
		require.Nil(t, out.AddedAt)
	}
}

// Failure never publishes reconstructed state, even after a successful assignment.
func TestUserUpdateAssignmentFailureBoundaries(t *testing.T) {
	for _, fault := range []string{"malformed-json", "denied", "disconnect"} {
		t.Run(fault, func(t *testing.T) {
			server, api := providertest.New(t)
			seedUser(api)
			created, _ := providertest.Create(t, server, urnUser(), providertest.Props(map[string]any{"userId": "user_1", "role": "user"}))
			fail := true
			api.Handle("POST "+userPath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
				require.Equal(t, fakeapi.Object{"role": "billing"}, body)
				if fail {
					switch fault {
					case "denied":
						fakeapi.WriteError(w, http.StatusForbidden, "denied")
					case "disconnect":
						conn, _, err := w.(http.Hijacker).Hijack()
						require.NoError(t, err)
						_ = conn.Close()
					case "malformed-json":
						api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "billing" })
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"id":`))
					}
					return
				}
				api.Mutate(userPath, func(obj fakeapi.Object) { obj["role"] = "billing" })
				fakeapi.WriteJSON(w, api.Object(userPath))
			})
			inputs := providertest.Props(map[string]any{"userId": "user_1", "role": "billing"})
			before := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: urnUser(), ID: created.ID, State: created.Properties, Inputs: inputs})
			requireOrdinaryError(t, err)
			require.Nil(t, updated.PartialState)
			require.Empty(t, updated.Properties.AsMap())
			if fault == "malformed-json" {
				require.Equal(t, "billing", api.Object(userPath)["role"], "the mutation may still have succeeded")
			} else {
				require.Equal(t, "user", api.Object(userPath)["role"])
			}
			requests := api.Requests()[before:]
			require.Len(t, requests, 1)
			require.Equal(t, "POST", requests[0].Method)
			fail = false
			before = len(api.Requests())
			recovered, err := server.Update(p.UpdateRequest{Urn: urnUser(), ID: created.ID, State: created.Properties, Inputs: inputs})
			require.NoError(t, err)
			require.Nil(t, recovered.PartialState)
			require.Equal(t, "billing", recovered.Properties.Get("role").AsString())
			requests = api.Requests()[before:]
			require.Len(t, requests, 1)
			require.Equal(t, "POST", requests[0].Method)
		})
	}
}
