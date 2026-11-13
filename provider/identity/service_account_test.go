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

package identity_test

import (
	"net/http"
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

const serviceAccountToken = "anthropic:identity:ServiceAccount"

// serviceAccountServerBehavior mimics the API's default role.
func serviceAccountServerBehavior(api *fakeapi.Server) {
	api.SetHooks("service_accounts", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		if obj["organization_role"] == nil {
			obj["organization_role"] = "developer"
		}
	}})
}

func TestServiceAccountLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	serviceAccountServerBehavior(api)

	integration.LifeCycleTest{
		Resource: serviceAccountToken,
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"name": "ci-deployer", "description": "CI"}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/service_accounts")
				assert.Equal(t, fakeapi.Object{"name": "ci-deployer", "description": "CI"}, req.Body)
				assert.Equal(t, "Bearer oauth-test", req.Header.Get("Authorization"),
					"service accounts accept only an OAuth token")
				assert.Empty(t, req.Header.Get("X-Api-Key"))
				assert.Equal(t, "oauth-2025-04-20", req.Header.Get("anthropic-beta"),
					"OAuth authentication requires its beta without a resource-specific beta")
				_, hasRole := output.GetOk("organizationRole")
				assert.False(t, hasRole, "the server's default role does not leak into the inputs")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{
				"name": "ci-deployer", "description": "Deploys from CI", "organizationRole": "admin",
			}),
			Hook: func(_, _ property.Map) {
				reqs := api.Requests()
				assert.Equal(t, fakeapi.Object{"description": "Deploys from CI", "organization_role": "admin"},
					reqs[len(reqs)-1].Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Regexp(t, `^/v1/organizations/service_accounts/service_account_\d+/archive$`, last.Path,
		"deleting a service account archives it")
}

func TestServiceAccountRefreshAndReplace(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	serviceAccountServerBehavior(api)

	inputs := map[string]any{"name": "ci-deployer", "description": "CI"}
	created, checked := providertest.Create(t, server, providertest.URN(serviceAccountToken), providertest.Props(inputs))
	assert.Equal(t, checked, providertest.Read(t, server, providertest.URN(serviceAccountToken), created, checked).Inputs,
		"a server-defaulted role is not drift")

	api.Mutate("/v1/organizations/service_accounts/"+created.ID, func(obj fakeapi.Object) {
		obj["description"] = "Edited in the console."
	})
	assert.Equal(t, "Edited in the console.",
		providertest.Read(t, server, providertest.URN(serviceAccountToken), created, checked).Inputs.Get("description").AsString())

	inputs["name"] = "ci-publisher"
	d := providertest.Diff(t, server, providertest.URN(serviceAccountToken), created, checked, providertest.Props(inputs))
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["name"].Kind, "renaming replaces the service account")
}

