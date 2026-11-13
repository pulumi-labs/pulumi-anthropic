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

package agents_test

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

const deploymentToken = "anthropic:agents:Deployment"

var deploymentURN = providertest.URN(deploymentToken)

// Synthetic responses model the documented control plane, not live behavior.
func deploymentDefaults(obj fakeapi.Object) {
	if id, ok := obj["agent"].(string); ok {
		obj["agent"] = map[string]any{"type": "agent", "id": id, "version": 7.0}
	}
	for k, v := range map[string]any{"description": "", "metadata": map[string]any{}, "resources": []any{}, "vault_ids": []any{}, "schedule": nil, "archived_at": nil, "status": "active", "paused_reason": nil} {
		if _, ok := obj[k]; !ok {
			obj[k] = v
		}
	}
}

func deploymentServerBehavior(api *fakeapi.Server) {
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: deploymentDefaults, OnUpdate: func(_, after, _ fakeapi.Object) error { deploymentDefaults(after); return nil }})
}

func baseDeployment() map[string]any {
	return map[string]any{"name": "triage", "agent": map[string]any{"id": "agent_1"}, "environmentId": "env_1", "initialEvents": []any{map[string]any{"type": "user.message", "content": []any{map[string]any{"type": "text", "text": "Triage new issues."}}}}, "resources": []any{map[string]any{"type": "github_repository", "url": "https://github.com/acme/app", "checkout": map[string]any{"type": "branch", "name": "main"}, "authorizationToken": secret("ghp")}}, "schedule": map[string]any{"expression": "0 9 * * 1-5", "timezone": "UTC"}, "budget": map[string]any{"maxListCost": map[string]any{"amount": "2500", "currency": "USD"}}}
}

func deploymentMinimal() map[string]any {
	a := baseDeployment()
	delete(a, "resources")
	delete(a, "schedule")
	delete(a, "budget")
	return a
}

func TestDeploymentMinimalLifecycle(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	updated := deploymentMinimal()
	updated["name"] = "updated-name"
	integration.LifeCycleTest{Resource: deploymentToken,
		Create:  integration.Operation{Inputs: providertest.Props(deploymentMinimal())},
		Updates: []integration.Operation{{Inputs: providertest.Props(updated)}},
	}.Run(t, server)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.True(t, strings.HasSuffix(api.Last().Path, "/archive"))
}

func TestDeploymentLifecycle(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))
	require.Equal(t, "active", created.Properties.Get("status").AsString())
	require.True(t, created.Properties.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken").Secret())
	require.True(t, created.Properties.Get("agent").AsMap().Get("version").IsNull())
	body := api.Last().Body
	require.Equal(t, "agent_1", body["agent"])
	require.Equal(t, "managed-agents-2026-04-01", api.Last().Header.Get("anthropic-beta"))
	require.Equal(t, map[string]any{"type": "limit", "max_list_cost": map[string]any{"amount": "2500", "currency": "USD"}}, body["budget"])
	pinned := baseDeployment()
	pinned["agent"] = map[string]any{"id": "agent_1", "version": 3}
	checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(pinned)})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: created.Properties, OldInputs: inputs, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Len(t, api.Requests(), before+1, "Update must not perform an internal read")
	require.Equal(t, map[string]any{"type": "agent", "id": "agent_1", "version": 3.0}, api.Last().Body["agent"])
	require.Len(t, api.Last().Body, 7, "all desired mutable fields are sent")
	require.Equal(t, "triage", api.Last().Body["name"])
	refreshed := providertest.Read(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: updated.Properties}, checked.Inputs)
	require.Equal(t, checked.Inputs, refreshed.Inputs)
	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: deploymentURN})
	require.NoError(t, err)
	require.Equal(t, 3.0, imported.Inputs.Get("agent").AsMap().Get("version").AsNumber())
	require.True(t, imported.Inputs.Get("paused").IsNull())
	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: deploymentURN, Properties: updated.Properties}))
	require.Equal(t, "/v1/deployments/"+created.ID+"/archive", api.Last().Path)
	for _, r := range api.Requests() {
		require.False(t, strings.HasSuffix(r.Path, "/pause") || strings.HasSuffix(r.Path, "/unpause") || strings.HasSuffix(r.Path, "/run"))
	}
}

