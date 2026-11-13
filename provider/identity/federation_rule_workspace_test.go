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
	"encoding/json"
	"fmt"
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

const federationRuleWorkspaceToken = "anthropic:identity:FederationRuleWorkspace"

const rulePath = "/v1/organizations/federation_rules/fdrl_1"

// ruleBindingServerBehavior seeds a rule; its workspace bindings are a sub-collection
// keyed by workspace ID, whose objects the API fills in with the workspace's name.
func ruleBindingServerBehavior(api *fakeapi.Server) {
	api.Put(rulePath, fakeapi.Object{"id": "fdrl_1", "archived_at": nil})
	api.SetHooks("workspaces", fakeapi.Hooks{KeyField: "workspace_id", OnCreate: func(obj fakeapi.Object) {
		obj["type"] = "federation_rule_workspace"
		obj["federation_rule_id"] = "fdrl_1"
		obj["workspace_name"] = "Production"
		obj["created_at"] = "2026-09-25T00:00:00Z"
	}})
}

func binding(workspaceID string) map[string]any {
	return map[string]any{"federationRuleId": "fdrl_1", "workspaceId": workspaceID}
}

func TestFederationRuleWorkspaceLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	ruleBindingServerBehavior(api)

	integration.LifeCycleTest{
		Resource: federationRuleWorkspaceToken,
		Create: integration.Operation{
			Inputs: providertest.Props(binding("wrkspc_1")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, rulePath+"/workspaces")
				assert.Equal(t, fakeapi.Object{"workspace_id": "wrkspc_1"}, req.Body)
				assert.Equal(t, "Production", output.Get("workspaceName").AsString())
			},
		},
		// Bindings cannot be updated, so a change of workspace replaces the binding.
		Updates: []integration.Operation{{Inputs: providertest.Props(binding("wrkspc_2"))}},
	}.Run(t, server)

	api.LastRequest(t, http.MethodDelete, rulePath+"/workspaces/wrkspc_1")
	api.LastRequest(t, http.MethodDelete, rulePath+"/workspaces/wrkspc_2")
	assert.Nil(t, api.Object(rulePath+"/workspaces/wrkspc_2"))
}

func TestFederationRuleWorkspaceRead(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	ruleBindingServerBehavior(api)

	created, checked := providertest.Create(t, server, providertest.URN(federationRuleWorkspaceToken), providertest.Props(binding("wrkspc_1")))
	assert.Equal(t, "fdrl_1/wrkspc_1", created.ID)
	read := providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked)
	assert.Equal(t, created.ID, read.ID)
	assert.Equal(t, checked, read.Inputs)

	imported, err := server.Read(p.ReadRequest{ID: created.ID, Urn: providertest.URN(federationRuleWorkspaceToken)})
	assert.NoError(t, err)
	assert.Equal(t, checked, imported.Inputs, "an import recovers both IDs from the resource ID")
	assert.Equal(t, "Production", imported.Properties.Get("workspaceName").AsString(),
		"an import reads the binding's details")

	api.Remove(rulePath + "/workspaces/wrkspc_1")
	assert.Empty(t, providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked).ID,
		"a binding removed outside of Pulumi reads as gone")

	api.Remove(rulePath)
	assert.Empty(t, providertest.Read(t, server, providertest.URN(federationRuleWorkspaceToken), created, checked).ID,
		"a binding of a rule that no longer exists reads as gone")
}

const federationRuleWorkspacePath = "/v1/organizations/federation_rules/fdrl_audit/workspaces"

func federationRuleWorkspaceInputs() property.Map {
	return providertest.Props(map[string]any{"federationRuleId": "fdrl_audit", "workspaceId": "wrkspc_audit"})
}

