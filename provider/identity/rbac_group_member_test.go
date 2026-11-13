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
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Contract: unresolved identity inputs survive Check and mutation-free previews.
func TestRbacGroupMemberPreview(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroupMember")
	inputs := property.NewMap(map[string]property.Value{
		"rbacGroupId": property.New(property.Computed), "userId": property.New(property.Computed),
	})
	checked, err := s.Check(p.CheckRequest{Urn: urn, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	for _, key := range []string{"rbacGroupId", "userId"} {
		require.True(t, checked.Inputs.Get(key).IsComputed())
	}
	preview, err := s.Create(p.CreateRequest{Urn: urn, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.Empty(t, preview.ID)
	for _, key := range []string{"rbacGroupId", "userId", "email", "createdAt"} {
		require.True(t, preview.Properties.Get(key).IsComputed(), key)
	}
	old := providertest.Props(map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1", "email": "ada@example.com", "createdAt": "2026-01-01T00:00:00Z"})
	diff, err := s.Diff(p.DiffRequest{Urn: urn, ID: "rbac_group_1/user_1", State: old, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.True(t, diff.HasChanges)
	require.Contains(t, []p.DiffKind{p.UpdateReplace, p.AddReplace, p.DeleteReplace}, diff.DetailedDiff["userId"].Kind)
	updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: "rbac_group_1/user_1", State: old, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("userId").IsComputed())
	require.Empty(t, api.Requests())
}

// Contract: SDK-shaped responses (no synthetic id), ID-only import, output drift,
// stable no-op updates, structural replacement, and idempotent removal.
func TestRbacGroupMemberImportStability(t *testing.T) {
	s, api := providertest.New(t)
	urn := providertest.URN("anthropic:identity:RbacGroupMember")
	const path = "/v1/organizations/rbac_groups/rbac_group_1/members"
	member := fakeapi.Object{"user_id": "user_1", "rbac_group_id": "rbac_group_1", "type": "rbac_group_member", "email": "ada@example.com", "created_at": "2026-01-01T00:00:00Z"}
	api.Handle("POST "+path, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"user_id": "user_1"}, body)
		require.Equal(t, "sk-ant-admin-test", r.Header.Get("x-api-key"))
		fakeapi.WriteJSON(w, member)
	})
	api.Handle("GET "+path, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{member}, "next_page": nil})
	})
	inputs := providertest.Props(map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"})
	created, checked := providertest.Create(t, s, urn, inputs)
	require.Equal(t, "rbac_group_1/user_1", created.ID)
	imported, err := s.Read(p.ReadRequest{Urn: urn, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, checked, imported.Inputs)
	check, err := s.Check(p.CheckRequest{Urn: urn, Inputs: imported.Inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	require.False(t, providertest.Diff(t, s, urn, created, checked, imported.Inputs).HasChanges)
	before := len(api.Requests())
	for range 2 {
		updated, err := s.Update(p.UpdateRequest{Urn: urn, ID: created.ID, State: created.Properties, Inputs: imported.Inputs, OldInputs: checked})
		require.NoError(t, err)
		require.Equal(t, created.Properties, updated.Properties)
	}
	require.Len(t, api.Requests(), before)
	member["email"] = "changed@example.com"
	refreshed := providertest.Read(t, s, urn, created, checked)
	require.Equal(t, "changed@example.com", refreshed.Properties.Get("email").AsString())
	require.Equal(t, checked, refreshed.Inputs)
	require.False(t, providertest.Diff(t, s, urn, p.CreateResponse{ID: refreshed.ID, Properties: refreshed.Properties}, checked, inputs).HasChanges)
	for _, field := range []string{"rbacGroupId", "userId"} {
		next := map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"}
		next[field] = "other_2"
		diff := providertest.Diff(t, s, urn, created, checked, providertest.Props(next))
		require.Equal(t, p.UpdateReplace, diff.DetailedDiff[field].Kind)
		require.False(t, diff.DeleteBeforeReplace)
	}
	// Failed deletes retain an error; already absent is accepted. Both use the ID,
	// not a potentially stale or mismatched state input.
	deleteStatus := http.StatusForbidden
	api.Handle("DELETE "+path+"/user_1", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, deleteStatus, "cannot remove")
	})
	err = s.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: created.Properties})
	require.Error(t, err)
	deleteStatus = http.StatusNotFound
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: urn, ID: created.ID, Properties: created.Properties}))
	api.LastRequest(t, http.MethodDelete, path+"/user_1")
}

// Nonabsence list failures must preserve tracking by returning an error.
func TestRbacGroupMemberReadFailure(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/organizations/rbac_groups/rbac_group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteError(w, status, "cannot list")
			})
			_, err := s.Read(p.ReadRequest{Urn: providertest.URN("anthropic:identity:RbacGroupMember"), ID: "rbac_group_1/user_1"})
			require.Error(t, err)
		})
	}
}

