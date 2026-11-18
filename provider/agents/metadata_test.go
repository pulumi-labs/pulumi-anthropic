// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents_test

import (
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

type outputMetadataCase struct {
	name, key string
	setup     func(*fakeapi.Server)
	inputs    func(*testing.T) property.Map
}

func outputMetadataCases(t *testing.T) []outputMetadataCase {
	return []outputMetadataCase{
		{"Deployment", "name", deploymentServerBehavior, func(*testing.T) property.Map {
			return providertest.Props(baseDeployment())
		}},
		{"MemoryStore", "name", memoryStoreDefaults, func(*testing.T) property.Map {
			return providertest.Props(map[string]any{"name": "notes"})
		}},
		{"Vault", "displayName", func(*fakeapi.Server) {}, func(*testing.T) property.Map {
			return providertest.Props(map[string]any{"displayName": "vault", "metadata": map[string]any{"owner": "test"}})
		}},
		{"Environment", "name", environmentServerBehavior, func(*testing.T) property.Map {
			return providertest.Props(baseEnvironment())
		}},
		{"UserProfile", "name", func(api *fakeapi.Server) {
			observed := profileObject(t, profileJSON)
			for _, route := range []string{"POST /v1/user_profiles", "GET /v1/user_profiles/uprof_test"} {
				api.Handle(route, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
					fakeapi.WriteJSON(w, observed)
				})
			}
			api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
				updated := profileObject(t, profileJSON)
				updated["name"] = body["name"]
				fakeapi.WriteJSON(w, updated)
			})
		}, func(*testing.T) property.Map {
			return providertest.Props(map[string]any{"name": "Acme"})
		}},
		{"Tunnel", "displayName", tunnelServerBehavior, func(*testing.T) property.Map {
			return providertest.Props(map[string]any{"displayName": "tunnel"})
		}},
		{"TunnelCertificate", "caCertificatePem", func(api *fakeapi.Server) {
			for _, route := range []string{"POST /v1/tunnels/tnl_a/certificates", "GET /v1/tunnels/tnl_a/certificates/tcrt_a"} {
				api.Handle(route, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
					fakeapi.WriteJSON(w, certificateObject())
				})
			}
		}, certificateInputs},
		{"VaultCredential", "displayName", credentialServerBehavior, func(*testing.T) property.Map {
			return providertest.Props(bearerCredential("https://mcp.example.com", "synthetic-token"))
		}},
	}
}

// Read must preserve both configured secrecy and secrecy applied to old outputs
// (for example, additionalSecretOutputs), without treating old data as observations.
func TestReadPreservesUserSecretMetadata(t *testing.T) {
	for _, tc := range outputMetadataCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			tc.setup(api)
			u := providertest.URN(tokens.Type("anthropic:agents:" + tc.name))
			inputs := tc.inputs(t)
			inputs = inputs.Set(tc.key, inputs.Get(tc.key).WithSecret(true))
			created, checked := providertest.Create(t, s, u, inputs)
			require.True(t, created.Properties.Get(tc.key).Secret(), "Create must protect the configured value")
			created.Properties = created.Properties.Set("createdAt", created.Properties.Get("createdAt").WithSecret(true))
			read, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: checked, Properties: created.Properties})
			require.NoError(t, err)
			require.Equal(t, created.ID, read.ID)
			require.True(t, read.Inputs.Get(tc.key).Secret(), "Read inputs must preserve configured secrecy")
			require.True(t, read.Properties.Get(tc.key).Secret(), "Read outputs must preserve configured secrecy")
			require.True(t, read.Properties.Get("createdAt").Secret(), "Read must preserve old output-only secrecy")
			require.Equal(t, inputs.Get(tc.key).AsString(), read.Inputs.Get(tc.key).AsString())
			require.Equal(t, inputs.Get(tc.key).AsString(), read.Properties.Get(tc.key).AsString())
		})
	}
}

