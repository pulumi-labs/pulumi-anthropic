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
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const federationRuleToken = "anthropic:identity:FederationRule"

// federationRuleServerBehavior mimics the API's resolved and defaulted fields. Nested
// objects are rebuilt rather than modified, since they are shared with the recorded request.
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
				assert.Equal(t, fakeapi.Object{"match": map[string]any{
					"subject_prefix": "repo:acme/app:ref:",
					"claims":         map[string]any{"ref": "refs/heads/main"},
				}}, reqs[len(reqs)-1].Body, "a changed match is sent whole")
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
	}
}
