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
	"fmt"
	"net/http"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

const serviceAccountWorkspaceToken = "anthropic:identity:ServiceAccountWorkspace"

const membershipPath = "/v1/organizations/workspaces/wrkspc_1/service_accounts/svac_1"

const membershipCollection = "/v1/organizations/workspaces/wrkspc_1/service_accounts"

func membership(role any) map[string]any {
	return map[string]any{"workspaceId": "wrkspc_1", "serviceAccountId": "svac_1", "workspaceRole": role}
}

func sawRow(role any) fakeapi.Object {
	return fakeapi.Object{"type": "service_account_workspace_member", "workspace_id": "wrkspc_1", "service_account_id": "svac_1", "workspace_role": role, "implicit": false, "created_by_actor_id": "user_synthetic"}
}

func sawSetup(t *testing.T) (integration.Server, *fakeapi.Server) {
	server, api := providertest.New(t)
	api.Handle("POST "+membershipCollection, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, "svac_1", b["service_account_id"])
		row := sawRow(b["workspace_role"])
		api.Put(membershipPath, row)
		fakeapi.WriteJSON(w, row)
	})
	api.Handle("POST "+membershipPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Len(t, b, 1)
		row := sawRow(b["workspace_role"])
		api.Put(membershipPath, row)
		fakeapi.WriteJSON(w, row)
	})
	return server, api
}

func TestServiceAccountWorkspaceLifecycle(t *testing.T) {
	server, api := sawSetup(t)
	integration.LifeCycleTest{
		Resource: serviceAccountWorkspaceToken,
		Create:   integration.Operation{Inputs: providertest.Props(membership("workspace_user"))},
		Updates:  []integration.Operation{{Inputs: providertest.Props(membership("workspace_developer"))}},
	}.Run(t, server)
	require.Nil(t, api.Object(membershipPath))
}

func TestServiceAccountWorkspaceReadAndImport(t *testing.T) {
	server, api := sawSetup(t)
	urn := providertest.URN(serviceAccountWorkspaceToken)
	created, checked := providertest.Create(t, server, urn, providertest.Props(membership("workspace_user")))
	require.Equal(t, "wrkspc_1/svac_1", created.ID)
	require.Equal(t, fakeapi.Object{"service_account_id": "svac_1", "workspace_role": "workspace_user"}, api.LastRequest(t, "POST", membershipCollection).Body)
	imported, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, checked, imported.Inputs)
	twice, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, imported, twice)
	d, err := server.Diff(p.DiffRequest{Urn: urn, ID: created.ID, State: imported.Properties, OldInputs: checked, Inputs: checked})
	require.NoError(t, err)
	require.False(t, d.HasChanges)
	for _, role := range []string{"workspace_user", "workspace_developer"} {
		next := providertest.Props(membership(role))
		before := len(api.Requests())
		updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: imported.Properties, OldInputs: checked, Inputs: next})
		require.NoError(t, err)
		require.Equal(t, next, updated.Properties)
		require.Len(t, api.Requests(), before+1, "Update sends full desired role without pre-read or equality suppression")
		require.Equal(t, fakeapi.Object{"workspace_role": role}, api.LastRequest(t, "POST", membershipPath).Body)
	}
	api.Mutate(membershipPath, func(o fakeapi.Object) { o["workspace_role"] = "workspace_admin" })
	drift, err := server.Read(p.ReadRequest{Urn: urn, ID: created.ID, Inputs: checked, Properties: created.Properties})
	require.NoError(t, err)
	require.Equal(t, "workspace_admin", drift.Inputs.Get("workspaceRole").AsString())
	require.NoError(t, server.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: created.Properties}))
	api.LastRequest(t, "DELETE", membershipPath)
}