func TestDeploymentStatusIsObservationOnly(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	a := baseDeployment()
	a["paused"] = true
	checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(a)})
	require.NoError(t, err)
	require.NotEmpty(t, checked.Failures)
	require.Empty(t, api.Requests())
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))
	api.Mutate("/v1/deployments/"+created.ID, func(o fakeapi.Object) { o["status"] = "paused" })
	read := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, "paused", read.Properties.Get("status").AsString())
	require.Equal(t, inputs, read.Inputs)
	_, err = server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: read.Properties, OldInputs: read.Inputs, Inputs: read.Inputs})
	require.NoError(t, err)
	require.Equal(t, "/v1/deployments/"+created.ID, api.Last().Path)
	require.NotContains(t, api.Last().Body, "paused")
}

func TestDeploymentRemovalAndUnusableMutation(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	a := baseDeployment()
	a["description"] = "remove"
	a["metadata"] = map[string]any{"keep": "yes", "remove": "old"}
	a["vaultIds"] = []any{"vault_1"}
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(a))
	desired := deploymentMinimal()
	desired["metadata"] = map[string]any{"keep": "yes"}
	check, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(desired)})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: created.Properties, OldInputs: inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	body := api.Last().Body
	require.Equal(t, fakeapi.Object{"name": "triage", "agent": "agent_1", "environment_id": "env_1", "initial_events": []any{map[string]any{"type": "user.message", "content": []any{map[string]any{"type": "text", "text": "Triage new issues."}}}}, "description": nil, "schedule": nil, "budget": nil, "resources": nil, "vault_ids": nil, "metadata": map[string]any{"keep": "yes", "remove": nil}}, body)
	before := len(api.Requests())
	_, err = server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: updated.Properties, OldInputs: check.Inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	require.Len(t, api.Requests(), before+1, "even equal inputs send managed configuration")
	// Successful allocation with an unusable response returns an ordinary error.
	api.Handle("POST /v1/deployments", func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		body["id"] = "deployment_partial"
		deploymentDefaults(body)
		delete(body, "status")
		fakeapi.WriteJSON(w, body)
	})
	failed, err := server.Create(p.CreateRequest{Urn: deploymentURN, Properties: check.Inputs})
	require.Error(t, err)
	require.Empty(t, failed.ID)
	require.Nil(t, failed.PartialState)
	require.Zero(t, failed.Properties.Len())
	api.Handle("POST /v1/deployments/"+created.ID, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		body["id"] = created.ID
		deploymentDefaults(body)
		delete(body, "status")
		fakeapi.WriteJSON(w, body)
	})
	before = len(api.Requests())
	out, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: updated.Properties, OldInputs: check.Inputs, Inputs: check.Inputs})
	require.Error(t, err)
	require.Nil(t, out.PartialState)
	require.Zero(t, out.Properties.Len())
	require.Len(t, api.Requests(), before+1, "no internal recovery GET")
}

func TestDeploymentReadErrorsAndIdentity(t *testing.T) {
	for _, status := range []int{403, 404, 409, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, api := providertest.New(t)
			api.Handle("GET /v1/deployments/deployment_x", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteError(w, status, "synthetic")
			})
			r, err := server.Read(p.ReadRequest{Urn: deploymentURN, ID: "deployment_x"})
			if status == 404 {
				require.NoError(t, err)
				require.Empty(t, r.ID)
			} else {
				require.Error(t, err)
			}
		})
	}
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(deploymentMinimal()))
	api.Handle("GET /v1/deployments/"+created.ID, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		o := api.Object("/v1/deployments/" + created.ID)
		o["id"] = "foreign"
		fakeapi.WriteJSON(w, o)
	})
	_, err := server.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.ErrorContains(t, err, "different identity")
}

