// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package identity_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	resourceurn "github.com/pulumi/pulumi/sdk/v3/go/common/resource/urn"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// Custom resources are dependency-graph nodes. Outputs retain data, knownness
// and secrecy, but never inherit the input's dependency annotations. An ancestor
// secret protects its descendants; exact secret wrapper placement is not required.
func requireOutputValue(t *testing.T, want, got property.Value) {
	t.Helper()
	requireOutputValueAt(t, want, got, false)
}

func requireOutputValueAt(t *testing.T, want, got property.Value, protected bool) {
	t.Helper()
	require.Empty(t, got.Dependencies())
	protected = protected || got.Secret()
	if want.Secret() {
		require.True(t, protected, "output must protect secret input")
	}
	switch {
	case want.IsMap():
		require.True(t, got.IsMap(), "expected map, got %v", got)
		require.Equal(t, want.AsMap().Len(), got.AsMap().Len())
		for k, v := range want.AsMap().All {
			requireOutputValueAt(t, v, got.AsMap().Get(k), protected)
		}
	case want.IsArray():
		require.True(t, got.IsArray(), "expected array, got %v", got)
		require.Equal(t, want.AsArray().Len(), got.AsArray().Len())
		for i, v := range want.AsArray().All {
			requireOutputValueAt(t, v, got.AsArray().Get(i), protected)
		}
	default:
		require.Equal(t, want.WithSecret(false).WithDependencies(nil), got.WithSecret(false).WithDependencies(nil))
	}
}

func requireOutputMap(t *testing.T, want, got property.Map) {
	t.Helper()
	requireOutputValue(t, property.New(want), property.New(got))
}

func requireNoOutputDependencies(t *testing.T, got property.Value) {
	t.Helper()
	require.Empty(t, got.Dependencies())
	if got.IsMap() {
		for _, v := range got.AsMap().All {
			requireNoOutputDependencies(t, v)
		}
	}
	if got.IsArray() {
		for _, v := range got.AsArray().All {
			requireNoOutputDependencies(t, v)
		}
	}
}