func TestServiceAccountWorkspaceValidationMetadataAndDiff(t *testing.T) {
	server, api := sawSetup(t)
	urn := providertest.URN(serviceAccountWorkspaceToken)
	old := providertest.Props(membership("workspace_user"))
	for _, k := range []string{"workspaceId", "serviceAccountId", "workspaceRole"} {
		for _, bad := range []string{"", "invalid/value"} {
			c, err := server.Check(p.CheckRequest{Urn: urn, Inputs: old.Set(k, property.New(bad))})
			require.NoError(t, err)
			require.NotEmpty(t, c.Failures)
			require.Equal(t, k, c.Failures[0].Property)
		}
	}
	c, err := server.Check(p.CheckRequest{Urn: urn, Inputs: old.Set("workspaceRole", property.New("workspace_billing"))})
	require.NoError(t, err)
	require.NotEmpty(t, c.Failures)
	for _, known := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New(property.Computed)
			if known {
				v = property.New("workspace_admin")
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::x:y:Z::dependency"})
			inputs := old.Set("workspaceRole", v)
			saved := inputs
			checked, err := server.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
			again, err := server.Check(p.CheckRequest{Urn: urn, Inputs: checked.Inputs})
			require.NoError(t, err)
			require.Equal(t, checked, again)
			preview, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs, DryRun: true})
			require.NoError(t, err)
			requireOutputMap(t, inputs, preview.Properties)
			update, err := server.Update(p.UpdateRequest{Urn: urn, ID: "wrkspc_1/svac_1", State: old, Inputs: inputs, DryRun: true})
			require.NoError(t, err)
			requireOutputMap(t, inputs, update.Properties)
			require.Equal(t, saved, inputs)
			if !known {
				d, err := server.Diff(p.DiffRequest{Urn: urn, ID: "wrkspc_1/svac_1", State: old, OldInputs: old, Inputs: inputs})
				require.NoError(t, err)
				require.True(t, d.HasChanges)
				require.Equal(t, p.Update, d.DetailedDiff["workspaceRole"].Kind)
			}
		}
	}
	for _, k := range []string{"workspaceId", "serviceAccountId"} {
		d, err := server.Diff(p.DiffRequest{Urn: urn, ID: "wrkspc_1/svac_1", State: old, OldInputs: old, Inputs: old.Set(k, property.New("different"))})
		require.NoError(t, err)
		require.Equal(t, p.UpdateReplace, d.DetailedDiff[k].Kind)
		require.False(t, d.DeleteBeforeReplace)
	}
	require.Empty(t, api.Requests())
}

func TestServiceAccountWorkspaceImplicitAndResponseValidation(t *testing.T) {
	for _, field := range []string{"implicit", "workspace_id", "service_account_id", "type", "workspace_role"} {
		t.Run(field, func(t *testing.T) {
			server, api := sawSetup(t)
			row := sawRow("workspace_user")
			row[field] = 42
			api.Put(membershipPath, row)
			_, err := server.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountWorkspaceToken), ID: "wrkspc_1/svac_1"})
			require.Error(t, err)
		})
	}
	server, api := sawSetup(t)
	urn := providertest.URN(serviceAccountWorkspaceToken)
	row := sawRow("workspace_user")
	row["implicit"] = true
	api.Put(membershipPath, row)
	_, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_1/svac_1"})
	require.ErrorContains(t, err, "implicit workspace membership cannot be imported")
	read, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_1/svac_1", Inputs: providertest.Props(membership("workspace_user")), Properties: providertest.Props(membership("workspace_user"))})
	require.NoError(t, err)
	require.Empty(t, read.ID)
}

func TestServiceAccountWorkspaceMalformedCreateAndLegacyRefresh(t *testing.T) {
	for _, bad := range []string{"role", "identity", "decode"} {
		t.Run(bad, func(t *testing.T) {
			server, api := providertest.New(t)
			api.Handle("POST "+membershipPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteJSON(w, sawRow(b["workspace_role"]))
			})
			urn := providertest.URN(serviceAccountWorkspaceToken)
			inputs := providertest.Props(membership("workspace_user"))
			api.Handle("POST "+membershipCollection, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				if bad == "decode" {
					w.Header().Set("Content-Type", "application/json")
					if _, err := fmt.Fprint(w, `{"workspace_role":`); err != nil {
						t.Error(err)
					}
					return
				}
				row := sawRow(17)
				if bad == "identity" {
					row = sawRow("workspace_admin")
					row["service_account_id"] = "svac_foreign"
				}
				fakeapi.WriteJSON(w, row)
			})
			created, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
			require.Error(t, err)
			require.Nil(t, created.PartialState)
			require.Empty(t, created.ID)
			require.Zero(t, created.Properties.Len())
			api.Put(membershipPath, sawRow("workspace_user"))
			legacy := inputs.Delete("workspaceRole")
			refreshed, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_1/svac_1", Properties: legacy, Inputs: inputs})
			require.NoError(t, err)
			require.Equal(t, inputs, refreshed.Properties)
			before := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: refreshed.ID, State: refreshed.Properties, OldInputs: inputs, Inputs: inputs})
			require.NoError(t, err)
			require.Nil(t, updated.PartialState)
			require.Equal(t, inputs, updated.Properties)
			require.Len(t, api.Requests(), before+1)
		})
	}
}

