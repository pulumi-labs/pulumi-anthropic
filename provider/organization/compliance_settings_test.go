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
	"sync"
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

const compliancePath = "/v1/organizations/compliance_settings"

// complianceServerBehavior serves the singleton, which accepts the state's short string
// form too but always responds with the object form.
func complianceServerBehavior(api *fakeapi.Server) func(string) {
	var mu sync.Mutex
	state := "disabled"
	respond := func(w http.ResponseWriter) {
		fakeapi.WriteJSON(w, fakeapi.Object{"type": "compliance_settings",
			"state": map[string]any{"type": state}})
	}
	api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		mu.Lock()
		defer mu.Unlock()
		respond(w)
	})
	api.Handle("POST "+compliancePath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		mu.Lock()
		defer mu.Unlock()
		state = body["state"].(map[string]any)["type"].(string)
		respond(w)
	})
	return func(s string) {
		mu.Lock()
		defer mu.Unlock()
		state = s
	}
}

func TestComplianceSettingsLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	complianceServerBehavior(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:ComplianceSettings",
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{"enabled": true}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, compliancePath)
				assert.Equal(t, fakeapi.Object{"state": map[string]any{"type": "enabled"}}, req.Body)
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{"enabled": false}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, compliancePath)
				assert.Equal(t, fakeapi.Object{"state": map[string]any{"type": "disabled"}}, req.Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodPost, last.Method, "deleting the settings sends no request")
}

func TestComplianceSettingsRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	setState := complianceServerBehavior(api)

	created, checked := providertest.Create(t, server, urn("ComplianceSettings"), providertest.Props(map[string]any{"enabled": false}))
	assert.Equal(t, "compliance-settings", created.ID)
	resp := providertest.Read(t, server, urn("ComplianceSettings"), created, checked)
	assert.Equal(t, checked, resp.Inputs)

	// Provisioning Access Transparency re-enables the Compliance API behind Pulumi's back.
	setState("enabled")
	resp = providertest.Read(t, server, urn("ComplianceSettings"), created, checked)
	assert.True(t, resp.Inputs.Get("enabled").AsBool())
}

// Contract CS-E1: unknown preview inputs survive typed adapters without I/O.
func TestComplianceSettingsUnknownPreview(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN("anthropic:organization:ComplianceSettings")
	inputs := property.NewMap(map[string]property.Value{"enabled": property.New(property.Computed)})
	checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("enabled").IsComputed())
	created, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, created.Properties.Get("enabled").IsComputed())
	old := providertest.Props(map[string]any{"enabled": false})
	diff, err := server.Diff(p.DiffRequest{Urn: u, ID: "compliance-settings", State: old, OldInputs: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges, "unknown must not collapse to equality with false")
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: "compliance-settings", State: old, OldInputs: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("enabled").IsComputed())
	require.Empty(t, api.Requests())
}

// Contract CS-E2/E3: ID-only import is usable, mutable diffs never replace,
// and forgetting an organization singleton must not disable its API.
func TestComplianceSettingsImportStableUpdateAndForget(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN("anthropic:organization:ComplianceSettings")
	api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"type": "compliance_settings", "state": map[string]any{"type": "enabled"}})
	})
	imported, err := server.Read(p.ReadRequest{Urn: u, ID: "compliance-settings"})
	require.NoError(t, err)
	require.Equal(t, "compliance-settings", imported.ID)
	require.True(t, imported.Inputs.Get("enabled").AsBool())
	checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	created := p.CreateResponse{ID: imported.ID, Properties: imported.Properties}
	require.False(t, providertest.Diff(t, server, u, created, imported.Inputs, checked.Inputs).HasChanges)
	off := providertest.Props(map[string]any{"enabled": false})
	diff := providertest.Diff(t, server, u, created, imported.Inputs, off)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.Update, diff.DetailedDiff["enabled"].Kind)
	require.False(t, diff.DeleteBeforeReplace)
	api.Handle("POST "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"state": map[string]any{"type": "disabled"}})
	})
	for range 2 {
		updated, err := server.Update(p.UpdateRequest{Urn: u, ID: imported.ID, State: imported.Properties, Inputs: off, OldInputs: imported.Inputs})
		require.NoError(t, err)
		require.False(t, updated.Properties.Get("enabled").AsBool())
		require.Equal(t, fakeapi.Object{"state": map[string]any{"type": "disabled"}}, api.LastRequest(t, http.MethodPost, compliancePath).Body)
		created.Properties = updated.Properties
		require.False(t, providertest.Diff(t, server, u, created, off, off).HasChanges)
	}
	count := len(api.Requests())
	for range 2 {
		err := server.Delete(p.DeleteRequest{Urn: u, ID: imported.ID, Properties: created.Properties})
		require.NoError(t, err)
	}
	require.Len(t, api.Requests(), count)
}