func TestCustomResourceAppliedOutputDependencies(t *testing.T) {
	deps := []resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::synthetic"}
	// Annotate parents and leaves to catch implicit whole-value/map copies as
	// well as explicit mergers. These are local loopback tests, not API evidence.
	var annotate func(property.Value) property.Value
	annotate = func(v property.Value) property.Value {
		if v.IsMap() {
			m := v.AsMap()
			for k, x := range m.All {
				m = m.Set(k, annotate(x))
			}
			v = property.WithGoValue(v, m)
		} else if v.IsArray() {
			xs := v.AsArray().AsSlice()
			for i, x := range xs {
				xs[i] = annotate(x)
			}
			v = property.WithGoValue(v, xs)
		}
		return v.WithDependencies(deps)
	}
	for _, tc := range outputMetadataCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			tc.setup(api)
			u := providertest.URN(tokens.Type("anthropic:agents:" + tc.name))
			inputs := annotate(property.New(tc.inputs(t))).AsMap()
			inputs = inputs.Set(tc.key, inputs.Get(tc.key).WithSecret(true))
			created, checked := providertest.Create(t, s, u, inputs)
			require.NotEmpty(t, created.ID)
			require.Equal(t, deps, checked.Get(tc.key).Dependencies(), "Check must retain input dependencies")
			again, err := s.Check(p.CheckRequest{Urn: u, Inputs: checked})
			require.NoError(t, err)
			require.Empty(t, again.Failures)
			require.Equal(t, checked, again.Inputs)
			requireNoOutputDependencies(t, property.New(created.Properties))
			requireOutputValue(t, checked.Get(tc.key), created.Properties.Get(tc.key))

			preview, err := s.Create(p.CreateRequest{Urn: u, Properties: checked, DryRun: true})
			require.NoError(t, err)
			requireNoOutputDependencies(t, property.New(preview.Properties))
			requireOutputValue(t, checked.Get(tc.key), preview.Properties.Get(tc.key))

			// Read must not forward legacy state dependencies either. Keep an old
			// output-only secret, independently of input dependency annotations.
			state := annotate(property.New(created.Properties)).AsMap()
			state = state.Set("createdAt", state.Get("createdAt").WithSecret(true))
			read, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: checked, Properties: state})
			require.NoError(t, err)
			require.Equal(t, created.ID, read.ID)
			requireNoOutputDependencies(t, property.New(read.Properties))
			requireOutputValue(t, checked.Get(tc.key), read.Properties.Get(tc.key))
			require.True(t, read.Properties.Get("createdAt").Secret())

			desired := checked
			// Tunnel and TunnelCertificate have no platform update operation;
			// exercise their explicit no-op Update adapters with legacy state.
			mutable := tc.name != "Tunnel" && tc.name != "TunnelCertificate"
			if mutable {
				desired = desired.Set(tc.key, property.New(checked.Get(tc.key).AsString()+" updated").WithSecret(true).WithDependencies(deps))
			}
			oldState := annotate(property.New(read.Properties)).AsMap()
			updateReq := p.UpdateRequest{Urn: u, ID: created.ID, State: oldState, OldInputs: checked, Inputs: desired, DryRun: true}
			planned, err := s.Update(updateReq)
			require.NoError(t, err)
			requireNoOutputDependencies(t, property.New(planned.Properties))
			requireOutputValue(t, desired.Get(tc.key), planned.Properties.Get(tc.key))
			updateReq.DryRun = false
			updated, err := s.Update(updateReq)
			require.NoError(t, err)
			requireNoOutputDependencies(t, property.New(updated.Properties))
			requireOutputValue(t, desired.Get(tc.key), updated.Properties.Get(tc.key))

			imported, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID})
			require.NoError(t, err)
			require.Equal(t, created.ID, imported.ID)
			requireNoOutputDependencies(t, property.New(imported.Properties))
			if tc.name == "Tunnel" {
				require.True(t, created.Properties.Get("tunnelToken").Secret())
				require.True(t, imported.Properties.Get("tunnelToken").Secret(), "ID-only Read must mark newly revealed tokens")
			}
		})
	}
}