func TestServiceAccountWorkspaceValidationAndDrift(t *testing.T) {
	server, api := sawSetup(t)
	u := providertest.URN(serviceAccountWorkspaceToken)
	inputs := providertest.Props(membership("workspace_user"))
	for _, tc := range []struct{ key, value string }{{"workspaceId", ""}, {"workspaceId", "a/b"}, {"serviceAccountId", ""}, {"serviceAccountId", "a/b"}, {"workspaceRole", ""}, {"workspaceRole", "workspace_billing"}, {"workspaceRole", "unknown"}} {
		checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs.Set(tc.key, property.New(tc.value))})
		require.NoError(t, err)
		require.NotEmpty(t, checked.Failures)
		require.Equal(t, tc.key, checked.Failures[0].Property)
	}
	for _, k := range []string{"workspaceId", "serviceAccountId", "workspaceRole"} {
		next := inputs.Set(k, property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::x:y:Z::dependency"}))
		checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: next})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
		require.Equal(t, next, checked.Inputs)
	}
	require.Empty(t, api.Requests())
	api.Put(membershipPath, sawRow("workspace_admin"))
	observed, err := server.Read(p.ReadRequest{Urn: u, ID: "wrkspc_1/svac_1", Inputs: inputs, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, "workspace_admin", observed.Properties.Get("workspaceRole").AsString())
	require.Equal(t, observed.Inputs, observed.Properties)
	diff, err := server.Diff(p.DiffRequest{Urn: u, ID: observed.ID, State: observed.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Equal(t, p.Update, diff.DetailedDiff["workspaceRole"].Kind)
}

func TestServiceAccountWorkspaceRuntimeMetadata(t *testing.T) {
	server, _ := sawSetup(t)
	u := providertest.URN(serviceAccountWorkspaceToken)
	inputs := providertest.Props(membership("workspace_user"))
	for _, k := range []string{"workspaceId", "serviceAccountId", "workspaceRole"} {
		inputs = inputs.Set(k, inputs.Get(k).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::x:y:Z::dependency"}))
	}
	saved := inputs
	created, err := server.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.NoError(t, err)
	requireOutputMap(t, inputs, created.Properties)
	read, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: inputs, Properties: created.Properties})
	require.NoError(t, err)
	require.Equal(t, inputs, read.Inputs)
	requireOutputMap(t, inputs, read.Properties)
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, Inputs: inputs, State: created.Properties, OldInputs: inputs})
	require.NoError(t, err)
	requireOutputMap(t, inputs, updated.Properties)
	require.Equal(t, saved, inputs)
	requireOutputMap(t, inputs, created.Properties)
}

func TestServiceAccountWorkspaceUpdateMalformedResponse(t *testing.T) {
	server, api := providertest.New(t)
	u := providertest.URN(serviceAccountWorkspaceToken)
	old := providertest.Props(membership("workspace_user"))
	next := providertest.Props(membership("workspace_admin"))
	api.Handle("POST "+membershipPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"workspace_role": "workspace_admin"}, b)
		fakeapi.WriteJSON(w, sawRow(42))
	})
	updated, err := server.Update(p.UpdateRequest{Urn: u, ID: "wrkspc_1/svac_1", State: old, OldInputs: old, Inputs: next})
	require.Error(t, err)
	require.Nil(t, updated.PartialState)
	require.Zero(t, updated.Properties.Len())
	require.Len(t, api.Requests(), 1, "Update must not perform an internal Read")
	require.Equal(t, "workspace_user", old.Get("workspaceRole").AsString())
}