func TestDeploymentUnknownPreview(t *testing.T) {
	server, api := providertest.New(t)
	a := deploymentMinimal()
	a["environmentId"] = property.New(property.Computed)
	checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(a)})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("environmentId").IsComputed())
	preview, err := server.Create(p.CreateRequest{Urn: deploymentURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, preview.Properties.Get("environmentId").IsComputed())
	require.Empty(t, api.Requests())
}

func TestDeploymentCheckValidationMetadataAndNonmutation(t *testing.T) {
	server, api := providertest.New(t)
	for _, field := range []string{"resources", "initialEvents", "vaultIds", "metadata", "budget", "agent", "environmentId"} {
		t.Run("unknown-"+field, func(t *testing.T) {
			a := baseDeployment()
			a[field] = property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::x:y:z::dependency"})
			bag := providertest.Props(a)
			checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: bag})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, bag, checked.Inputs)
			again, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Equal(t, checked.Inputs, again.Inputs)
			preview, err := server.Create(p.CreateRequest{Urn: deploymentURN, Properties: checked.Inputs, DryRun: true})
			require.NoError(t, err)
			require.True(t, preview.Properties.Get(field).IsComputed())
			require.True(t, preview.Properties.Get(field).Secret())
			requireNoOutputDependencies(t, property.New(preview.Properties))
			planned, err := server.Update(p.UpdateRequest{Urn: deploymentURN, ID: "preview", State: preview.Properties, Inputs: checked.Inputs, DryRun: true})
			require.NoError(t, err)
			require.True(t, planned.Properties.Get(field).IsComputed())
			require.True(t, planned.Properties.Get(field).Secret())
			requireNoOutputDependencies(t, property.New(planned.Properties))
		})
	}
	a := baseDeployment()
	r := a["resources"].([]any)[0].(map[string]any)
	r["authorizationToken"] = property.New("synthetic-token").WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::x:y:z::token"})
	bag := providertest.Props(a)
	checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: bag})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	token := checked.Inputs.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken")
	require.True(t, token.Secret())
	require.Equal(t, []resourceurn.URN{"urn:pulumi:test::test::x:y:z::token"}, token.Dependencies())
	require.False(t, bag.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken").Secret(), "Check must not mutate inputs")
	require.Empty(t, api.Requests())
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"events-count", func(a map[string]any) { a["initialEvents"] = []any{} }},
		{"event-variant", func(a map[string]any) { a["initialEvents"] = []any{map[string]any{"type": "foreign"}} }},
		{"file", func(a map[string]any) { a["resources"] = []any{map[string]any{"type": "file", "url": "wrong"}} }},
		{"checkout", func(a map[string]any) {
			a["resources"] = []any{map[string]any{"type": "github_repository", "url": "https://example.invalid/repo", "checkout": map[string]any{"type": "commit", "name": "wrong"}}}
		}},
		{"memory-access", func(a map[string]any) {
			a["resources"] = []any{map[string]any{"type": "memory_store", "memoryStoreId": "m", "access": "root"}}
		}},
		{"currency", func(a map[string]any) {
			a["budget"] = map[string]any{"maxListCost": map[string]any{"amount": "2500", "currency": "EUR"}}
		}},
		{"negative", func(a map[string]any) {
			a["budget"] = map[string]any{"maxListCost": map[string]any{"amount": "-1", "currency": "USD"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := baseDeployment()
			c.edit(a)
			checked, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(a)})
			require.NoError(t, err)
			require.NotEmpty(t, checked.Failures)
		})
	}
}