func TestServiceAccountRequiresOAuth(t *testing.T) {
	t.Parallel()
	server, _ := providertest.NewWithConfig(t, map[string]property.Value{
		"adminKey": property.New("sk-ant-admin-test").WithSecret(true),
	})
	_, err := server.Create(p.CreateRequest{Urn: providertest.URN(serviceAccountToken),
		Properties: providertest.Props(map[string]any{"name": "ci-deployer"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic:authToken")
}

func TestServiceAccountCheckMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(serviceAccountToken)
	for _, key := range []string{"name", "description", "organizationRole"} {
		for _, known := range []bool{true, false} {
			value := "ci"
			if key == "organizationRole" {
				value = "developer"
			}
			v := property.New(value)
			if !known {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:stack::project::test:index:Thing::dep"})
			inputs := providertest.Props(map[string]any{"name": "ci"}).Set(key, v)
			before := inputs
			c, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, c.Failures)
			require.Equal(t, inputs, c.Inputs)
			require.Equal(t, before, inputs)
			again, err := s.Check(p.CheckRequest{Urn: u, Inputs: c.Inputs})
			require.NoError(t, err)
			require.Equal(t, c.Inputs, again.Inputs)
			preview, err := s.Create(p.CreateRequest{Urn: u, Properties: c.Inputs, DryRun: true})
			require.NoError(t, err)
			requireOutputValue(t, v, preview.Properties.Get(key))
		}
	}
	for key, value := range map[string]string{"name": "INVALID", "organizationRole": "owner", "description": string(make([]byte, 2001))} {
		inputs := providertest.Props(map[string]any{"name": "ci"}).Set(key, property.New(value))
		c, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
		require.NoError(t, err)
		require.NotEmpty(t, c.Failures)
	}
	old := providertest.Props(map[string]any{"name": "ci", "description": "", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	d, err := s.Diff(p.DiffRequest{Urn: u, ID: "svac_a", State: old, OldInputs: providertest.Props(map[string]any{"name": "ci", "description": ""}), Inputs: providertest.Props(map[string]any{"name": "ci"}).Set("description", property.New(property.Computed))})
	require.NoError(t, err)
	require.True(t, d.HasChanges)
	require.Empty(t, api.Requests())
}

func TestServiceAccountFullUpdateRemovalAndImport(t *testing.T) {
	s, api := providertest.New(t)
	serviceAccountServerBehavior(api)
	u := providertest.URN(serviceAccountToken)
	api.SetHooks("service_accounts", fakeapi.Hooks{OnUpdate: func(_, after, body fakeapi.Object) error {
		if v, ok := body["description"]; ok && v == nil {
			after["description"] = ""
		}
		return nil
	}})
	inputs := providertest.Props(map[string]any{"name": "ci", "description": "CI", "organizationRole": "admin"})
	c, _ := providertest.Create(t, s, u, inputs)
	n := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: u, ID: c.ID, State: c.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, fakeapi.Object{"description": "CI", "organization_role": "admin"}, api.Last().Body)
	next := providertest.Props(map[string]any{"name": "ci"})
	updated, err = s.Update(p.UpdateRequest{Urn: u, ID: c.ID, State: updated.Properties, Inputs: next})
	require.NoError(t, err)
	require.Equal(t, fakeapi.Object{"description": nil}, api.Last().Body)
	r, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID, Properties: updated.Properties, Inputs: next})
	require.NoError(t, err)
	require.Equal(t, next, r.Inputs)
	imported, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID})
	require.NoError(t, err)
	require.Equal(t, "admin", imported.Inputs.Get("organizationRole").AsString())
	require.Equal(t, "", imported.Inputs.Get("description").AsString())
	next = next.Set("organizationRole", property.New("developer"))
	_, err = s.Update(p.UpdateRequest{Urn: u, ID: c.ID, State: updated.Properties, Inputs: next})
	require.NoError(t, err)
	require.Equal(t, fakeapi.Object{"organization_role": "developer"}, api.Last().Body)
}

func TestServiceAccountMalformedCreateAndLegacyRefresh(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(serviceAccountToken)
	api.Handle("POST /v1/organizations/service_accounts", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		obj := fakeapi.Object{"id": "svac_partial", "name": "ci", "description": "CI", "organization_role": "developer", "created_at": "2026-01-01T00:00:00Z", "updated_at": map[string]any{"bad": true}}
		api.Put("/v1/organizations/service_accounts/svac_partial", obj)
		fakeapi.WriteJSON(w, obj)
	})
	inputs := providertest.Props(map[string]any{"name": "ci", "description": "CI"})
	c, err := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.Error(t, err)
	require.Empty(t, c.ID)
	require.Nil(t, c.PartialState)
	require.Zero(t, c.Properties.Len())
	api.Mutate("/v1/organizations/service_accounts/svac_partial", func(o fakeapi.Object) { o["updated_at"] = "2026-01-01T00:00:00Z" })
	refreshed, err := s.Read(p.ReadRequest{Urn: u, ID: "svac_partial", Properties: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "CI", refreshed.Properties.Get("description").AsString())
	require.Equal(t, "2026-01-01T00:00:00Z", refreshed.Properties.Get("createdAt").AsString())
	api.Handle("POST /v1/organizations/service_accounts/svac_partial", func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"description": "CI"}, body)
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "svac_partial", "name": "ci", "description": "CI", "organization_role": "developer", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"})
	})
	updated, err := s.Update(p.UpdateRequest{Urn: u, ID: refreshed.ID, State: refreshed.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "2026-01-01T00:00:00Z", updated.Properties.Get("updatedAt").AsString())
	require.Len(t, api.Requests(), 3)
}

