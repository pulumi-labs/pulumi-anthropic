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
	"maps"
	"net/http"
	"sync/atomic"
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

const federationRuleToken = "anthropic:identity:FederationRule"

// federationRuleServerBehavior mimics the API's resolved and defaulted fields. Nested
// objects are rebuilt rather than modified because the fake server retains the request body.
func federationRuleServerBehavior(api *fakeapi.Server) {
	api.SetHooks("federation_rules", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["issuer_name"] = "github-actions"
		obj["workspace_ids"] = []any{obj["workspace_id"]}
		obj["token_lifetime_seconds"] = 3600.0
		target := maps.Clone(obj["target"].(map[string]any))
		target["service_account_name"] = "ci-deployer"
		obj["target"] = target
		match := fakeapi.Object{"audience": nil, "claims": nil, "condition": nil, "subject_prefix": nil}
		maps.Copy(match, obj["match"].(map[string]any))
		obj["match"] = match
	}})
}

func rule(subjectPrefix string) map[string]any {
	return map[string]any{
		"issuerId":    "fdis_1",
		"name":        "deploy-from-main",
		"oauthScope":  "workspace:developer",
		"workspaceId": "wrkspc_1",
		"target":      map[string]any{"serviceAccountId": "svac_1"},
		"match": map[string]any{
			"subjectPrefix": subjectPrefix,
			"claims":        map[string]any{"ref": "refs/heads/main"},
		},
	}
}

func TestFederationRuleLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	federationRuleServerBehavior(api)

	integration.LifeCycleTest{
		Resource: federationRuleToken,
		Create: integration.Operation{
			Inputs: providertest.Props(rule("repo:acme/app:")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, "/v1/organizations/federation_rules")
				assert.Equal(t, fakeapi.Object{
					"issuer_id":    "fdis_1",
					"name":         "deploy-from-main",
					"oauth_scope":  "workspace:developer",
					"workspace_id": "wrkspc_1",
					"target":       map[string]any{"type": "service_account", "service_account_id": "svac_1"},
					"match": map[string]any{
						"subject_prefix": "repo:acme/app:",
						"claims":         map[string]any{"ref": "refs/heads/main"},
					},
				}, req.Body)
				assert.Equal(t, "github-actions", output.Get("issuerName").AsString())
				assert.Equal(t, []property.Value{property.New("wrkspc_1")}, output.Get("workspaceIds").AsArray().AsSlice())
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(rule("repo:acme/app:ref:")),
			Hook: func(_, _ property.Map) {
				reqs := api.Requests()
				assert.Equal(t, fakeapi.Object{"name": "deploy-from-main", "oauth_scope": "workspace:developer", "target": map[string]any{"type": "service_account", "service_account_id": "svac_1"}, "match": map[string]any{
					"subject_prefix": "repo:acme/app:ref:",
					"claims":         map[string]any{"ref": "refs/heads/main"},
				}}, reqs[len(reqs)-1].Body, "full mutable desired state is sent with whole matcher replacement")
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Regexp(t, `^/v1/organizations/federation_rules/federation_rule_\d+/archive$`, last.Path)
}

func TestFederationRuleRefreshAndReplace(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	federationRuleServerBehavior(api)

	created, checked := providertest.Create(t, server, providertest.URN(federationRuleToken), providertest.Props(rule("repo:acme/app:")))
	assert.Equal(t, checked, providertest.Read(t, server, providertest.URN(federationRuleToken), created, checked).Inputs,
		"resolved and defaulted fields are not drift")

	api.Mutate("/v1/organizations/federation_rules/"+created.ID, func(obj fakeapi.Object) {
		obj["oauth_scope"] = "workspace:inference"
	})
	assert.Equal(t, "workspace:inference",
		providertest.Read(t, server, providertest.URN(federationRuleToken), created, checked).Inputs.Get("oauthScope").AsString())

	for field, value := range map[string]any{"issuerId": "fdis_2", "workspaceId": "wrkspc_2"} {
		inputs := rule("repo:acme/app:")
		inputs[field] = value
		d := providertest.Diff(t, server, providertest.URN(federationRuleToken), created, checked, providertest.Props(inputs))
		assert.Equal(t, p.UpdateReplace, d.DetailedDiff[field].Kind, "changing %s replaces the rule", field)
		assert.True(t, d.DeleteBeforeReplace)
	}
}

func TestFederationRuleValidationAndMetadata(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN(federationRuleToken)
	for k, v := range map[string]any{"name": "Bad Name", "tokenLifetimeSeconds": 59, "description": string(make([]byte, 2001)), "match": map[string]any{"audience": "only"}, "target": map[string]any{"serviceAccountId": ""}} {
		in := rule("repo:acme/app:*")
		in[k] = v
		checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: providertest.Props(in)})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures, k)
	}
	dep := []resourceurn.URN{"urn:pulumi:test::test::test:dep:Resource::dependency"}
	for _, field := range []string{"issuerId", "name", "target", "match", "description", "tokenLifetimeSeconds", "appliesToAllWorkspaces", "workspaceId"} {
		in := providertest.Props(rule("repo:acme/app:*")).Set(field, property.New(property.Computed).WithSecret(true).WithDependencies(dep))
		saved := in
		checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, in, checked.Inputs)
		require.Equal(t, saved, in)
		twice, err := server.Check(p.CheckRequest{Urn: urn, Inputs: checked.Inputs})
		require.NoError(t, err)
		require.Equal(t, checked.Inputs, twice.Inputs)
		preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: in, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, in.Get(field), preview.Properties.Get(field))
		previewU, err := server.Update(p.UpdateRequest{Urn: urn, ID: "fdrl_preview", State: providertest.Props(rule("repo:acme/app:*")).Set("issuerName", property.New("github")).Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z")), Inputs: in, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, in.Get(field), previewU.Properties.Get(field))
	}
	require.Empty(t, api.Requests())
}