// Synthetic canonical responses exercise mapping, not platform inheritance behavior.
func TestComplianceSettingsUsesWriteResponse(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN("anthropic:organization:ComplianceSettings")
	api.Handle("POST "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"state": map[string]any{"type": "enabled"}})
	})
	created, _ := providertest.Create(t, server, u, providertest.Props(map[string]any{"enabled": false}))
	require.True(t, created.Properties.Get("enabled").AsBool())
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: created.Properties, Inputs: created.Properties})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("enabled").AsBool())
	require.Len(t, api.Requests(), 2, "no readback")
}

// Missing and future response variants cannot establish a boolean.
func TestComplianceSettingsInvalidRead(t *testing.T) {
	for _, state := range []any{map[string]any{}, map[string]any{"type": "future"}, nil} {
		t.Run("invalid-state", func(t *testing.T) {
			server, api := providertest.New(t)
			api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteJSON(w, fakeapi.Object{"state": state})
			})
			got, err := server.Read(p.ReadRequest{Urn: providertest.URN("anthropic:organization:ComplianceSettings"), ID: "compliance-settings"})
			require.Error(t, err)
			require.Empty(t, got.ID)
		})
	}
}

func TestComplianceSettingsUnusableCreateAndRefreshedUpdate(t *testing.T) {
	s, api := providertest.New(t)
	u := urnForCompliance()
	n := 0
	api.Handle("POST "+compliancePath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		n++
		require.Equal(t, fakeapi.Object{"state": map[string]any{"type": "enabled"}}, b)
		if n == 1 {
			fakeapi.WriteJSON(w, fakeapi.Object{"state": nil})
			return
		}
		fakeapi.WriteJSON(w, fakeapi.Object{"state": map[string]any{"type": "enabled"}})
	})
	in := providertest.Props(map[string]any{"enabled": true})
	created, err := s.Create(p.CreateRequest{Urn: u, Properties: in})
	requireOrdinaryError(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Empty(t, created.Properties.AsMap())
	api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"state": map[string]any{"type": "enabled"}})
	})
	refreshed, err := s.Read(p.ReadRequest{Urn: u, ID: "compliance-settings"})
	require.NoError(t, err)
	before := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: u, ID: refreshed.ID, State: refreshed.Properties, OldInputs: in, Inputs: in})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.True(t, updated.Properties.Get("enabled").AsBool())
	require.Len(t, api.Requests(), before+1, "Update never refreshes internally")
}

func TestComplianceSettingsUpdateFailures(t *testing.T) {
	for _, fault := range []string{"rejected", "unusable"} {
		t.Run(fault, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteJSON(w, fakeapi.Object{"state": map[string]any{"type": "enabled"}})
			})
			in := providertest.Props(map[string]any{"enabled": true})
			created, err := s.Read(p.ReadRequest{Urn: urnForCompliance(), ID: "compliance-settings"})
			require.NoError(t, err)
			api.Handle("POST "+compliancePath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
				require.Equal(t, fakeapi.Object{"state": map[string]any{"type": "disabled"}}, body)
				if fault == "rejected" {
					fakeapi.WriteError(w, http.StatusForbidden, "denied")
					return
				}
				fakeapi.WriteJSON(w, fakeapi.Object{"state": nil})
			})
			before := len(api.Requests())
			out, err := s.Update(p.UpdateRequest{Urn: urnForCompliance(), ID: created.ID, State: created.Properties, OldInputs: in, Inputs: providertest.Props(map[string]any{"enabled": false})})
			requireOrdinaryError(t, err)
			require.Nil(t, out.PartialState)
			require.Empty(t, out.Properties.AsMap(), "do not invent false from an invalid response")
			require.Len(t, api.Requests(), before+1)
		})
	}
}

func urnForCompliance() resourceurn.URN {
	return providertest.URN("anthropic:organization:ComplianceSettings")
}

func TestComplianceSettingsCheckMetadata(t *testing.T) {
	s, api := providertest.New(t)
	for _, v := range []property.Value{property.New(true), property.New(false), property.New(property.Computed)} {
		for _, secret := range []bool{false, true} {
			v := v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::test:index:Thing::dep"})
			in := property.NewMap(map[string]property.Value{"enabled": v})
			checked, err := s.Check(p.CheckRequest{Urn: urnForCompliance(), Inputs: in})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, in, checked.Inputs)
			again, err := s.Check(p.CheckRequest{Urn: urnForCompliance(), Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Equal(t, checked.Inputs, again.Inputs)
			require.Equal(t, v, in.Get("enabled"))
		}
	}
	for _, in := range []property.Map{property.NewMap(nil), providertest.Props(map[string]any{"enabled": "yes"}), property.NewMap(map[string]property.Value{"enabled": property.Value{}})} {
		got, err := s.Check(p.CheckRequest{Urn: urnForCompliance(), Inputs: in})
		require.NoError(t, err)
		require.NotEmpty(t, got.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestComplianceSettingsReadErrors(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET "+compliancePath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				w.WriteHeader(status)
				fakeapi.WriteJSON(w, fakeapi.Object{"type": "error", "error": map[string]any{"type": "api_error", "message": "synthetic"}})
			})
			_, err := s.Read(p.ReadRequest{Urn: urnForCompliance(), ID: "compliance-settings"})
			require.Error(t, err)
		})
	}
}