func TestDeploymentGeneratedVariantsAndExplicitEmpty(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	a := deploymentMinimal()
	a["resources"] = []any{map[string]any{"type": "github_repository", "url": "https://example.invalid/repo", "checkout": map[string]any{"type": "commit", "sha": "abc"}}, map[string]any{"type": "file", "fileId": "file_1", "mountPath": "/tmp/input"}, map[string]any{"type": "memory_store", "memoryStoreId": "memstore_1", "access": "read_only", "instructions": ""}}
	a["initialEvents"] = []any{map[string]any{"type": "system.message", "content": []any{map[string]any{"type": "text", "text": "context"}}}, map[string]any{"type": "user.define_outcome", "description": "done", "max_iterations": 2, "rubric": map[string]any{"type": "text", "content": "pass"}}, map[string]any{"type": "user.message", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.invalid/image"}}, map[string]any{"type": "document", "title": "fixture", "source": map[string]any{"type": "file", "file_id": "file_1"}}, map[string]any{"type": "redacted"}}}}
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(a))
	require.Equal(t, "active", created.Properties.Get("status").AsString())
	require.Len(t, api.Last().Body["resources"], 3)
	require.Equal(t, inputs, providertest.Read(t, server, deploymentURN, created, inputs).Inputs)
	a["resources"] = []any{}
	a["vaultIds"] = []any{}
	check, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(a)})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	out, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: created.Properties, OldInputs: inputs, Inputs: check.Inputs})
	require.NoError(t, err)
	require.Equal(t, []any{}, api.Last().Body["resources"])
	require.Equal(t, []any{}, api.Last().Body["vault_ids"])
	require.True(t, out.Properties.Get("resources").IsArray())
	require.True(t, out.Properties.Get("vaultIds").IsArray())
}

func TestDeploymentLegacyIncompleteStateRefresh(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))
	legacy := created.Properties.Delete("status").Delete("createdAt").Delete("updatedAt")
	refreshed, err := server.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID, Inputs: inputs, Properties: legacy})
	require.NoError(t, err)
	require.Equal(t, created.ID, refreshed.ID)
	require.Equal(t, "active", refreshed.Properties.Get("status").AsString())
	require.Equal(t, created.Properties.Get("createdAt"), refreshed.Properties.Get("createdAt"))
	require.True(t, refreshed.Properties.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken").Secret())
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: deploymentURN, ID: refreshed.ID, State: refreshed.Properties, Inputs: refreshed.Inputs})
	require.NoError(t, err)
	require.Equal(t, "active", updated.Properties.Get("status").AsString())
	require.Len(t, api.Requests(), before+1, "Update must not refresh internally")
}

func TestDeploymentResponseValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(fakeapi.Object)
	}{
		{"numeric-name", func(o fakeapi.Object) { o["name"] = 42 }},
		{"invalid-status", func(o fakeapi.Object) { o["status"] = "foreign" }},
		{"null-vault-ids", func(o fakeapi.Object) { o["vault_ids"] = nil }},
		{"null-resources", func(o fakeapi.Object) { o["resources"] = nil }},
		{"invalid-version", func(o fakeapi.Object) { o["agent"] = map[string]any{"type": "agent", "id": "agent_1", "version": "2"} }},
		{"unknown-resource", func(o fakeapi.Object) { o["resources"] = []any{map[string]any{"type": "foreign"}} }},
		{"invalid-resource-field", func(o fakeapi.Object) {
			o["resources"] = []any{map[string]any{"type": "file", "file_id": "file_1", "mount_path": 42}}
		}},
		{"invalid-budget", func(o fakeapi.Object) {
			o["budget"] = map[string]any{"type": "limit", "max_list_cost": map[string]any{"currency": "USD", "amount": "garbage"}}
		}},
		{"invalid-event", func(o fakeapi.Object) {
			o["initial_events"] = []any{map[string]any{"type": "user.message", "content": []any{map[string]any{"type": "image", "source": map[string]any{"type": "foreign"}}}}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server, api := providertest.New(t)
			deploymentServerBehavior(api)
			created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(deploymentMinimal()))
			path := "/v1/deployments/" + created.ID
			api.Mutate(path, c.change)
			_, err := server.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID, Inputs: inputs, Properties: created.Properties})
			require.Error(t, err)
		})
	}
}