func TestFederationRuleFullDesiredRemovalAndLegacyRefresh(t *testing.T) {
	server, api := providertest.New(t)
	federationRuleServerBehavior(api)
	urn := providertest.URN(federationRuleToken)
	in := rule("repo:acme/app:*")
	in["description"] = "clear me"
	in["tokenLifetimeSeconds"] = 1200
	in["appliesToAllWorkspaces"] = true
	in["match"] = map[string]any{"subjectPrefix": "repo:acme/app:*", "audience": "aud", "condition": "claims.ref == 'main'", "claims": map[string]any{"owner": "acme", "ref": "main"}}
	created, checked := providertest.Create(t, server, urn, providertest.Props(in))
	path := "/v1/organizations/federation_rules/" + created.ID
	next := rule("repo:acme/app:*")
	next["match"] = map[string]any{"subjectPrefix": "repo:acme/app:*", "claims": map[string]any{"owner": "acme"}}
	desired := providertest.Props(next)
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
	require.NoError(t, err)
	require.Len(t, api.Requests(), before+1, "Update never reads internally")
	require.Equal(t, fakeapi.Object{"name": "deploy-from-main", "oauth_scope": "workspace:developer", "target": map[string]any{"type": "service_account", "service_account_id": "svac_1"}, "match": map[string]any{"subject_prefix": "repo:acme/app:*", "claims": map[string]any{"owner": "acme"}}, "description": nil, "token_lifetime_seconds": float64(3600), "applies_to_all_workspaces": false}, api.Last().Body)
	// Refresh an incomplete legacy checkpoint before applying desired state.
	partial := property.NewMap(map[string]property.Value{"issuerId": checked.Get("issuerId"), "workspaceId": checked.Get("workspaceId")})
	refreshed, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Properties: partial, Inputs: desired})
	require.NoError(t, err)
	_, err = server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: refreshed.Properties, OldInputs: desired, Inputs: desired})
	require.NoError(t, err)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, path, api.Last().Path)
	require.Contains(t, api.Last().Body, "name")
	require.NotContains(t, api.Last().Body, "issuer_id")
	require.NotContains(t, api.Last().Body, "workspace_id")
	require.NotContains(t, api.Last().Body, "token_lifetime_seconds", "initially omitted settings are not configured")
	require.True(t, updated.Properties.Get("description").IsNull())
	// A rejected false reset must not create a workspace or return proposed state.
	api.Handle("POST "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 400, "no enabled workspace remains")
	})
	_, err = server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
	require.Error(t, err)
	require.Equal(t, path, api.Last().Path)
}