// Contract: unknown IDs survive preview; changing either structural ID replaces.
func TestFederationRuleWorkspacePreviewAndReplacement(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN(federationRuleWorkspaceToken)
	unknown := property.NewMap(map[string]property.Value{
		"federationRuleId": property.New("fdrl_audit"), "workspaceId": property.New(property.Computed),
	})
	checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: unknown})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.True(t, checked.Inputs.Get("workspaceId").IsComputed())
	preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	require.True(t, preview.Properties.Get("workspaceId").IsComputed())
	require.True(t, preview.Properties.Get("workspaceName").IsComputed())
	state := providertest.Props(map[string]any{"federationRuleId": "fdrl_audit", "workspaceId": "wrkspc_audit", "workspaceName": "Audit", "createdAt": "2026-01-01T00:00:00Z"})
	for _, field := range []string{"federationRuleId", "workspaceId"} {
		inputs := map[string]property.Value{"federationRuleId": property.New("fdrl_audit"), "workspaceId": property.New("wrkspc_audit")}
		inputs[field] = property.New("changed")
		diff, err := server.Diff(p.DiffRequest{Urn: urn, ID: "fdrl_audit/wrkspc_audit", State: state, Inputs: property.NewMap(inputs)})
		require.NoError(t, err)
		require.True(t, diff.HasChanges)
		require.Equal(t, p.UpdateReplace, diff.DetailedDiff[field].Kind)
		require.False(t, diff.DeleteBeforeReplace)
	}
	diff, err := server.Diff(p.DiffRequest{Urn: urn, ID: "fdrl_audit/wrkspc_audit", State: state, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: "fdrl_audit/wrkspc_audit", State: state, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("workspaceId").IsComputed())
	require.Empty(t, api.Requests(), "preview and diff must not contact the API")
}

// SDK v2.9.0 documents a null name on Add, populated by List.
func TestFederationRuleWorkspaceSDKResponseAndStableImport(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN(federationRuleWorkspaceToken)
	response := fakeapi.Object{"type": "federation_rule_workspace", "federation_rule_id": "fdrl_audit", "workspace_id": "wrkspc_audit", "workspace_name": nil, "created_at": "2026-01-01T00:00:00Z", "created_by_actor_id": "svac_actor"}
	api.Handle("POST "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		require.Equal(t, fakeapi.Object{"workspace_id": "wrkspc_audit"}, body)
		require.Equal(t, "Bearer oauth-test", r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(response)
	})
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: federationRuleWorkspaceInputs()})
	require.NoError(t, err, "%+v", created.PartialState)
	checked := federationRuleWorkspaceInputs()
	require.Equal(t, "fdrl_audit/wrkspc_audit", created.ID, "Add response has no generic id")
	require.True(t, created.Properties.Get("workspaceName").IsNull())
	require.False(t, providertest.Diff(t, server, urn, created, checked, checked).HasChanges,
		"identical inputs are stable before refresh too")
	api.Handle("GET "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		listed := fakeapi.Object{"type": "federation_rule_workspace", "federation_rule_id": "fdrl_audit", "workspace_id": "wrkspc_audit", "workspace_name": "Renamed", "created_at": "2026-01-01T00:00:00Z"}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{listed}, "next_page": nil})
	})
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, checked, imported.Inputs)
	require.Equal(t, "Renamed", imported.Properties.Get("workspaceName").AsString())
	withEmpty, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Properties: providertest.Props(map[string]any{"federationRuleId": "", "workspaceId": ""})})
	require.NoError(t, err)
	require.Equal(t, imported.Properties, withEmpty.Properties)
	check, err := server.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	diff := providertest.Diff(t, server, urn, p.CreateResponse{ID: imported.ID, Properties: imported.Properties}, imported.Inputs, checked)
	require.False(t, diff.HasChanges)
	before := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: imported.ID, State: imported.Properties, Inputs: checked})
	require.NoError(t, err)
	require.Equal(t, imported.Properties, updated.Properties)
	require.Len(t, api.Requests(), before, "no mutable fields; Update performs no I/O")
	refreshed := providertest.Read(t, server, urn, created, checked)
	require.Equal(t, checked, refreshed.Inputs)
	require.Equal(t, "Renamed", refreshed.Properties.Get("workspaceName").AsString())
}

