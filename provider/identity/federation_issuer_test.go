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

	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const federationIssuerToken = "anthropic:identity:FederationIssuer"

// federationIssuerServerBehavior mimics the API's defaults and key polling status.
func federationIssuerServerBehavior(api *fakeapi.Server) {
	api.SetHooks("federation_issuers", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		defaults := fakeapi.Object{
			"check_jti":                true,
			"max_jwt_lifetime_seconds": 3600.0,
			"jwks":                     map[string]any{"type": "discovery", "ca_cert_pem": nil, "discovery_base": nil},
		}
		for k, v := range defaults {
			if obj[k] == nil {
				obj[k] = v
			}
		}
		obj["poll_status"] = map[string]any{"consecutive_failures": 0.0, "last_fetched_at": "2026-09-25T00:00:00Z"}
	}})
}

func TestFederationIssuerLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	federationIssuerServerBehavior(api)

	integration.LifeCycleTest{
		Resource: federationIssuerToken,
		Create: integration.Operation{
			Inputs: providertest.Props(map[string]any{
				"name": "github-actions", "issuerUrl": "https://token.actions.githubusercontent.com",
			}),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/federation_issuers")
				assert.Equal(t, fakeapi.Object{
					"name": "github-actions", "issuer_url": "https://token.actions.githubusercontent.com",
				}, req.Body)
				assert.Equal(t, 0.0, output.Get("pollStatus").AsMap().Get("consecutiveFailures").AsNumber())
				_, hasJwks := output.GetOk("jwks")
				assert.False(t, hasJwks, "the server's default key source does not leak into the inputs")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(map[string]any{
				"name": "github-actions", "issuerUrl": "https://token.actions.githubusercontent.com",
				"maxJwtLifetimeSeconds": 600,
				"jwks": map[string]any{"type": "inline", "keys": []any{
					map[string]any{"kty": "RSA", "kid": "k1", "n": "abc", "e": "AQAB"},
				}},
			}),
			Hook: func(_, _ property.Map) {
				reqs := api.Requests()
				assert.Equal(t, fakeapi.Object{
					"max_jwt_lifetime_seconds": 600.0,
					"jwks": map[string]any{"type": "inline", "keys": []any{
						map[string]any{"kty": "RSA", "kid": "k1", "n": "abc", "e": "AQAB"},
					}},
				}, reqs[len(reqs)-1].Body, "JWK member names are data and keep their spelling")
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Regexp(t, `^/v1/organizations/federation_issuers/federation_issuer_\d+/archive$`, last.Path)
}

func TestFederationIssuerRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	federationIssuerServerBehavior(api)

	created, checked := providertest.Create(t, server, providertest.URN(federationIssuerToken), providertest.Props(map[string]any{
		"name": "github-actions", "issuerUrl": "https://token.actions.githubusercontent.com",
		"jwks": map[string]any{"type": "discovery"},
	}))
	assert.Equal(t, checked, providertest.Read(t, server, providertest.URN(federationIssuerToken), created, checked).Inputs,
		"server defaults, including nulls in the key source, are not drift")

	api.Mutate("/v1/organizations/federation_issuers/"+created.ID, func(obj fakeapi.Object) {
		obj["issuer_url"] = "https://elsewhere.example.com"
	})
	assert.Equal(t, "https://elsewhere.example.com",
		providertest.Read(t, server, providertest.URN(federationIssuerToken), created, checked).Inputs.Get("issuerUrl").AsString())
}