func TestFederationRuleImportReadDriftAndArchive(t *testing.T) {
	server, api := providertest.New(t)
	federationRuleServerBehavior(api)
	urn := providertest.URN(federationRuleToken)
	in := rule("repo:acme/app:*")
	in["appliesToAllWorkspaces"] = false
	created, checked := providertest.Create(t, server, urn, providertest.Props(in))
	path := "/v1/organizations/federation_rules/" + created.ID
	api.Mutate(path, func(o fakeapi.Object) { delete(o, "workspace_id"); o["description"] = "" })
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, created.ID, imported.ID)
	require.True(t, imported.Inputs.Get("workspaceId").IsNull())
	require.Equal(t, float64(3600), imported.Inputs.Get("tokenLifetimeSeconds").AsNumber())
	require.False(t, imported.Inputs.Get("appliesToAllWorkspaces").AsBool())
	check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	again, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, imported, again)
	api.Mutate(path, func(o fakeapi.Object) {
		o["applies_to_all_workspaces"] = true
		o["oauth_scope"] = "workspace:inference"
	})
	drift, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Inputs: checked, Properties: created.Properties})
	require.NoError(t, err)
	require.True(t, drift.Inputs.Get("appliesToAllWorkspaces").AsBool())
	require.Equal(t, "workspace:inference", drift.Inputs.Get("oauthScope").AsString())
	require.Equal(t, checked.Get("workspaceId"), drift.Inputs.Get("workspaceId"))
	var status atomic.Int32
	status.Store(403)
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		if code := status.Load(); code != 200 {
			fakeapi.WriteError(w, int(code), "unavailable")
			return
		}
		fakeapi.WriteJSON(w, api.Object(path))
	})
	_, err = server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.Error(t, err)
	require.NoError(t, server.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: created.Properties}))
	require.Equal(t, path+"/archive", api.Last().Path)
	status.Store(200)
	gone, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
	api.Remove(path)
	status.Store(404)
	gone, err = server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
}

func TestFederationRuleSuccessfulResponseMapping(t *testing.T) {
	server, api := providertest.New(t)
	api.Handle("POST /v1/organizations/federation_rules", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "fdrl_mapped", "type": "federation_rule", "issuer_id": "fdis_1", "issuer_name": "github", "name": "deploy-from-main", "oauth_scope": "workspace:developer", "match": map[string]any{"subject_prefix": "server-subject"}, "target": map[string]any{"type": "service_account", "service_account_id": "svac_1"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "workspace_ids": []any{}})
	})
	in := providertest.Props(rule("desired-subject"))
	created, err := server.Create(p.CreateRequest{Urn: providertest.URN(federationRuleToken), Properties: in})
	require.NoError(t, err)
	require.Equal(t, "fdrl_mapped", created.ID)
	require.Equal(t, in.Get("workspaceId"), created.Properties.Get("workspaceId"), "retain the accepted write-only creation workspace")
	require.Equal(t, "server-subject", created.Properties.Get("match").AsMap().Get("subjectPrefix").AsString())
	require.True(t, created.Properties.Get("match").AsMap().Get("claims").IsNull(), "map the whole server matcher, not desired claims")
	require.True(t, created.Properties.Get("workspaceIds").IsArray())
	require.Zero(t, created.Properties.Get("workspaceIds").AsArray().Len())
}