// Failed observations are not absence. Delete uses physical identity.
func TestFederationRuleWorkspaceAbsenceErrorsAndDelete(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN(federationRuleWorkspaceToken)
	_, err := server.Read(p.ReadRequest{Urn: urn, ID: "fdrl_audit"})
	require.Error(t, err)
	require.Empty(t, api.Requests())
	api.Handle("GET "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"denied"}}`))
	})
	_, err = server.Read(p.ReadRequest{Urn: urn, ID: "fdrl_audit/wrkspc_audit"})
	require.Error(t, err, "access denial must propagate")
	missing, err := server.Read(p.ReadRequest{Urn: urn, ID: "fdrl_other/wrkspc_audit"})
	require.NoError(t, err)
	require.Empty(t, missing.ID)
	for range 2 {
		err = server.Delete(p.DeleteRequest{Urn: urn, ID: "fdrl_audit/wrkspc_audit", Properties: providertest.Props(map[string]any{"federationRuleId": "stale", "workspaceId": "stale", "workspaceName": "Audit", "createdAt": "2026-01-01T00:00:00Z"})})
		require.NoError(t, err)
	}
	api.LastRequest(t, http.MethodDelete, federationRuleWorkspacePath+"/wrkspc_audit")
}

// An unusable Add response returns an ordinary error, despite possible allocation.
func TestFederationRuleWorkspaceMalformedCreate(t *testing.T) {
	server, api := providertest.New(t)
	api.Handle("POST "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		api.Put(federationRuleWorkspacePath+"/wrkspc_audit", body)
		_, _ = w.Write([]byte(`{"workspace_id":"wrkspc_audit","workspace_name":42}`))
	})
	created, err := server.Create(p.CreateRequest{Urn: providertest.URN(federationRuleWorkspaceToken), Properties: federationRuleWorkspaceInputs()})
	require.Error(t, err)
	require.NotNil(t, api.Object(federationRuleWorkspacePath+"/wrkspc_audit"))
	require.Empty(t, created.ID)
	require.Nil(t, created.PartialState)
	require.Zero(t, created.Properties.Len())
	require.Len(t, api.Requests(), 1)
}

func TestFederationRuleWorkspaceMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(federationRuleWorkspaceToken)
	dep := resourceurn.URN("urn:pulumi:stack::project::pkg:index:Dependency::dep")
	for _, known := range []bool{true, false} {
		for _, secret := range []bool{true, false} {
			v := property.New("wrkspc_one")
			if !known {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{dep})
			inputs := property.NewMap(map[string]property.Value{"federationRuleId": property.New("fdrl_one"), "workspaceId": v})
			checked, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
			twice, err := s.Check(p.CheckRequest{Urn: u, Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Equal(t, checked, twice)
			preview, err := s.Create(p.CreateRequest{Urn: u, Properties: inputs, DryRun: true})
			require.NoError(t, err)
			requireOutputValue(t, v, preview.Properties.Get("workspaceId"))
			require.Equal(t, inputs, checked.Inputs, "inputs are not mutated")
		}
	}
	for _, value := range []string{"", "a/b"} {
		inputs := providertest.Props(map[string]any{"federationRuleId": value, "workspaceId": "wrkspc_one"})
		checked, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
		require.Equal(t, "federationRuleId", checked.Failures[0].Property)
	}
	require.Empty(t, api.Requests())
	api.Handle("GET /v1/organizations/federation_rules/fdrl_one/workspaces", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"type":"federation_rule_workspace","federation_rule_id":"fdrl_one","workspace_id":"wrkspc_one","workspace_name":"Test","created_at":"2026-01-01T00:00:00Z"}],"next_page":null}`))
	})
	v := property.New("wrkspc_one").WithSecret(true).WithDependencies([]resourceurn.URN{dep})
	inputs := property.NewMap(map[string]property.Value{"federationRuleId": property.New("fdrl_one"), "workspaceId": v})
	read, err := s.Read(p.ReadRequest{Urn: u, ID: "fdrl_one/wrkspc_one", Inputs: inputs, Properties: inputs})
	require.NoError(t, err)
	requireOutputValue(t, v, read.Properties.Get("workspaceId"))
	require.Equal(t, v, read.Inputs.Get("workspaceId"))
	require.Equal(t, inputs.Get("workspaceId"), v)
}

