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

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
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
				assert.Empty(t, req.Header.Get("anthropic-beta"))
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