func TestFederationRuleMalformedMutationAndRead(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN(federationRuleToken)
	api.Handle("POST /v1/organizations/federation_rules", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "fdrl_partial", "type": "federation_rule", "issuer_id": "fdis_1", "issuer_name": "github", "name": "deploy-from-main", "oauth_scope": "workspace:developer", "match": map[string]any{"subject_prefix": "repo:acme/app:*"}, "target": map[string]any{"type": "service_account", "service_account_id": "svac_1"}, "created_at": false, "updated_at": false, "workspace_ids": []any{}})
	})
	in := providertest.Props(rule("repo:acme/app:*"))
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: in})
	require.Error(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	state := in.Set("issuerName", property.New("github")).Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
	api.Handle("POST /v1/organizations/federation_rules/fdrl_partial", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "fdrl_foreign", "type": "federation_rule", "name": "foreign"})
	})
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: "fdrl_partial", State: state, Inputs: in, OldInputs: in})
	require.Error(t, err)
	require.Nil(t, updated.PartialState)
	require.Zero(t, updated.Properties.Len(), "foreign response cannot establish desired state")
	require.Equal(t, "POST", api.Last().Method)
	require.Equal(t, "/v1/organizations/federation_rules/fdrl_partial", api.Last().Path)
	api.Handle("GET /v1/organizations/federation_rules/fdrl_partial", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "fdrl_foreign", "type": "federation_rule", "archived_at": "2026-01-01T00:00:00Z"})
	})
	_, err = server.Read(p.ReadRequest{Urn: urn, ID: "fdrl_partial"})
	require.Error(t, err, "foreign archived observation is not absence")
}

// FR-R1: absent and empty claims serialize identically. Canonicalize to an
// empty map so its metadata need not be discarded or moved to another value.
func TestFederationRuleEmptyClaimsEquivalence(t *testing.T) {
	s, api := providertest.New(t)
	federationRuleServerBehavior(api)
	urn := providertest.URN(federationRuleToken)
	base := providertest.Props(rule("exact-subject"))
	match := base.Get("match").AsMap().Delete("claims")
	absent := base.Set("match", property.New(match))
	empty := base.Set("match", property.New(match.Set("claims", property.New(property.Map{}))))
	check := func(in property.Map) property.Map {
		out, err := s.Check(p.CheckRequest{Urn: urn, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, out.Failures)
		again, err := s.Check(p.CheckRequest{Urn: urn, Inputs: out.Inputs})
		require.NoError(t, err)
		require.Equal(t, out.Inputs, again.Inputs, "normalization must be idempotent")
		return out.Inputs
	}
	require.Equal(t, check(absent), check(empty))
	require.True(t, absent.Get("match").AsMap().Get("claims").IsNull(), "input bag must not be mutated")
	dep := []resourceurn.URN{"urn:pulumi:test::test::test:dep:Resource::claims"}
	decoratedEmpty := property.New(property.Map{}).WithSecret(true).WithDependencies(dep)
	parent := property.New(match.Set("claims", decoratedEmpty)).WithSecret(true).WithDependencies(dep)
	decorated := base.Set("match", parent)
	normalized := check(decorated)
	require.Equal(t, decorated, normalized, "metadata stays on its logical values")
	for _, pair := range [][2]property.Map{{absent, empty}, {empty, absent}, {absent, decorated}, {decorated, absent}} {
		// Exercise raw legacy/Read-shaped state as well as checked inputs: a Read
		// can legitimately recover omitted claims even after Check canonicalization.
		state := pair[0].Set("issuerName", property.New("github")).Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
		out, err := s.Diff(p.DiffRequest{Urn: urn, ID: "fdrl_equivalent", State: state, OldInputs: pair[0], Inputs: check(pair[1])})
		require.NoError(t, err)
		require.False(t, out.HasChanges, "empty and omitted claims must not cause Update: %#v", out.DetailedDiff)
		require.Empty(t, out.DetailedDiff)
	}
	for _, claims := range []property.Value{property.New(property.Computed).WithSecret(true).WithDependencies(dep), property.New(property.NewMap(map[string]property.Value{"owner": property.New("acme").WithSecret(true).WithDependencies(dep)}))} {
		in := base.Set("match", property.New(match.Set("claims", claims)))
		require.Equal(t, in, check(in), "unknown and nonempty claims must remain distinct and preserve metadata")
		d, err := s.Diff(p.DiffRequest{Urn: urn, ID: "fdrl_equivalent", State: check(absent), OldInputs: check(absent), Inputs: check(in)})
		require.NoError(t, err)
		require.True(t, d.HasChanges)
	}
	require.Empty(t, api.Requests(), "Check and Diff must not do I/O")
}

func TestFederationRuleDiffUnknownAndSemantics(t *testing.T) {
	s, api := providertest.New(t)
	federationRuleServerBehavior(api)
	urn := providertest.URN(federationRuleToken)
	in := rule("exact-subject")
	in["description"] = ""
	created, checked := providertest.Create(t, s, urn, providertest.Props(in))
	same, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: checked})
	require.NoError(t, err)
	require.False(t, same.HasChanges)
	for _, k := range []string{"issuerId", "workspaceId", "description", "match", "target"} {
		proposed := checked.Set(k, property.New(property.Computed))
		d, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: proposed})
		require.NoError(t, err)
		require.True(t, d.HasChanges, k)
	}
	for _, subject := range []string{"exact-subject*", "EXACT-subject"} {
		proposed := providertest.Props(rule(subject)).Set("description", property.New(""))
		d, err := s.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: proposed})
		require.NoError(t, err)
		require.True(t, d.HasChanges)
		require.False(t, d.DeleteBeforeReplace)
	}
}