func TestServiceAccountWorkspaceErrorBoundaries(t *testing.T) {
	for _, status := range []int{400, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server, api := providertest.New(t)
			urn := providertest.URN(serviceAccountWorkspaceToken)
			_, err := server.Read(p.ReadRequest{Urn: urn, ID: "bad"})
			require.Error(t, err)
			require.Empty(t, api.Requests())
			for _, method := range []string{"GET", "DELETE"} {
				api.Handle(method+" "+membershipPath, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
					fakeapi.WriteError(w, status, "injected")
				})
			}
			read, err := server.Read(p.ReadRequest{Urn: urn, ID: "wrkspc_1/svac_1"})
			if status == 404 {
				require.NoError(t, err)
				require.Empty(t, read.ID)
			} else {
				require.Error(t, err)
			}
			err = server.Delete(p.DeleteRequest{Urn: urn, ID: "wrkspc_1/svac_1", Properties: providertest.Props(membership("workspace_user"))})
			if status == 404 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			api.Handle("POST "+membershipCollection, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteError(w, status, "injected")
			})
			created, err := server.Create(p.CreateRequest{Urn: urn, Properties: providertest.Props(membership("workspace_user"))})
			require.Error(t, err)
			require.Empty(t, created.ID)
			require.Nil(t, created.PartialState)
		})
	}
}

func TestServiceAccountWorkspaceMutationMayOmitImplicit(t *testing.T) {
	for _, shape := range []string{"absent", "null"} {
		t.Run(shape, func(t *testing.T) { testWorkspaceMutationImplicit(t, shape) })
	}
}

func testWorkspaceMutationImplicit(t *testing.T, shape string) {
	mutationRow := func(role any) fakeapi.Object {
		row := sawRow(role)
		if shape == "null" {
			row["implicit"] = nil
		} else {
			delete(row, "implicit")
		}
		return row
	}
	server, api := providertest.New(t)
	api.Handle("POST "+membershipCollection, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		row := mutationRow(body["workspace_role"])
		api.Put(membershipPath, row)
		fakeapi.WriteJSON(w, row)
	})
	api.Handle("POST "+membershipPath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		row := mutationRow(body["workspace_role"])
		api.Put(membershipPath, row)
		fakeapi.WriteJSON(w, row)
	})
	urn := providertest.URN(serviceAccountWorkspaceToken)
	inputs := providertest.Props(membership("workspace_user"))
	created, err := server.Create(p.CreateRequest{Urn: urn, Properties: inputs})
	require.NoError(t, err)
	require.Equal(t, "wrkspc_1/svac_1", created.ID)
	require.Equal(t, "workspace_user", created.Properties.Get("workspaceRole").AsString())
	require.Len(t, api.Requests(), 1)
	updated, err := server.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: providertest.Props(membership("workspace_developer"))})
	require.NoError(t, err)
	require.Equal(t, "workspace_developer", updated.Properties.Get("workspaceRole").AsString())
	require.Len(t, api.Requests(), 2, "no internal refresh to infer explicitness")
}

func TestServiceAccountWorkspaceReadRequiresImplicitFlag(t *testing.T) {
	for _, shape := range []string{"absent"} {
		t.Run(shape, func(t *testing.T) {
			server, api := sawSetup(t)
			row := sawRow("workspace_user")
			delete(row, "implicit")
			api.Put(membershipPath, row)
			_, err := server.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountWorkspaceToken), ID: "wrkspc_1/svac_1"})
			require.ErrorContains(t, err, "invalid workspace grant implicit flag")
		})
	}
}

func TestServiceAccountWorkspaceReadWithNullImplicit(t *testing.T) {
	server, api := sawSetup(t)
	row := sawRow("workspace_user")
	row["implicit"] = nil
	api.Put(membershipPath, row)
	result, err := server.Read(p.ReadRequest{Urn: providertest.URN(serviceAccountWorkspaceToken), ID: "wrkspc_1/svac_1"})
	require.NoError(t, err)
	require.Equal(t, "wrkspc_1/svac_1", result.ID)
	require.Equal(t, "workspace_user", result.Properties.Get("workspaceRole").AsString())
	require.Len(t, api.Requests(), 1)
}