func TestDeploymentForeignUpdateObservation(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(deploymentMinimal()))
	api.Handle("POST /v1/deployments/"+created.ID, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		body["id"] = "foreign"
		body["status"] = "paused"
		body["created_at"] = "2099-01-01T00:00:00Z"
		body["updated_at"] = "2099-01-01T00:00:00Z"
		fakeapi.WriteJSON(w, body)
	})
	out, err := server.Update(p.UpdateRequest{Urn: deploymentURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: inputs})
	require.Error(t, err)
	require.Nil(t, out.PartialState)
	require.Zero(t, out.Properties.Len(), "foreign observations must not produce saved state")
}

func TestDeploymentNoAmbiguousCreateRetry(t *testing.T) {
	server, api := providertest.New(t)
	api.Handle("POST /v1/deployments", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 500, "synthetic response after possible allocation")
	})
	out, err := server.Create(p.CreateRequest{Urn: deploymentURN, Properties: providertest.Props(deploymentMinimal())})
	require.Error(t, err)
	require.Empty(t, out.ID)
	require.Nil(t, out.PartialState)
	require.Zero(t, out.Properties.Len())
	require.Len(t, api.Requests(), 1, "generated create conservative retry policy must not retry an ambiguous allocation")
}

func TestDeploymentArchiveAbsenceAndTokenReadback(t *testing.T) {
	server, api := providertest.New(t)
	deploymentServerBehavior(api)
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(baseDeployment()))
	path := "/v1/deployments/" + created.ID
	api.Mutate(path, func(o fakeapi.Object) {
		for _, r := range o["resources"].([]any) {
			delete(r.(map[string]any), "authorization_token")
		}
	})
	read := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, inputs, read.Inputs, "known write-only token survives readable response omission")
	imported, err := server.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID})
	require.NoError(t, err)
	require.True(t, imported.Properties.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken").IsNull(), "ID-only read must not manufacture credentials")
	require.NoError(t, server.Delete(p.DeleteRequest{ID: created.ID, Urn: deploymentURN, Properties: read.Properties}))
	gone, err := server.Read(p.ReadRequest{ID: created.ID, Urn: deploymentURN, Properties: read.Properties, Inputs: read.Inputs})
	require.NoError(t, err)
	require.Empty(t, gone.ID)
}

func deploymentReviewEvents() []any {
	return []any{
		map[string]any{"type": "user.define_outcome", "description": "finish", "rubric": map[string]any{"type": "text", "content": "done"}},
		map[string]any{"type": "user.message", "content": []any{map[string]any{"type": "document", "source": map[string]any{"type": "text", "data": "context"}}}},
	}
}

// Responses may contain null optionals even though request optionals accept only
// omission or a typed value. These are independently authored response changes,
// not request echoes and not observations of live platform behavior.
func deploymentReviewResponse(expandedDefaults bool) func(fakeapi.Object) {
	return func(o fakeapi.Object) {
		deploymentDefaults(o)
		events := o["initial_events"].([]any)
		outcome := events[0].(map[string]any)
		outcome["max_iterations"] = nil
		if expandedDefaults {
			outcome["max_iterations"] = 3.0
		}
		doc := events[1].(map[string]any)["content"].([]any)[0].(map[string]any)
		doc["title"], doc["context"] = nil, nil
		if expandedDefaults {
			doc["source"].(map[string]any)["media_type"] = "text/plain"
		}
	}
}

