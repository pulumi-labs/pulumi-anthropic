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
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Compare data and knownness exactly, but accept an ancestor secret protecting a
// secret descendant. Custom-resource outputs must not inherit graph annotations.
func requireOutputValue(t *testing.T, want, got property.Value) {
	t.Helper()
	require.Equal(t, organizationPlainValue(want), organizationPlainValue(got))
	var check func(property.Value, property.Value, bool)
	check = func(want, got property.Value, protected bool) {
		t.Helper()
		require.Empty(t, got.Dependencies(), "custom-resource output dependencies")
		protected = protected || got.Secret()
		if want.Secret() {
			require.True(t, protected, "secret value must be protected")
		}
		if want.IsMap() {
			for k, v := range want.AsMap().All {
				check(v, got.AsMap().Get(k), protected)
			}
		} else if want.IsArray() {
			for i, v := range want.AsArray().All {
				check(v, got.AsArray().Get(i), protected)
			}
		}
	}
	check(want, got, false)
}

func requireNoOutputDependencies(t *testing.T, v property.Value) {
	t.Helper()
	require.Empty(t, v.Dependencies())
	if v.IsMap() {
		for _, child := range v.AsMap().All {
			requireNoOutputDependencies(t, child)
		}
	} else if v.IsArray() {
		for _, child := range v.AsArray().All {
			requireNoOutputDependencies(t, child)
		}
	}
}

func organizationDependencyValue(v property.Value) property.Value {
	if v.IsMap() {
		m := v.AsMap()
		for k, child := range m.All {
			m = m.Set(k, organizationDependencyValue(child))
		}
		v = property.WithGoValue(v, m)
	} else if v.IsArray() {
		a := v.AsArray().AsSlice()
		for i := range a {
			a[i] = organizationDependencyValue(a[i])
		}
		v = property.WithGoValue(v, property.NewArray(a))
	}
	return v.WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::synthetic"})
}

func TestOrganizationCustomResourceOutputDependencies(t *testing.T) {
	for _, tc := range []struct {
		name         string
		setup        func(*fakeapi.Server)
		inputs, next map[string]any
	}{
		{"Workspace", workspaceServerBehavior, baseWorkspace(), map[string]any{"name": "updated", "tags": map[string]any{"new": ""}}},
		{"User", seedUser, map[string]any{"userId": "user_1", "role": "developer"}, map[string]any{"userId": "user_1", "role": "billing"}},
		{"WorkspaceMember", memberServerBehavior, member("workspace_user"), member("workspace_admin")},
		{"Invite", func(api *fakeapi.Server) {
			api.SetHooks("invites", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
				obj["status"] = "pending"
				obj["invited_at"] = "2026-01-01T00:00:00Z"
				obj["expires_at"] = "2026-01-22T00:00:00Z"
			}})
		}, map[string]any{"email": "metadata@example.invalid", "role": "user", "rbacGroupIds": []any{"group_a", "group_b"}}, nil},
		{"SpendLimit", spendLimitServerBehavior, spendLimit("1", "monthly"), spendLimit("0", "monthly")},
		{"ComplianceSettings", func(api *fakeapi.Server) { complianceServerBehavior(api) }, map[string]any{"enabled": true}, map[string]any{"enabled": false}},
		{"ApiKey", seedApiKey, map[string]any{"apiKeyId": "apikey_1", "name": "created", "status": "active"}, map[string]any{"apiKeyId": "apikey_1", "name": "updated", "status": "inactive"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, api := providertest.New(t)
			tc.setup(api)
			u := urn(tc.name)
			markInputs := func(values map[string]any) property.Map {
				m := providertest.Props(values)
				for k, v := range m.All {
					m = m.Set(k, organizationDependencyValue(v).WithSecret(true))
				}
				return m
			}
			inputs := markInputs(tc.inputs)
			checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs, "Check retains input graph metadata")
			preview, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs, DryRun: true})
			require.NoError(t, err)
			requireNoOutputDependencies(t, property.New(preview.Properties))
			created, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs})
			require.NoError(t, err)
			require.NotEmpty(t, created.ID)
			require.Nil(t, created.PartialState)
			requireNoOutputDependencies(t, property.New(created.Properties))
			for k, v := range inputs.All {
				requireOutputValue(t, v, created.Properties.Get(k))
			}
			// Legacy state may already contain dependencies. Read and Update must
			// re-encode observations rather than return these whole raw values.
			state := organizationDependencyValue(property.New(created.Properties)).AsMap()
			read, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID, Properties: state, Inputs: inputs})
			require.NoError(t, err)
			require.Equal(t, created.ID, read.ID)
			requireOutputValue(t, property.New(created.Properties), property.New(read.Properties))
			imported, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID})
			require.NoError(t, err)
			require.Equal(t, created.ID, imported.ID)
			requireNoOutputDependencies(t, property.New(imported.Properties))
			next := inputs
			if tc.next != nil {
				next = markInputs(tc.next)
			}
			for _, dryRun := range []bool{true, false} {
				updated, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: state, OldInputs: inputs, Inputs: next, DryRun: dryRun})
				require.NoError(t, err)
				require.Nil(t, updated.PartialState)
				requireNoOutputDependencies(t, property.New(updated.Properties))
				for k, v := range next.All {
					requireOutputValue(t, v, updated.Properties.Get(k))
				}
			}
		})
	}
}

func organizationPlainValue(v property.Value) property.Value {
	if v.IsMap() {
		m := v.AsMap()
		for k, child := range m.All {
			m = m.Set(k, organizationPlainValue(child))
		}
		v = property.WithGoValue(v, m)
	} else if v.IsArray() {
		a := v.AsArray().AsSlice()
		for i := range a {
			a[i] = organizationPlainValue(a[i])
		}
		v = property.WithGoValue(v, property.NewArray(a))
	}
	return v.WithSecret(false).WithDependencies(nil)
}