// Add synthetic dependencies at every level, including containers and empty
// values. This exercises both explicit forwarding and implicit whole-value copies.
func withOutputTestDependencies(v property.Value) property.Value {
	if v.IsMap() {
		m := v.AsMap()
		for k, child := range m.All {
			m = m.Set(k, withOutputTestDependencies(child))
		}
		v = property.WithGoValue(v, m)
	}
	if v.IsArray() {
		values := v.AsArray().AsSlice()
		for i, child := range values {
			values[i] = withOutputTestDependencies(child)
		}
		v = property.WithGoValue(v, property.NewArray(values))
	}
	return v.WithDependencies([]resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::synthetic"})
}

func TestIdentityOutputMetadataLifecycle(t *testing.T) {
	for _, tc := range []struct {
		token   string
		inputs  map[string]any
		setup   func(*fakeapi.Server)
		mutable string
		next    any
	}{
		{federationIssuerToken, map[string]any{
			"name": "issuer", "issuerUrl": "https://issuer.example", "checkJti": false,
			"jwks": map[string]any{"type": "inline", "keys": []any{map[string]any{"kty": "RSA", "n": "abc", "e": "AQAB"}}},
		}, federationIssuerServerBehavior, "issuerUrl", "https://updated.example"},
		{federationRuleToken, func() map[string]any {
			in := rule("repo:acme/app:*")
			in["description"] = "CI"
			in["appliesToAllWorkspaces"] = false
			return in
		}(), federationRuleServerBehavior, "description", ""},
		{federationRuleWorkspaceToken, binding("wrkspc_1"), ruleBindingServerBehavior, "", nil},
		{rbacGroupToken, map[string]any{"name": "Platform"}, func(api *fakeapi.Server) {
			api.SetHooks("rbac_groups", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
				obj["source_type"] = "direct"
				obj["role_ids"] = []any{}
			}})
		}, "name", "Engineering"},
		{rbacGroupMemberToken, map[string]any{"rbacGroupId": "rbac_group_1", "userId": "user_1"}, memberListServerBehavior, "", nil},
		{serviceAccountToken, map[string]any{"name": "ci", "description": "CI"}, serviceAccountServerBehavior, "description", ""},
		{serviceAccountWorkspaceToken, membership("workspace_user"), func(api *fakeapi.Server) {
			api.Handle("POST "+membershipCollection, func(w http.ResponseWriter, _ *http.Request, b fakeapi.Object) {
				row := sawRow(b["workspace_role"])
				api.Put(membershipPath, row)
				fakeapi.WriteJSON(w, row)
			})
			api.Handle("POST "+membershipPath, func(w http.ResponseWriter, _ *http.Request, b fakeapi.Object) {
				row := sawRow(b["workspace_role"])
				api.Put(membershipPath, row)
				fakeapi.WriteJSON(w, row)
			})
		}, "workspaceRole", "workspace_admin"},
	} {
		for _, secret := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/secret=%t", tc.token, secret), func(t *testing.T) {
				server, api := providertest.New(t)
				tc.setup(api)
				u := providertest.URN(tokens.Type(tc.token))
				inputs := providertest.Props(tc.inputs)
				for k, v := range inputs.All {
					if secret && v.IsMap() {
						// Exercise nested secrecy without requiring exact leaf wrappers.
						m := v.AsMap()
						for key, child := range m.All {
							m = m.Set(key, child.WithSecret(true))
						}
						v = property.New(m)
					} else {
						v = v.WithSecret(secret)
					}
					inputs = inputs.Set(k, withOutputTestDependencies(v))
				}
				checked, err := server.Check(p.CheckRequest{Urn: u, Inputs: inputs})
				require.NoError(t, err)
				require.Empty(t, checked.Failures)
				require.Equal(t, inputs, checked.Inputs)
				preview, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs, DryRun: true})
				require.NoError(t, err)
				requireNoOutputDependencies(t, property.New(preview.Properties))
				for _, field := range map[string][]string{
					federationIssuerToken:        {"createdAt", "updatedAt", "pollStatus", "jwksPollingDisabledAt"},
					federationRuleToken:          {"issuerName", "createdAt", "updatedAt", "workspaceIds"},
					federationRuleWorkspaceToken: {"workspaceName", "createdAt"},
					rbacGroupToken:               {"sourceType", "createdAt", "updatedAt", "roleIds"},
					rbacGroupMemberToken:         {"email", "createdAt"},
					serviceAccountToken:          {"createdAt", "updatedAt"},
				}[tc.token] {
					require.True(t, preview.Properties.Get(field).IsComputed(), "%s preview observation is unknown", field)
				}
				require.Empty(t, api.Requests())

				created, err := server.Create(p.CreateRequest{Urn: u, Properties: checked.Inputs})
				require.NoError(t, err)
				require.NotEmpty(t, created.ID)
				requireNoOutputDependencies(t, property.New(created.Properties))
				for k, v := range inputs.All {
					requireOutputValue(t, v, created.Properties.Get(k))
				}

				imported, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID})
				require.NoError(t, err)
				require.Equal(t, created.ID, imported.ID)
				requireNoOutputDependencies(t, property.New(imported.Properties))
				if tc.token == federationRuleWorkspaceToken || tc.token == serviceAccountWorkspaceToken {
					// These narrow Read adapters also protect secret inputs when
					// no prior output state exists. Input dependencies remain inputs.
					inputOnly, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: checked.Inputs})
					require.NoError(t, err)
					require.Equal(t, checked.Inputs, inputOnly.Inputs)
					requireNoOutputDependencies(t, property.New(inputOnly.Properties))
					for k, v := range inputs.All {
						requireOutputValue(t, v, inputOnly.Properties.Get(k))
					}
				}

				// Deliberately taint old output-only values too: structural/no-op
				// updates must not implicitly carry old raw dependency metadata.
				state := withOutputTestDependencies(property.New(created.Properties)).AsMap()
				read, err := server.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: checked.Inputs, Properties: state})
				require.NoError(t, err)
				require.Equal(t, created.ID, read.ID)
				requireNoOutputDependencies(t, property.New(read.Properties))
				for k, v := range inputs.All {
					requireOutputValue(t, v, read.Properties.Get(k))
				}

				desired := checked.Inputs
				if tc.mutable != "" {
					desired = desired.Set(tc.mutable, withOutputTestDependencies(providertest.Props(map[string]any{"next": tc.next}).Get("next")).WithSecret(secret))
				}
				state = withOutputTestDependencies(property.New(read.Properties)).AsMap()
				updatePreview, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: state, OldInputs: checked.Inputs, Inputs: desired, DryRun: true})
				require.NoError(t, err)
				requireNoOutputDependencies(t, property.New(updatePreview.Properties))
				if tc.mutable == "" {
					requireOutputMap(t, state, updatePreview.Properties)
				}
				before := len(api.Requests())
				updated, err := server.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: state, OldInputs: checked.Inputs, Inputs: desired})
				require.NoError(t, err)
				requireNoOutputDependencies(t, property.New(updated.Properties))
				for k, v := range desired.All {
					requireOutputValue(t, v, updated.Properties.Get(k))
				}
				if tc.mutable == "" {
					require.Len(t, api.Requests(), before, "structural bindings have no mutable API operation")
				} else {
					require.Len(t, api.Requests(), before+1, "Update sends desired state without a pre-read")
				}
				require.Equal(t, inputs, checked.Inputs, "lifecycle calls do not mutate checked inputs")
			})
		}
	}
}