func TestDeploymentNullableEventResponses(t *testing.T) {
	server, api := providertest.New(t)
	observe := deploymentReviewResponse(false)
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: observe, OnUpdate: func(_, after, _ fakeapi.Object) error { observe(after); return nil }})
	a := deploymentMinimal()
	a["initialEvents"] = deploymentReviewEvents()
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(a))
	require.Equal(t, inputs.Get("initialEvents"), created.Properties.Get("initialEvents"))
	read := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, inputs.Get("initialEvents"), read.Inputs.Get("initialEvents"))
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: read.Properties, OldInputs: read.Inputs, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, inputs.Get("initialEvents"), updated.Properties.Get("initialEvents"))
	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: deploymentURN})
	require.NoError(t, err)
	importEvents := imported.Inputs.Get("initialEvents").AsArray()
	require.True(t, importEvents.Get(0).AsMap().Get("max_iterations").IsNull())
	document := importEvents.Get(1).AsMap().Get("content").AsArray().Get(0).AsMap()
	require.True(t, document.Get("title").IsNull())
	require.True(t, document.Get("context").IsNull())
	check, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures, "recovered nullable response fields must produce usable inputs")

	// A response-only null allowance must not accidentally become a request allowance.
	for _, field := range []string{"max_iterations", "title", "context"} {
		a["initialEvents"] = deploymentReviewEvents()
		events := a["initialEvents"].([]any)
		if field == "max_iterations" {
			events[0].(map[string]any)[field] = property.Value{}
		} else {
			events[1].(map[string]any)["content"].([]any)[0].(map[string]any)[field] = property.Value{}
		}
		check, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: providertest.Props(a)})
		require.NoError(t, err)
		require.NotEmpty(t, check.Failures, field)
	}
}

func TestDeploymentExpandedEventDefaultsConverge(t *testing.T) {
	server, api := providertest.New(t)
	observe := deploymentReviewResponse(true)
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: observe, OnUpdate: func(_, after, _ fakeapi.Object) error { observe(after); return nil }})
	a := deploymentMinimal()
	a["initialEvents"] = deploymentReviewEvents()
	created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(a))
	require.Equal(t, inputs.Get("initialEvents"), created.Properties.Get("initialEvents"))
	require.False(t, providertest.Diff(t, server, deploymentURN, created, inputs, inputs).HasChanges)
	read := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, inputs.Get("initialEvents"), read.Inputs.Get("initialEvents"))
	require.False(t, providertest.Diff(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: read.Properties}, read.Inputs, inputs).HasChanges)
	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: deploymentURN})
	require.NoError(t, err)
	events := imported.Inputs.Get("initialEvents").AsArray()
	require.Equal(t, float64(3), events.Get(0).AsMap().Get("max_iterations").AsNumber(), "ID-only Read recovers the observed default as a usable explicit value")
	require.Equal(t, "text/plain", events.Get(1).AsMap().Get("content").AsArray().Get(0).AsMap().Get("source").AsMap().Get("media_type").AsString())

	// A nondefault observation is drift even when the optional value was omitted.
	api.Mutate("/v1/deployments/"+created.ID, func(o fakeapi.Object) { o["initial_events"].([]any)[0].(map[string]any)["max_iterations"] = 4.0 })
	drift := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, float64(4), drift.Inputs.Get("initialEvents").AsArray().Get(0).AsMap().Get("max_iterations").AsNumber())
	require.True(t, providertest.Diff(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: drift.Properties}, drift.Inputs, inputs).HasChanges)
}