func TestFederationRuleNullableDescription(t *testing.T) {
	for _, importing := range []bool{false, true} {
		name := "managed"
		if importing {
			name = "import"
		}
		t.Run(name, func(t *testing.T) {
			server, api := providertest.New(t)
			row := ruleResponse(nil)
			row["description"] = nil
			row["applies_to_all_workspaces"] = false
			row["token_lifetime_seconds"] = 3600
			api.Put("/v1/organizations/federation_rules/fdrl_response", row)
			request := p.ReadRequest{Urn: providertest.URN(federationRuleToken), ID: "fdrl_response"}
			if !importing {
				inputs := rule("repo:acme/app:*")
				inputs["description"] = "previously managed"
				request.Inputs = providertest.Props(inputs)
				request.Properties = request.Inputs
			}
			result, err := server.Read(request)
			require.NoError(t, err)
			require.Equal(t, request.ID, result.ID)
			require.True(t, result.Inputs.Get("description").IsNull())
			require.True(t, result.Properties.Get("description").IsNull())
			require.True(t, result.Properties.Get("issuerName").IsString())
			require.Empty(t, result.Properties.Get("issuerName").AsString())
			require.Len(t, api.Requests(), 1, "no enrichment lookup")
		})
	}
}

func ruleResponse(issuerName any) fakeapi.Object {
	return fakeapi.Object{
		"id": "fdrl_response", "type": "federation_rule",
		"issuer_id": "fdis_1", "issuer_name": issuerName,
		"name": "deploy-from-main", "oauth_scope": "workspace:developer",
		"target":        map[string]any{"type": "service_account", "service_account_id": "svac_1"},
		"match":         map[string]any{"subject_prefix": "repo:acme/app:*"},
		"workspace_ids": []any{"wrkspc_1"},
		"created_at":    "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z",
	}
}

func TestFederationRuleEmptyIssuerName(t *testing.T) {
	t.Run("empty", func(t *testing.T) { testFederationRuleEmptyIssuerName(t, "") })
	t.Run("null", func(t *testing.T) { testFederationRuleEmptyIssuerName(t, nil) })
}

