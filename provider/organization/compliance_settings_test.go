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

	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
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