func TestDeploymentExplicitEventDefaultsAndMetadata(t *testing.T) {
	server, api := providertest.New(t)
	observe := deploymentReviewResponse(true)
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: observe, OnUpdate: func(_, after, _ fakeapi.Object) error { observe(after); return nil }})
	a := deploymentMinimal()
	events := deploymentReviewEvents()
	dep := []resourceurn.URN{"urn:pulumi:test::test::x:y:z::iterations"}
	events[0].(map[string]any)["max_iterations"] = property.New(float64(3)).WithSecret(true).WithDependencies(dep)
	events[1].(map[string]any)["content"].([]any)[0].(map[string]any)["source"].(map[string]any)["media_type"] = property.New("text/plain").WithSecret(true).WithDependencies(dep)
	a["initialEvents"] = events
	bag := providertest.Props(a)
	check, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: bag})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	require.Equal(t, bag, check.Inputs, "handling response defaults must not rewrite or lose metadata on configured values")
	checkedAgain, err := server.Check(p.CheckRequest{Urn: deploymentURN, Inputs: check.Inputs})
	require.NoError(t, err)
	require.Equal(t, check.Inputs, checkedAgain.Inputs)
	created, inputs := providertest.Create(t, server, deploymentURN, check.Inputs)
	read := providertest.Read(t, server, deploymentURN, created, inputs)
	updated, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: read.Properties, OldInputs: inputs, Inputs: inputs.Set("name", property.New("updated"))})
	require.NoError(t, err)
	for name, bag := range map[string]property.Map{"Create": created.Properties, "ReadInputs": read.Inputs, "ReadOutputs": read.Properties, "Update": updated.Properties} {
		t.Run(name, func(t *testing.T) {
			requireOutputValue(t, inputs.Get("initialEvents"), bag.Get("initialEvents"))
		})
	}
	require.False(t, read.Inputs.Get("initialEvents").AsArray().Get(0).AsMap().Get("max_iterations").IsNull())
	require.False(t, read.Inputs.Get("initialEvents").AsArray().Get(1).AsMap().Get("content").AsArray().Get(0).AsMap().Get("source").AsMap().Get("media_type").IsNull())
	require.False(t, providertest.Diff(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: read.Properties}, read.Inputs, inputs).HasChanges)
	api.Mutate("/v1/deployments/"+created.ID, func(o fakeapi.Object) { o["initial_events"].([]any)[0].(map[string]any)["max_iterations"] = 4.0 })
	drift := providertest.Read(t, server, deploymentURN, created, inputs)
	require.Equal(t, float64(4), drift.Inputs.Get("initialEvents").AsArray().Get(0).AsMap().Get("max_iterations").AsNumber())
	require.True(t, providertest.Diff(t, server, deploymentURN, p.CreateResponse{ID: created.ID, Properties: drift.Properties}, drift.Inputs, inputs).HasChanges)
}

func TestDeploymentRepositoryTokenPresence(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "omitted"
		if explicit {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			server, api := providertest.New(t)
			deploymentServerBehavior(api)
			a := deploymentMinimal()
			r := map[string]any{"type": "github_repository", "url": "https://example.invalid/repo"}
			if explicit {
				r["authorizationToken"] = ""
			}
			a["resources"] = []any{r}
			created, inputs := providertest.Create(t, server, deploymentURN, providertest.Props(a))
			assertToken := func() {
				wire := api.Last().Body["resources"].([]any)[0].(map[string]any)
				value, present := wire["authorization_token"]
				require.Equal(t, explicit, present)
				if explicit {
					require.Equal(t, "", value)
				}
			}
			assertToken()
			_, err := server.Update(p.UpdateRequest{ID: created.ID, Urn: deploymentURN, State: created.Properties, OldInputs: inputs, Inputs: inputs})
			require.NoError(t, err)
			assertToken()
			token := created.Properties.Get("resources").AsArray().Get(0).AsMap().Get("authorizationToken")
			if explicit {
				require.True(t, token.IsString())
				require.Equal(t, "", token.AsString())
				require.True(t, token.Secret())
			} else {
				require.True(t, token.IsNull())
			}
		})
	}
}

func TestDeploymentReorderedRepositoryTokenOutputs(t *testing.T) {
	s, api := providertest.New(t)
	observe := func(o fakeapi.Object) {
		deploymentDefaults(o)
		resources := o["resources"].([]any)
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].(map[string]any)["url"].(string) > resources[j].(map[string]any)["url"].(string)
		})
		for _, v := range resources {
			delete(v.(map[string]any), "authorization_token")
		}
	}
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: observe, OnUpdate: func(_, after, _ fakeapi.Object) error { observe(after); return nil }})
	a := deploymentMinimal()
	deps := []resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::token"}
	a["resources"] = []any{
		map[string]any{"type": "github_repository", "url": "https://example.invalid/a", "authorizationToken": property.New("synthetic-a").WithDependencies(deps)},
		map[string]any{"type": "github_repository", "url": "https://example.invalid/b", "authorizationToken": property.New("synthetic-b").WithDependencies(deps)},
	}
	created, checked := providertest.Create(t, s, deploymentURN, providertest.Props(a))
	read := providertest.Read(t, s, deploymentURN, created, checked)
	updated, err := s.Update(p.UpdateRequest{Urn: deploymentURN, ID: created.ID, State: read.Properties, OldInputs: checked, Inputs: checked.Set("name", property.New("updated"))})
	require.NoError(t, err)
	for name, m := range map[string]property.Map{"Create": created.Properties, "Read": read.Properties, "Update": updated.Properties} {
		t.Run(name, func(t *testing.T) {
			requireNoOutputDependencies(t, property.New(m))
			resources := m.Get("resources")
			require.Equal(t, 2, resources.AsArray().Len())
			for i, resource := range resources.AsArray().All {
				r := resource.AsMap()
				suffix := "b"
				if i == 1 {
					suffix = "a"
				}
				require.Equal(t, "https://example.invalid/"+suffix, r.Get("url").AsString())
				token := r.Get("authorizationToken")
				require.Equal(t, "synthetic-"+suffix, token.AsString(), "write-only token follows repository identity")
				require.True(t, resources.Secret() || resource.Secret() || token.Secret())
			}
		})
	}
	imported, err := s.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID})
	require.NoError(t, err)
	for _, resource := range imported.Properties.Get("resources").AsArray().All {
		require.True(t, resource.AsMap().Get("authorizationToken").IsNull(), "import cannot recover write-only tokens")
	}
}