func testFederationRuleEmptyIssuerName(t *testing.T, issuerName any) {
	server, api := providertest.New(t)
	path := "/v1/organizations/federation_rules/fdrl_response"
	api.Handle("POST /v1/organizations/federation_rules", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		row := ruleResponse(issuerName)
		api.Put(path, row)
		fakeapi.WriteJSON(w, row)
	})
	api.Handle("POST "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, ruleResponse(issuerName))
	})
	urn := providertest.URN(federationRuleToken)
	inputs := providertest.Props(rule("repo:acme/app:*"))
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, "fdrl_response", created.ID)
	require.True(t, created.Properties.Get("issuerName").IsString())
	require.Empty(t, created.Properties.Get("issuerName").AsString())
	require.Len(t, api.Requests(), 1, "no issuer lookup for an empty display name")

	read, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, err)
	require.True(t, read.Properties.Get("issuerName").IsString())
	require.Empty(t, read.Properties.Get("issuerName").AsString())
	require.Len(t, api.Requests(), 2)

	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("issuerName").IsString())
	require.Empty(t, updated.Properties.Get("issuerName").AsString())
	require.Len(t, api.Requests(), 3, "update is a single mutation, without enrichment or pre-read")
}

func TestFederationRuleIssuerNameTypeValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		omit  bool
		shape string
	}{
		{"absent", nil, true, "absent"},
		{"number", 42, false, "non-string"},
		{"object", map[string]any{"bad": true}, false, "non-string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, api := providertest.New(t)
			row := ruleResponse(tc.value)
			if tc.omit {
				delete(row, "issuer_name")
			}
			api.Put("/v1/organizations/federation_rules/fdrl_response", row)
			_, err := server.Read(p.ReadRequest{Urn: providertest.URN(federationRuleToken), ID: "fdrl_response"})
			require.ErrorContains(t, err, "invalid rule issuerName: expected a string or null, got "+tc.shape)
		})
	}
}

func TestFederationRuleCreateCompensatesInvalidObservation(t *testing.T) {
	for _, cleanupFails := range []bool{false, true} {
		name := "archived"
		if cleanupFails {
			name = "archive-failed"
		}
		t.Run(name, func(t *testing.T) {
			server, api := providertest.New(t)
			api.Handle("POST /v1/organizations/federation_rules", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteJSON(w, ruleResponse(42))
			})
			archived := false
			api.Handle("POST /v1/organizations/federation_rules/fdrl_response/archive", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				if cleanupFails {
					fakeapi.WriteError(w, http.StatusForbidden, "archive unavailable")
					return
				}
				archived = true
				fakeapi.WriteJSON(w, ruleResponse(""))
			})
			created, err := server.Create(p.CreateRequest{Urn: providertest.URN(federationRuleToken), Properties: providertest.Props(rule("subject"))})
			require.ErrorContains(t, err, "invalid rule issuerName")
			require.Empty(t, created.ID)
			require.Nil(t, created.PartialState)
			require.Equal(t, !cleanupFails, archived)
			require.Len(t, api.Requests(), 2)
			if cleanupFails {
				require.ErrorContains(t, err, "could not archive newly created federation rule fdrl_response")
			}
		})
	}
}

func TestFederationRuleNestedClaimPreviewMetadata(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN(federationRuleToken)
	base := providertest.Props(rule("repo:acme/app:*"))
	unknown := withOutputTestDependencies(property.New(property.Computed).WithSecret(true))
	match := base.Get("match").AsMap().Set("claims", property.New(property.NewMap(map[string]property.Value{"owner": unknown})))
	inputs := base.Set("match", property.New(match))
	checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, inputs, checked.Inputs)
	twice, err := server.Check(p.CheckRequest{Urn: u, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, checked.Inputs, twice.Inputs)
	state := base.Set("issuerName", property.New("github")).Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
	created, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: "fdrl_preview", State: state, OldInputs: base, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	for _, outputs := range []property.Map{created.Properties, updated.Properties} {
		requireNoOutputDependencies(t, property.New(outputs))
		v := outputs.Get("match")
		secret := v.Secret()
		for _, key := range []string{"claims", "owner"} {
			if v.IsComputed() {
				break // An unknown ancestor also protects the leaf's knownness.
			}
			require.True(t, v.IsMap())
			v = v.AsMap().Get(key)
			secret = secret || v.Secret()
		}
		require.True(t, v.IsComputed(), "the logical unknown claim must not become known")
		require.True(t, secret, "the claim must be protected by a secret ancestor or leaf")
	}
	require.Equal(t, inputs, checked.Inputs)
	require.Empty(t, api.Requests())
}