func TestFederationRuleWorkspaceReadFailures(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null}`, `{"data":{}}`, `{"data":[{}]}`, `{"data":[{"type":"federation_rule_workspace","federation_rule_id":"foreign","workspace_id":"wrkspc_audit"}]}`, `{"data":[{"type":"federation_rule_workspace","federation_rule_id":"fdrl_audit","workspace_id":"wrkspc_audit","workspace_name":42,"created_at":"2026-01-01T00:00:00Z"}]}`} {
		t.Run(body, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			_, err := s.Read(p.ReadRequest{Urn: providertest.URN(federationRuleWorkspaceToken), ID: "fdrl_audit/wrkspc_audit"})
			require.Error(t, err)
		})
	}
}

// SDK decoding coerces these primitives to strings. Test matching coerced IDs
// as well as unmatched malformed rows, which must never establish absence.
func TestFederationRuleWorkspaceIdentityTypes(t *testing.T) {
	for _, field := range []string{"federation_rule_id", "workspace_id"} {
		for _, primitive := range []string{"42", "true", "false"} {
			for _, operation := range []string{"add", "read-matching", "read-unmatched"} {
				t.Run(field+"/"+primitive+"/"+operation, func(t *testing.T) {
					rule, workspace := "fdrl_audit", "wrkspc_audit"
					if field == "federation_rule_id" {
						rule = primitive
					} else if operation != "read-unmatched" {
						workspace = primitive
					}
					row := map[string]any{"type": "federation_rule_workspace", "federation_rule_id": rule, "workspace_id": workspace, "workspace_name": "Untrusted", "created_at": "2026-01-01T00:00:00Z"}
					row[field] = json.RawMessage(primitive)
					body, err := json.Marshal(row)
					require.NoError(t, err)
					s, api := providertest.New(t)
					path := fmt.Sprintf("/v1/organizations/federation_rules/%s/workspaces", rule)
					method := "GET"
					if operation == "add" {
						method = "POST"
					} else {
						body = append(append([]byte(`{"data":[`), body...), []byte(`],"next_page":null}`)...)
					}
					api.Handle(method+" "+path, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(body)
					})
					id := rule + "/" + workspace
					if operation != "add" {
						_, err := s.Read(p.ReadRequest{Urn: providertest.URN(federationRuleWorkspaceToken), ID: id})
						require.Error(t, err, "malformed identity must not be accepted or reported absent")
						return
					}
					created, err := s.Create(p.CreateRequest{Urn: providertest.URN(federationRuleWorkspaceToken), Properties: providertest.Props(map[string]any{"federationRuleId": rule, "workspaceId": workspace})})
					require.Error(t, err)
					require.Empty(t, created.ID)
					require.Nil(t, created.PartialState)
					require.Zero(t, created.Properties.Len())
				})
			}
		}
	}
}

func TestFederationRuleWorkspaceAddFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"rejected", `{"type":"error","error":{"type":"permission_error","message":"denied"}}`, 403},
		{"malformed-json", `{`, 200},
		{"foreign", `{"type":"federation_rule_workspace","federation_rule_id":"foreign","workspace_id":"wrkspc_audit","workspace_name":"Foreign","created_at":"2026-01-01T00:00:00Z"}`, 200},
		{"invalid-name", `{"type":"federation_rule_workspace","federation_rule_id":"fdrl_audit","workspace_id":"wrkspc_audit","workspace_name":42,"created_at":"2026-01-01T00:00:00Z"}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("POST "+federationRuleWorkspacePath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			created, err := s.Create(p.CreateRequest{Urn: providertest.URN(federationRuleWorkspaceToken), Properties: federationRuleWorkspaceInputs()})
			require.Error(t, err)
			require.Empty(t, created.ID)
			require.Nil(t, created.PartialState)
			require.Zero(t, created.Properties.Len())
			require.Len(t, api.Requests(), 1)
		})
	}
}