func TestDeploymentEmptyCollectionOutputMetadata(t *testing.T) {
	s, api := providertest.New(t)
	deploymentServerBehavior(api)
	inputs := providertest.Props(deploymentMinimal())
	deps := []resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::empty"}
	for _, k := range []string{"resources", "vaultIds", "metadata"} {
		v := property.New([]property.Value{})
		if k == "metadata" {
			v = property.New(property.Map{})
		}
		inputs = inputs.Set(k, v.WithSecret(true).WithDependencies(deps))
	}
	created, checked := providertest.Create(t, s, deploymentURN, inputs)
	read := providertest.Read(t, s, deploymentURN, created, checked)
	preview, err := s.Create(p.CreateRequest{Urn: deploymentURN, Properties: checked, DryRun: true})
	require.NoError(t, err)
	updateReq := p.UpdateRequest{Urn: deploymentURN, ID: created.ID, State: read.Properties, OldInputs: checked, Inputs: checked.Set("name", property.New("updated")), DryRun: true}
	planned, err := s.Update(updateReq)
	require.NoError(t, err)
	updateReq.DryRun = false
	updated, err := s.Update(updateReq)
	require.NoError(t, err)
	for name, m := range map[string]property.Map{"Create": created.Properties, "Read": read.Properties, "CreatePreview": preview.Properties, "UpdatePreview": planned.Properties, "Update": updated.Properties} {
		t.Run(name, func(t *testing.T) {
			requireNoOutputDependencies(t, property.New(m))
			for _, k := range []string{"resources", "vaultIds", "metadata"} {
				requireOutputValue(t, checked.Get(k), m.Get(k))
			}
		})
	}
}

func TestDeploymentStateOnlyReadProtectsRecoveredEvents(t *testing.T) {
	s, api := providertest.New(t)
	api.SetHooks("deployments", fakeapi.Hooks{OnCreate: deploymentReviewResponse(false)})
	args := deploymentMinimal()
	events := deploymentReviewEvents()
	events[0].(map[string]any)["description"] = property.New("finish").WithSecret(true).WithDependencies([]resourceurn.URN{"urn:event"})
	args["initialEvents"] = events
	created, _ := providertest.Create(t, s, deploymentURN, providertest.Props(args))
	require.True(t, created.Properties.Get("initialEvents").HasSecrets())
	n := len(api.Requests())
	read, err := s.Read(p.ReadRequest{Urn: deploymentURN, ID: created.ID, Properties: created.Properties})
	require.NoError(t, err)
	require.Equal(t, n+1, len(api.Requests()))
	require.True(t, read.Inputs.Get("initialEvents").HasSecrets(), "state-only Read must protect recovered secret inputs")
	require.True(t, read.Properties.Get("initialEvents").HasSecrets())
	requireNoOutputDependencies(t, property.New(read.Properties))
}
