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
	}, OnUpdate: func(before, after, body fakeapi.Object) error {
		// The endpoint replaces JWKS as a whole, unlike the generic fake merge.
		if j, ok := body["jwks"]; ok {
			after["jwks"] = j
		}
		after["poll_status"] = map[string]any{"consecutive_failures": 0.0, "last_fetched_at": "2026-09-25T00:00:00Z"}
		return nil
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
					"name": "github-actions", "issuer_url": "https://token.actions.githubusercontent.com",
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

func TestFederationIssuerValidationAndMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(federationIssuerToken)
	base := providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example/"})
	cases := []struct {
		name    string
		inputs  property.Map
		invalid bool
	}{
		{"slug", base.Set("name", property.New("Invalid")), true},
		{"lifetime", base.Set("maxJwtLifetimeSeconds", property.New(0.0)), true},
		{"fraction", base.Set("maxJwtLifetimeSeconds", property.New(1.5)), true},
		{"too-long", base.Set("maxJwtLifetimeSeconds", property.New(176401.0)), true},
		{"discovery-private", base.Set("issuerUrl", property.New("https://127.0.0.1")), true},
		{"discovery-http", base.Set("issuerUrl", property.New("http://issuer.example")), true},
		{"explicit-missing", base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "explicit_url"}))), true},
		{"mixed", base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "discovery", "url": "https://keys.example"}))), true},
		{"inline-empty", base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "inline", "keys": []any{}}))), true},
		{"private-key", base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "inline", "keys": []any{map[string]any{"kty": "RSA", "n": "abc", "e": "AQAB", "d": "never-private"}}}))), true},
		{"explicit-exact-iss", base.Set("issuerUrl", property.New("urn:issuer:exact/")).Set("jwks", property.New(providertest.Props(map[string]any{"type": "explicit_url", "url": "https://keys.example"}))), false},
		{"discovery-override", base.Set("issuerUrl", property.New("http://private-issuer")).Set("jwks", property.New(providertest.Props(map[string]any{"type": "discovery", "discoveryBase": "https://keys.example"}))), false},
		{"whole-jwks-unknown", base.Set("issuerUrl", property.New("urn:exact")).Set("jwks", property.New(property.Computed)), false},
		{"nested-unknown", base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "explicit_url", "url": property.New(property.Computed)}))), false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			check, err := s.Check(p.CheckRequest{Urn: u, Inputs: tt.inputs})
			require.NoError(t, err)
			if tt.invalid {
				require.NotEmpty(t, check.Failures)
			} else {
				require.Empty(t, check.Failures)
				require.Equal(t, tt.inputs, check.Inputs)
			}
		})
	}
	dep := resourceurn.URN("urn:pulumi:stack::project::test:index:Dependency::dependency")
	unknown := property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{dep})
	for _, key := range []string{"issuerUrl", "jwks", "checkJti", "maxJwtLifetimeSeconds"} {
		in := base.Set(key, unknown)
		before := in
		check, err := s.Check(p.CheckRequest{Urn: u, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, check.Failures)
		require.Equal(t, in, check.Inputs)
		again, err := s.Check(p.CheckRequest{Urn: u, Inputs: check.Inputs})
		require.NoError(t, err)
		require.Equal(t, check.Inputs, again.Inputs)
		preview, err := s.Create(p.CreateRequest{Urn: u, Properties: check.Inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, unknown, preview.Properties.Get(key))
		state := base.Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z")).Set("checkJti", property.New(false))
		d, err := s.Diff(p.DiffRequest{Urn: u, ID: "fdis_ci", State: state, Inputs: in})
		require.NoError(t, err)
		require.True(t, d.HasChanges)
		update, err := s.Update(p.UpdateRequest{Urn: u, ID: "fdis_ci", State: state, Inputs: in, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, unknown, update.Properties.Get(key))
		require.Equal(t, before, in)
	}
	nested := base.Set("jwks", property.New(providertest.Props(map[string]any{"type": "inline", "keys": []any{map[string]any{"kty": "RSA", "n": unknown, "e": "AQAB"}}})))
	check, err := s.Check(p.CheckRequest{Urn: u, Inputs: nested})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	require.Equal(t, nested, check.Inputs)
	preview, err := s.Create(p.CreateRequest{Urn: u, Properties: check.Inputs, DryRun: true})
	require.NoError(t, err)
	// Preview conservatively marks the enclosing value unknown and secret when
	// a nested key is unresolved. Check still preserves the precise input shape.
	requireOutputValue(t, property.New(property.Computed).WithSecret(true), preview.Properties.Get("jwks"))
	state := base.Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
	updated, err := s.Update(p.UpdateRequest{Urn: u, ID: "fdis_ci", State: state, OldInputs: base, Inputs: check.Inputs, DryRun: true})
	require.NoError(t, err)
	requireOutputValue(t, property.New(property.Computed).WithSecret(true), updated.Properties.Get("jwks"))
	require.Equal(t, nested, check.Inputs, "nested metadata is preserved without moving values")
	require.Empty(t, api.Requests(), "Check and previews must never perform I/O")
}

func TestFederationIssuerManagedRemovalAndImport(t *testing.T) {
	s, api := providertest.New(t)
	federationIssuerServerBehavior(api)
	u := providertest.URN(federationIssuerToken)
	in := providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example/", "checkJti": false, "maxJwtLifetimeSeconds": 600, "jwks": map[string]any{"type": "discovery", "caCertPem": "synthetic-ca", "discoveryBase": "https://discovery.example"}})
	c, checked := providertest.Create(t, s, u, in)
	next := in.Set("jwks", property.New(providertest.Props(map[string]any{"type": "explicit_url", "url": "https://keys.example/jwks"}))).Delete("maxJwtLifetimeSeconds")
	before := c.Properties
	update, err := s.Update(p.UpdateRequest{Urn: u, ID: c.ID, State: c.Properties, Inputs: next, OldInputs: checked})
	require.NoError(t, err)
	require.Equal(t, before, c.Properties)
	require.Equal(t, fakeapi.Object{"name": "ci", "issuer_url": "https://issuer.example/", "check_jti": false, "max_jwt_lifetime_seconds": 3600.0, "jwks": map[string]any{"type": "explicit_url", "url": "https://keys.example/jwks"}}, api.Last().Body)
	removed := next.Delete("checkJti").Delete("jwks")
	_, err = s.Update(p.UpdateRequest{Urn: u, ID: c.ID, State: update.Properties, Inputs: removed, OldInputs: next})
	require.NoError(t, err)
	require.Equal(t, fakeapi.Object{"name": "ci", "issuer_url": "https://issuer.example/", "check_jti": true, "jwks": map[string]any{"type": "discovery"}}, api.Last().Body)
	imported, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID})
	require.NoError(t, err)
	require.True(t, imported.Inputs.Get("checkJti").AsBool())
	require.Equal(t, 3600.0, imported.Inputs.Get("maxJwtLifetimeSeconds").AsNumber())
	check, err := s.Check(p.CheckRequest{Urn: u, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	diff, err := s.Diff(p.DiffRequest{Urn: u, ID: c.ID, State: imported.Properties, OldInputs: imported.Inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	api.Mutate("/v1/organizations/federation_issuers/"+c.ID, func(obj fakeapi.Object) {
		obj["check_jti"] = false
		obj["max_jwt_lifetime_seconds"] = 0
		obj["jwks_polling_disabled_at"] = "2026-01-01T00:00:00Z"
	})
	// A malformed value is an error rather than disappearing or being replaced by a stale declaration.
	_, err = s.Read(p.ReadRequest{Urn: u, ID: c.ID, Properties: imported.Properties, Inputs: imported.Inputs})
	require.Error(t, err)
	api.Mutate("/v1/organizations/federation_issuers/"+c.ID, func(obj fakeapi.Object) { obj["max_jwt_lifetime_seconds"] = 400 })
	read, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID, Properties: imported.Properties, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.False(t, read.Inputs.Get("checkJti").AsBool())
	require.Equal(t, 400.0, read.Inputs.Get("maxJwtLifetimeSeconds").AsNumber())
	require.Equal(t, "2026-01-01T00:00:00Z", read.Properties.Get("jwksPollingDisabledAt").AsString())
	for _, r := range api.Requests() {
		require.NotContains(t, r.Path, "beta=true")
	}
}

func TestFederationIssuerReadAndArchiveErrors(t *testing.T) {
	s, api := providertest.New(t)
	federationIssuerServerBehavior(api)
	u := providertest.URN(federationIssuerToken)
	c, in := providertest.Create(t, s, u, providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example"}))
	api.Handle("POST /v1/organizations/federation_issuers/{id}/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 400, "live rule references issuer")
	})
	require.ErrorContains(t, s.Delete(p.DeleteRequest{Urn: u, ID: c.ID, Properties: c.Properties}), "live rule")
	require.Equal(t, c.ID, providertest.Read(t, s, u, c, in).ID)
	api.Mutate("/v1/organizations/federation_issuers/"+c.ID, func(obj fakeapi.Object) { obj["archived_at"] = "2026-01-02T00:00:00Z" })
	require.Empty(t, providertest.Read(t, s, u, c, in).ID, "an archived issuer is no longer managed")
	code := 403
	api.Handle("GET /v1/organizations/federation_issuers/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, code, "synthetic failure")
	})
	for _, status := range []int{403, 500, 404} {
		code = status
		r, err := s.Read(p.ReadRequest{Urn: u, ID: c.ID})
		if code == 404 {
			require.NoError(t, err)
			require.Empty(t, r.ID)
		} else {
			require.Error(t, err)
		}
	}
}

// A malformed successful response returns an ordinary error, even if allocation
// succeeded. Fault injection does not characterize platform behavior.
func TestFederationIssuerMalformedCreateAndLegacyRefresh(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/organizations/federation_issuers", func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		obj := fakeapi.Object{"id": "fdis_partial", "name": body["name"], "issuer_url": body["issuer_url"],
			"created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
			"poll_status": map[string]any{"consecutive_failures": "malformed"}}
		api.Put("/v1/organizations/federation_issuers/fdis_partial", obj)
		fakeapi.WriteJSON(w, obj)
	})
	resp, err := s.Create(p.CreateRequest{Urn: providertest.URN(federationIssuerToken), Properties: providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example"})})
	require.Error(t, err)
	require.NotNil(t, api.Object("/v1/organizations/federation_issuers/fdis_partial"), "remote object exists")
	require.Empty(t, resp.ID)
	require.Nil(t, resp.PartialState)
	require.Zero(t, resp.Properties.Len())
	// A legacy incomplete checkpoint can be refreshed explicitly before Update.
	api.Mutate("/v1/organizations/federation_issuers/fdis_partial", func(o fakeapi.Object) { o["poll_status"] = nil })
	inputs := providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example"})
	legacy := providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example", "pollStatus": map[string]any{"lastFetchedAt": "2026-01-01T00:00:00Z"}})
	read, err := s.Read(p.ReadRequest{Urn: providertest.URN(federationIssuerToken), ID: "fdis_partial", Properties: legacy, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "2026-01-01T00:00:00Z", read.Properties.Get("createdAt").AsString())
	federationIssuerServerBehavior(api)
	updated, err := s.Update(p.UpdateRequest{Urn: providertest.URN(federationIssuerToken), ID: read.ID, State: read.Properties, Inputs: inputs})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Equal(t, "POST", api.Last().Method)
	require.Equal(t, fakeapi.Object{"name": "ci", "issuer_url": "https://issuer.example"}, api.Last().Body)
}

// Deliberately malformed responses test provider knowledge, not platform behavior.
func TestFederationIssuerResponseKnowledge(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(fakeapi.Object)
	}{
		{"missing-created", func(o fakeapi.Object) { delete(o, "created_at") }},
		{"bad-updated", func(o fakeapi.Object) { o["updated_at"] = 123 }},
		{"bad-poll", func(o fakeapi.Object) {
			o["poll_status"] = map[string]any{"consecutive_failures": "bad", "last_fetched_at": "2026-01-01T00:00:00Z"}
		}},
		{"bad-id", func(o fakeapi.Object) { o["id"] = 123 }},
		{"bad-type", func(o fakeapi.Object) { o["type"] = "federation_rule" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("POST /v1/organizations/federation_issuers", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				o := fakeapi.Object{"id": "fdis_partial", "type": "federation_issuer", "name": "ci", "issuer_url": "https://issuer.example", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "poll_status": nil}
				tt.edit(o)
				fakeapi.WriteJSON(w, o)
			})
			in := providertest.Props(map[string]any{"name": "ci", "issuerUrl": property.New("https://issuer.example").WithSecret(true)})
			c, err := s.Create(p.CreateRequest{Urn: providertest.URN(federationIssuerToken), Properties: in})
			require.Error(t, err)
			require.Empty(t, c.ID)
			require.Nil(t, c.PartialState)
			require.Zero(t, c.Properties.Len())
			require.Len(t, api.Requests(), 1)
		})
	}
}

func TestFederationIssuerSuccessfulResponseMapping(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(federationIssuerToken)
	obj := fakeapi.Object{"id": "fdis_mapped", "type": "federation_issuer", "name": "ci", "issuer_url": "https://issuer.example", "check_jti": false, "max_jwt_lifetime_seconds": 600, "jwks": map[string]any{"type": "discovery"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "poll_status": map[string]any{"consecutive_failures": 0, "last_fetched_at": "2026-01-01T00:00:00Z"}}
	api.Handle("POST /v1/organizations/federation_issuers", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	in := providertest.Props(map[string]any{"name": "ci", "issuerUrl": property.New("https://issuer.example").WithSecret(true), "checkJti": false})
	created, err := s.Create(p.CreateRequest{Urn: u, Properties: in})
	require.NoError(t, err)
	require.Equal(t, "fdis_mapped", created.ID)
	require.True(t, created.Properties.Get("issuerUrl").Secret())
	require.False(t, created.Properties.Get("checkJti").AsBool())
	poll := created.Properties.Get("pollStatus").AsMap()
	require.Equal(t, float64(0), poll.Get("consecutiveFailures").AsNumber())
	require.Equal(t, "2026-01-01T00:00:00Z", poll.Get("lastFetchedAt").AsString())
	api.Put("/v1/organizations/federation_issuers/fdis_mapped", obj)
	api.Mutate("/v1/organizations/federation_issuers/fdis_mapped", func(o fakeapi.Object) { o["check_jti"] = true })
	read, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: in, Properties: created.Properties})
	require.NoError(t, err)
	require.True(t, read.Inputs.Get("checkJti").AsBool(), "managed false values must expose drift")
	require.Equal(t, float64(0), read.Properties.Get("pollStatus").AsMap().Get("consecutiveFailures").AsNumber())
}

func TestFederationIssuerForeignReadAndUpdate(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(federationIssuerToken)
	foreign := fakeapi.Object{"id": "fdis_foreign", "type": "federation_issuer", "name": "foreign", "issuer_url": "https://foreign.example", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": "2026-01-01T00:00:00Z"}
	api.Handle("GET /v1/organizations/federation_issuers/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, foreign) })
	_, err := s.Read(p.ReadRequest{Urn: u, ID: "fdis_expected"})
	require.Error(t, err, "foreign archived identity is not evidence of absence")
	api.Handle("POST /v1/organizations/federation_issuers/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, foreign) })
	state := providertest.Props(map[string]any{"name": "ci", "issuerUrl": "https://issuer.example", "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	out, err := s.Update(p.UpdateRequest{Urn: u, ID: "fdis_expected", State: state, Inputs: state.Delete("createdAt").Delete("updatedAt")})
	require.Error(t, err)
	require.Nil(t, out.PartialState)
	require.Zero(t, out.Properties.Len())
	require.Len(t, api.Requests(), 2, "Update must not perform an internal Read")
}