// A malformed successful Add returns an ordinary error without checkpoint state.
func TestRbacGroupMemberMalformedCreate(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/organizations/rbac_groups/rbac_group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"user_id": "user_1", "rbac_group_id": "rbac_group_1", "type": "rbac_group_member", "email": 123, "created_at": "2026-01-01T00:00:00Z"})
	})
	got, err := s.Create(p.CreateRequest{Urn: providertest.URN("anthropic:identity:RbacGroupMember"), Properties: providertest.Props(map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"})})
	require.Error(t, err)
	require.Nil(t, got.PartialState)
	require.Empty(t, got.ID)
	require.Zero(t, got.Properties.Len())
	require.Len(t, api.Requests(), 1)
}

func TestRbacGroupMemberCheckMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(rbacGroupMemberToken)
	for _, v := range []property.Value{
		property.New("user_1"), property.New("user_1").WithSecret(true), property.New(property.Computed), property.New(property.Computed).WithSecret(true),
		property.New("user_1").WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::test:index:Thing::dep"}),
	} {
		bag := property.NewMap(map[string]property.Value{"rbacGroupId": property.New("group_1"), "userId": v})
		got, err := s.Check(p.CheckRequest{Urn: u, Inputs: bag})
		require.NoError(t, err)
		require.Empty(t, got.Failures)
		require.Equal(t, bag, got.Inputs)
		again, err := s.Check(p.CheckRequest{Urn: u, Inputs: got.Inputs})
		require.NoError(t, err)
		require.Equal(t, bag, again.Inputs)
	}
	for _, key := range []string{"rbacGroupId", "userId"} {
		for _, value := range []string{"", "bad/id"} {
			bag := providertest.Props(map[string]any{"rbacGroupId": "group_1", "userId": "user_1"}).Set(key, property.New(value))
			got, err := s.Check(p.CheckRequest{Urn: u, Inputs: bag})
			require.NoError(t, err)
			require.NotEmpty(t, got.Failures)
		}
	}
	require.Empty(t, api.Requests())
}

func TestRbacGroupMemberInvalidList(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":null,"next_page":null}`, `{"data":{},"next_page":null}`, `{"data":[],"next_page":12}`, `{"data":[{"user_id":"user_1","rbac_group_id":"foreign","type":"rbac_group_member"}],"next_page":null}`} {
		t.Run(body, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("GET /v1/organizations/rbac_groups/group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			_, err := s.Read(p.ReadRequest{Urn: providertest.URN(rbacGroupMemberToken), ID: "group_1/user_1"})
			require.Error(t, err)
		})
	}
}

func TestRbacGroupMemberMalformedCreateAndLegacyRefresh(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN(rbacGroupMemberToken)
	inputs := providertest.Props(map[string]any{"rbacGroupId": "group_1", "userId": "user_1"})
	api.Handle("POST /v1/organizations/rbac_groups/group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"rbac_group_id": "group_1", "user_id": "user_1", "type": "rbac_group_member", "email": 123, "created_at": "2026-01-01T00:00:00Z"})
	})
	created, err := s.Create(p.CreateRequest{Urn: u, Properties: inputs})
	require.Error(t, err)
	require.Nil(t, created.PartialState)
	require.Empty(t, created.ID)
	require.Zero(t, created.Properties.Len())
	partial := inputs
	require.Len(t, api.Requests(), 1)
	api.Handle("GET /v1/organizations/rbac_groups/group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"data": []any{fakeapi.Object{"rbac_group_id": "group_1", "user_id": "user_1", "type": "rbac_group_member", "email": "ada@example.com", "created_at": "2026-01-01T00:00:00Z"}}, "next_page": nil})
	})
	observed, err := s.Read(p.ReadRequest{Urn: u, ID: "group_1/user_1", Properties: partial, Inputs: inputs})
	require.NoError(t, err)
	require.Equal(t, "ada@example.com", observed.Properties.Get("email").AsString())
	updated, err := s.Update(p.UpdateRequest{Urn: u, ID: observed.ID, State: observed.Properties, Inputs: inputs, OldInputs: inputs})
	require.NoError(t, err)
	require.Equal(t, observed.Properties, updated.Properties)
	require.Len(t, api.Requests(), 2)
}

// SDK decoding can coerce primitive identities into strings. Such responses
// establish no output observations, even when the coerced identities match.
func TestRbacGroupMemberMalformedCreateIdentity(t *testing.T) {
	for _, field := range []string{"rbac_group_id", "user_id"} {
		for _, tc := range []struct {
			name  string
			raw   any
			input string
		}{
			{"numeric", 123, "123"}, {"boolean", true, "true"},
		} {
			t.Run(field+"/"+tc.name, func(t *testing.T) {
				s, api := providertest.New(t)
				group, user := "group_1", "user_1"
				if field == "rbac_group_id" {
					group = tc.input
				} else {
					user = tc.input
				}
				inputs := providertest.Props(map[string]any{"rbacGroupId": group, "userId": user})
				api.Handle("POST /v1/organizations/rbac_groups/"+group+"/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
					member := fakeapi.Object{"rbac_group_id": group, "user_id": user, "type": "rbac_group_member", "email": "ada@example.com", "created_at": "2026-01-01T00:00:00Z"}
					member[field] = tc.raw
					fakeapi.WriteJSON(w, member)
				})
				got, err := s.Create(p.CreateRequest{Urn: providertest.URN(rbacGroupMemberToken), Properties: inputs})
				require.Error(t, err)
				require.Nil(t, got.PartialState)
				require.Empty(t, got.ID)
				require.Zero(t, got.Properties.Len())
				require.Len(t, api.Requests(), 1)
			})
		}
	}
}

func TestRbacGroupMemberRejectedAdd(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/organizations/rbac_groups/group_1/members", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 403, "not permitted")
	})
	got, err := s.Create(p.CreateRequest{Urn: providertest.URN(rbacGroupMemberToken), Properties: providertest.Props(map[string]any{"rbacGroupId": "group_1", "userId": "user_1"})})
	require.Error(t, err)
	require.Empty(t, got.ID)
	require.Nil(t, got.PartialState)
}
