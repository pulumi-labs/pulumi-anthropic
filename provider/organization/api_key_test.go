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
	"net/http"
	"strings"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const apiKeyPath = "/v1/organizations/api_keys/apikey_1"

func seedApiKey(api *fakeapi.Server) {
	api.Put(apiKeyPath, fakeapi.Object{
		"id": "apikey_1", "type": "api_key", "name": "old", "status": "active",
		"partial_key_hint": "sk-ant-api03-R2D...igAA",
		"scope":            fakeapi.Object{"type": "workspace", "workspace_id": "wrkspc_1"}, "principal": nil, "created_by": nil,
		"created_at": "2026-01-01T00:00:00Z", "expires_at": nil,
	})
}

func TestApiKeyLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedApiKey(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:ApiKey",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "ci"}),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "wrkspc_1", output.Get("workspaceId").AsString())
				req := api.LastRequest(t, http.MethodPost, apiKeyPath)
				assert.Equal(t, fakeapi.Object{"name": "ci"}, req.Body, "undeclared status is left alone")
			},
		},
		Updates: []integration.Operation{{
			// Dropping the name stops managing it; it is not cleared.
			Inputs: providertest.Props(map[string]any{"apiKeyId": "apikey_1", "status": "inactive"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, apiKeyPath)
				assert.Equal(t, fakeapi.Object{"status": "inactive"}, req.Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodPost, last.Method)
	assert.Equal(t, fakeapi.Object{"status": "archived"}, last.Body, "deleting the resource archives the key")
	assert.Equal(t, "ci", api.Object(apiKeyPath)["name"])
}

func TestApiKeyMissing(t *testing.T) {
	t.Parallel()
	server, _ := providertest.New(t)
	_, err := server.Create(p.CreateRequest{Urn: urn("ApiKey"),
		Properties: providertest.Props(map[string]any{"apiKeyId": "apikey_9"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key apikey_9 does not exist")
}

func TestApiKeyRefreshAndImport(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	seedApiKey(api)

	created, checked := providertest.Create(t, server, urn("ApiKey"), providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "old"}))
	resp := providertest.Read(t, server, urn("ApiKey"), created, checked)
	assert.Equal(t, checked, resp.Inputs, "the undeclared status is not drift")

	api.Mutate(apiKeyPath, func(obj fakeapi.Object) { obj["name"] = "renamed" })
	resp = providertest.Read(t, server, urn("ApiKey"), created, checked)
	assert.Equal(t, "renamed", resp.Inputs.Get("name").AsString())

	imported, err := server.Read(p.ReadRequest{ID: "apikey_1", Urn: urn("ApiKey")})
	require.NoError(t, err)
	assert.Equal(t, providertest.Props(map[string]any{
		"apiKeyId": "apikey_1", "name": "renamed", "status": "active"}), imported.Inputs)
}

func TestApiKeyCheckMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:organization:ApiKey")
	for _, v := range []property.Value{property.New("CI"), property.New("CI").WithSecret(true), property.New(property.Computed), property.New(property.Computed).WithSecret(true), property.New("CI").WithDependencies([]resourceurn.URN{u})} {
		in := property.NewMap(map[string]property.Value{"apiKeyId": property.New("apikey_1"), "name": v})
		a, err := s.Check(p.CheckRequest{Urn: u, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, a.Failures)
		require.Equal(t, in, a.Inputs)
		b, err := s.Check(p.CheckRequest{Urn: u, Inputs: a.Inputs})
		require.NoError(t, err)
		require.Equal(t, a.Inputs, b.Inputs)
		preview, err := s.Create(p.CreateRequest{Urn: u, Properties: in, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, v, preview.Properties.Get("name"))
		for _, output := range []string{"partialKeyHint", "workspaceId", "createdAt", "expiresAt", "observedStatus", "scope", "principal", "createdBy"} {
			require.True(t, preview.Properties.Get(output).IsComputed(), "%s is server determined", output)
		}
	}
	for _, in := range []map[string]any{{"apiKeyId": ""}, {"apiKeyId": "k", "name": ""}, {"apiKeyId": "k", "name": strings.Repeat("x", 501)}, {"apiKeyId": "k", "status": "expired"}, {"apiKeyId": "k", "status": "bogus"}} {
		c, err := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(in)})
		require.NoError(t, err)
		require.NotEmpty(t, c.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestApiKeyModernImport(t *testing.T) {
	s, api := providertest.New(t)
	seedApiKey(api)
	api.Mutate(apiKeyPath, func(o fakeapi.Object) {
		o["status"] = "expired"
		o["scope"] = fakeapi.Object{"type": "organization"}
		o["principal"] = fakeapi.Object{"type": "service_account_actor", "service_account_id": "svac_1"}
		o["created_by"] = fakeapi.Object{"type": "user", "id": "user_1"}
	})
	r, err := s.Read(p.ReadRequest{Urn: urn("ApiKey"), ID: "apikey_1"})
	require.NoError(t, err)
	require.Equal(t, "expired", r.Properties.Get("observedStatus").AsString())
	require.True(t, r.Inputs.Get("status").IsNull())
	require.Equal(t, "", r.Properties.Get("workspaceId").AsString())
	require.Equal(t, "organization", r.Properties.Get("scope").AsMap().Get("type").AsString())
	require.Equal(t, "svac_1", r.Properties.Get("principal").AsMap().Get("id").AsString())
	check, err := s.Check(p.CheckRequest{Urn: urn("ApiKey"), Inputs: r.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
}

func TestApiKeyManagedExpiration(t *testing.T) {
	s, api := providertest.New(t)
	seedApiKey(api)
	created, desired := providertest.Create(t, s, urn("ApiKey"), providertest.Props(map[string]any{"apiKeyId": "apikey_1", "status": "active"}))
	api.Mutate(apiKeyPath, func(o fakeapi.Object) { o["status"] = "expired" })
	before := len(api.Requests())
	refreshed := providertest.Read(t, s, urn("ApiKey"), created, desired)
	require.Len(t, api.Requests(), before+1)
	require.Equal(t, http.MethodGet, api.Last().Method)
	require.Equal(t, "apikey_1", refreshed.ID)
	require.True(t, refreshed.Inputs.Get("status").IsNull(), "expired is never a writable input")
	require.True(t, refreshed.Properties.Get("status").IsNull(), "status outputs must conform to the writable enum")
	require.Equal(t, "expired", refreshed.Properties.Get("observedStatus").AsString())
	checked, err := s.Check(p.CheckRequest{Urn: urn("ApiKey"), Inputs: refreshed.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, refreshed.Inputs, checked.Inputs)
	state := p.CreateResponse{ID: refreshed.ID, Properties: refreshed.Properties}
	diff := providertest.Diff(t, s, urn("ApiKey"), state, refreshed.Inputs, desired)
	require.True(t, diff.HasChanges, "configured active status must remain drift against expiration")
	require.Equal(t, p.Add, diff.DetailedDiff["status"].Kind)
	require.False(t, providertest.Diff(t, s, urn("ApiKey"), state, refreshed.Inputs, checked.Inputs).HasChanges)
	require.Len(t, api.Requests(), before+1, "Check and Diff must not mutate an expired key")
}

func TestApiKeyMalformedMutationAndFullUpdate(t *testing.T) {
	s, api := providertest.New(t)
	seedApiKey(api)
	in := providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "CI", "status": "inactive"})
	api.SetHooks("api_keys", fakeapi.Hooks{OnUpdate: func(before, after, body fakeapi.Object) error {
		after["name"] = 123
		after["expires_at"] = 123
		return nil
	}})
	created, err := s.Create(p.CreateRequest{Urn: urn("ApiKey"), Properties: in})
	requireOrdinaryError(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Empty(t, created.Properties.AsMap())
	api.SetHooks("api_keys", fakeapi.Hooks{})
	api.Mutate(apiKeyPath, func(o fakeapi.Object) { o["name"] = "CI"; o["expires_at"] = nil })
	// Refresh a legacy incomplete checkpoint before updating; Update never hydrates it.
	refreshed, err := s.Read(p.ReadRequest{Urn: urn("ApiKey"), ID: "apikey_1", Properties: in, Inputs: in})
	require.NoError(t, err)
	require.Equal(t, "CI", refreshed.Properties.Get("name").AsString())
	before := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: urn("ApiKey"), ID: refreshed.ID, State: refreshed.Properties, Inputs: in, OldInputs: in})
	require.NoError(t, err)
	require.Len(t, api.Requests(), before+1, "Update must not GET after refresh")
	require.Equal(t, fakeapi.Object{"name": "CI", "status": "inactive"}, api.Last().Body)
	require.Equal(t, "CI", updated.Properties.Get("name").AsString())
	desired := providertest.Props(map[string]any{"apiKeyId": "apikey_1"})
	_, err = s.Update(p.UpdateRequest{Urn: urn("ApiKey"), ID: refreshed.ID, State: updated.Properties, Inputs: desired})
	require.NoError(t, err)
	require.Empty(t, api.Last().Body, "omission relinquishes management")
	require.Equal(t, "CI", api.Object(apiKeyPath)["name"])
}

func TestApiKeyUpdateFailures(t *testing.T) {
	for _, fault := range []string{"rejected", "unusable", "foreign"} {
		t.Run(fault, func(t *testing.T) {
			s, api := providertest.New(t)
			seedApiKey(api)
			in := providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "old", "status": "active"})
			created, _ := providertest.Create(t, s, urn("ApiKey"), in)
			desired := in.Set("name", property.New("CI")).Set("status", property.New("inactive"))
			api.Handle("POST "+apiKeyPath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
				require.Equal(t, fakeapi.Object{"name": "CI", "status": "inactive"}, body)
				if fault == "rejected" {
					fakeapi.WriteError(w, http.StatusForbidden, "denied")
					return
				}
				response := api.Object(apiKeyPath)
				if fault == "foreign" {
					response["id"] = "apikey_foreign"
				} else {
					response["name"] = 42
				}
				fakeapi.WriteJSON(w, response)
			})
			before := len(api.Requests())
			out, err := s.Update(p.UpdateRequest{Urn: urn("ApiKey"), ID: created.ID, State: created.Properties, OldInputs: in, Inputs: desired})
			requireOrdinaryError(t, err)
			require.Nil(t, out.PartialState)
			require.Empty(t, out.Properties.AsMap())
			require.Len(t, api.Requests(), before+1, "Update must not GET or reconstruct a baseline")
		})
	}
}

func TestApiKeyReadAndDeleteErrors(t *testing.T) {
	for _, code := range []int{404, 403, 500} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s, api := providertest.New(t)
			seedApiKey(api)
			created, _ := providertest.Create(t, s, urn("ApiKey"), providertest.Props(map[string]any{"apiKeyId": "apikey_1"}))
			api.Handle("GET "+apiKeyPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteError(w, code, "injected")
			})
			r, err := s.Read(p.ReadRequest{Urn: urn("ApiKey"), ID: "apikey_1"})
			if code == 404 {
				require.NoError(t, err)
				require.Empty(t, r.ID)
			} else {
				require.Error(t, err)
			}
			api.Handle("POST "+apiKeyPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				require.Equal(t, fakeapi.Object{"status": "archived"}, b)
				fakeapi.WriteError(w, code, "injected")
			})
			err = s.Delete(p.DeleteRequest{Urn: urn("ApiKey"), ID: "apikey_1", Properties: created.Properties.Set("apiKeyId", property.New("wrong"))})
			if code == 404 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestApiKeyIdentityAndRejectedAdoption(t *testing.T) {
	for _, foreign := range []bool{true, false} {
		t.Run(map[bool]string{true: "foreign", false: "rejected"}[foreign], func(t *testing.T) {
			s, api := providertest.New(t)
			seedApiKey(api)
			if foreign {
				api.Mutate(apiKeyPath, func(o fakeapi.Object) { o["id"] = "apikey_foreign" })
			} else {
				api.Handle("POST "+apiKeyPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteError(w, 403, "rejected") })
			}
			in := providertest.Props(map[string]any{"apiKeyId": "apikey_1", "name": "unaccepted"})
			r, err := s.Create(p.CreateRequest{Urn: urn("ApiKey"), Properties: in})
			requireOrdinaryError(t, err)
			require.Empty(t, r.ID)
			require.Nil(t, r.PartialState)
			require.Empty(t, r.Properties.AsMap())
			if foreign {
				require.Len(t, api.Requests(), 1)
			} else {
				require.Len(t, api.Requests(), 2)
			}
			require.Equal(t, "old", api.Object(apiKeyPath)["name"])
		})
	}
}

func TestApiKeyInvalidObservations(t *testing.T) {
	for _, field := range []string{"status", "name", "created_at", "expires_at", "scope", "principal", "created_by"} {
		t.Run(field, func(t *testing.T) {
			s, api := providertest.New(t)
			seedApiKey(api)
			api.Mutate(apiKeyPath, func(o fakeapi.Object) { o[field] = 123 })
			_, err := s.Read(p.ReadRequest{Urn: urn("ApiKey"), ID: "apikey_1"})
			require.Error(t, err)
		})
	}
}

func TestApiKeyReplacement(t *testing.T) {
	s, api := providertest.New(t)
	seedApiKey(api)
	created, in := providertest.Create(t, s, urn("ApiKey"), providertest.Props(map[string]any{"apiKeyId": "apikey_1"}))
	d := providertest.Diff(t, s, urn("ApiKey"), created, in, in.Set("apiKeyId", property.New("apikey_2")))
	require.Equal(t, p.UpdateReplace, d.DetailedDiff["apiKeyId"].Kind)
	require.False(t, d.DeleteBeforeReplace)
}