func TestServiceAccountReadErrorsAndArchive(t *testing.T) {
	for _, code := range []int{403, 404, 429} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/organizations/service_accounts/svac_a", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteError(w, code, "synthetic")
			})
			r, err := s.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountToken), ID: "svac_a"})
			if code == 404 {
				require.NoError(t, err)
				require.Empty(t, r.ID)
			} else {
				require.Error(t, err)
			}
		})
	}
	s, api := providertest.New(t)
	u := providertest.URN(serviceAccountToken)
	serviceAccountServerBehavior(api)
	c, _ := providertest.Create(t, s, u, providertest.Props(map[string]any{"name": "ci"}))
	path := "/v1/organizations/service_accounts/" + c.ID
	api.Handle("POST "+path+"/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 400, "live federation rule")
	})
	require.ErrorContains(t, s.Delete(p.DeleteRequest{Urn: u, ID: c.ID, Properties: c.Properties}), "live federation rule")
	api.Mutate(path, func(obj fakeapi.Object) { obj["archived_at"] = "2026-01-01T00:00:00Z" })
	r, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID, Properties: c.Properties})
	require.NoError(t, err)
	require.Empty(t, r.ID)
}

func TestServiceAccountIDOnlyReadWithoutDescription(t *testing.T) {
	for _, shape := range []string{"absent", "null"} {
		t.Run(shape, func(t *testing.T) {
			server, api := providertest.New(t)
			row := serviceAccountObservation()
			if shape == "absent" {
				delete(row, "description")
			} else {
				row["description"] = nil
			}
			api.Put("/v1/organizations/service_accounts/svac_a", row)
			result, err := server.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountToken), ID: "svac_a"})
			require.NoError(t, err)
			require.Equal(t, "svac_a", result.ID)
			require.True(t, result.Inputs.Get("description").IsNull())
			require.True(t, result.Properties.Get("description").IsNull())
			require.Equal(t, "developer", result.Properties.Get("organizationRole").AsString())
		})
	}
}

func serviceAccountObservation() fakeapi.Object {
	return fakeapi.Object{"id": "svac_a", "type": "service_account", "name": "ci", "description": "CI", "organization_role": "developer", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
}

func TestServiceAccountResponseValidation(t *testing.T) {
	for _, field := range []string{"id", "type", "description", "organization_role", "created_at", "updated_at", "archived_at"} {
		t.Run(field, func(t *testing.T) {
			s, api := providertest.New(t)
			obj := serviceAccountObservation()
			switch field {
			case "id":
				obj[field] = "foreign"
			case "type":
				obj[field] = "user"
			case "organization_role":
				obj[field] = "owner"
			default:
				obj[field] = fakeapi.Object{"invalid": true}
			}
			api.Put("/v1/organizations/service_accounts/svac_a", obj)
			_, err := s.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountToken), ID: "svac_a"})
			require.Error(t, err)
		})
	}
}

func TestServiceAccountAmbiguousCreateNotRetried(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/organizations/service_accounts", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 500, "ambiguous synthetic allocation")
	})
	c, err := s.Create(p.CreateRequest{Urn: providertest.URN(serviceAccountToken), Properties: providertest.Props(map[string]any{"name": "ci"})})
	require.Error(t, err)
	require.Empty(t, c.ID)
	require.Nil(t, c.PartialState)
	require.Len(t, api.Requests(), 1)
}

func TestServiceAccountUpdateIdentitySafety(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(serviceAccountToken)
	old := providertest.Props(map[string]any{"name": "ci", "description": "before", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	next := providertest.Props(map[string]any{"name": "ci", "description": "after"})
	api.Handle("POST /v1/organizations/service_accounts/svac_a", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		obj := serviceAccountObservation()
		obj["id"] = "foreign"
		obj["description"] = "foreign observation"
		fakeapi.WriteJSON(w, obj)
	})
	r, err := s.Update(p.UpdateRequest{Urn: u, ID: "svac_a", State: old, Inputs: next})
	require.Error(t, err)
	require.Nil(t, r.PartialState)
	require.Zero(t, r.Properties.Len())
	require.Equal(t, fakeapi.Object{"description": "after"}, api.Last().Body)
	require.Equal(t, "before", old.Get("description").AsString())
	require.Len(t, api.Requests(), 1)
}
