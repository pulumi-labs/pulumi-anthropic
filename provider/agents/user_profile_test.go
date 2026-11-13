// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents_test

import (
	"encoding/json"
	"net/http"
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

func TestUserProfileReadProtectsInputOnlySecrets(t *testing.T) {
	s, api := providertest.New(t)
	observed := profileObject(t, profileJSON)
	for _, route := range []string{"POST /v1/user_profiles", "GET /v1/user_profiles/uprof_test"} {
		api.Handle(route, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	}
	u := providertest.URN("anthropic:agents:UserProfile")
	created, inputs := providertest.Create(t, s, u, providertest.Props(map[string]any{"name": "Acme"}))
	inputs = inputs.Set("name", inputs.Get("name").WithSecret(true).WithDependencies([]resourceurn.URN{"urn:input"}))
	for _, state := range []property.Map{{}, created.Properties} {
		read, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID, Inputs: inputs, Properties: state})
		require.NoError(t, err)
		require.Equal(t, "Acme", read.Properties.Get("name").AsString())
		require.True(t, read.Properties.Get("name").Secret(), "public/missing old state must not expose a secret input")
		require.True(t, read.Inputs.Get("name").Secret())
		requireNoOutputDependencies(t, property.New(read.Properties))
	}
	require.False(t, created.Properties.Get("name").Secret())
}

const profileJSON = `{"id":"uprof_test","type":"user_profile","name":"Acme","access_type":"passthrough","external_id":"acme-1","external_user_onboarded_at":null,"metadata":{"plan":"pro","region":"us"},"trust_grants":{"synthetic":{"status":"pending"}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

func profileObject(t *testing.T, raw string) fakeapi.Object {
	t.Helper()
	var o fakeapi.Object
	require.NoError(t, json.Unmarshal([]byte(raw), &o))
	return o
}

func TestUserProfileCheckMetadataAndValidity(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	for _, in := range []map[string]any{{"accessType": "wrong"}, {"name": strings.Repeat("x", 256)}, {"externalId": strings.Repeat("x", 256)}, {"externalUserOnboardedAt": "yesterday"}, {"externalUserOnboardedAt": "2999-01-01T00:00:00Z"}, {"metadata": map[string]any{"k": ""}}, {"metadata": map[string]any{strings.Repeat("k", 65): "v"}}, {"metadata": map[string]any{"k": strings.Repeat("v", 513)}}} {
		c, e := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(in)})
		require.NoError(t, e)
		require.NotEmpty(t, c.Failures)
	}
	for _, unknown := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New("valid")
			if unknown {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::x"})
			for _, m := range []any{map[string]any{"key": v}, property.New(property.Computed).WithSecret(secret).WithDependencies(v.Dependencies())} {
				in := providertest.Props(map[string]any{"name": v, "externalId": v, "metadata": m})
				c, e := s.Check(p.CheckRequest{Urn: u, Inputs: in})
				require.NoError(t, e)
				require.Empty(t, c.Failures)
				require.Equal(t, in, c.Inputs)
				twice, e := s.Check(p.CheckRequest{Urn: u, Inputs: c.Inputs})
				require.NoError(t, e)
				require.Equal(t, c.Inputs, twice.Inputs)
				for _, create := range []bool{true, false} {
					if create {
						r, e := s.Create(p.CreateRequest{Urn: u, Properties: c.Inputs, DryRun: true})
						require.NoError(t, e)
						requireOutputValue(t, v, r.Properties.Get("name"))
						requireOutputValue(t, in.Get("metadata"), r.Properties.Get("metadata"))
					} else {
						r, e := s.Update(p.UpdateRequest{Urn: u, ID: "preview", Inputs: c.Inputs, State: providertest.Props(map[string]any{"createdAt": "", "updatedAt": ""}), DryRun: true})
						require.NoError(t, e)
						requireOutputValue(t, v, r.Properties.Get("name"))
						requireOutputValue(t, in.Get("metadata"), r.Properties.Get("metadata"))
					}
				}
			}
		}
	}
	require.Empty(t, api.Requests())
}

func TestUserProfileSDKLifecycle(t *testing.T) {
	s, api := providertest.New(t)
	observed := profileObject(t, profileJSON)
	api.Handle("POST /v1/user_profiles", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, fakeapi.Object{"name": "edited", "metadata": map[string]any{"plan": "pro", "region": "us"}}, b)
		observed["name"] = "edited"
		observed["metadata"] = map[string]any{"plan": "pro", "region": "us"}
		fakeapi.WriteJSON(w, observed)
	})
	api.Handle("DELETE /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "uprof_test", "type": "user_profile_deleted"})
	})
	integration.LifeCycleTest{Resource: "anthropic:agents:UserProfile", Create: integration.Operation{Inputs: providertest.Props(map[string]any{"name": "Acme", "metadata": map[string]any{"plan": "pro", "region": "us"}})}, Updates: []integration.Operation{{Inputs: providertest.Props(map[string]any{"name": "edited", "metadata": map[string]any{"plan": "pro", "region": "us"}})}}}.Run(t, s)
	require.Equal(t, http.MethodDelete, api.Last().Method)
}

func TestUserProfileLifecycleFullDesiredAndRemovals(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	observed := profileObject(t, profileJSON)
	api.Handle("POST /v1/user_profiles", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		require.Equal(t, "user-profiles-2026-08-18", r.Header.Get("anthropic-beta"))
		fakeapi.WriteJSON(w, observed)
	})
	api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	in := providertest.Props(map[string]any{"name": "Acme", "accessType": "passthrough", "externalId": "acme-1", "metadata": map[string]any{"plan": "pro", "region": "us"}})
	created, err := s.Create(p.CreateRequest{Urn: u, Properties: in})
	require.NoError(t, err)
	require.Equal(t, "uprof_test", created.ID)
	require.Equal(t, fakeapi.Object{"name": "Acme", "access_type": "passthrough", "external_id": "acme-1", "metadata": map[string]any{"plan": "pro", "region": "us"}}, api.Last().Body)
	next := in.Set("metadata", property.New(property.NewMap(map[string]property.Value{"plan": property.New("pro")})))
	n := len(api.Requests())
	_, err = s.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: created.Properties, Inputs: next, OldInputs: in})
	require.NoError(t, err)
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, fakeapi.Object{"name": "Acme", "access_type": "passthrough", "external_id": "acme-1", "metadata": map[string]any{"plan": "pro", "region": ""}}, api.Last().Body)
	next = next.Delete("accessType")
	_, err = s.Update(p.UpdateRequest{Urn: u, ID: created.ID, State: created.Properties, Inputs: next, OldInputs: in})
	require.NoError(t, err)
	require.Equal(t, "application", api.Last().Body["access_type"])
	a, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID})
	require.NoError(t, err)
	b, err := s.Read(p.ReadRequest{Urn: u, ID: created.ID})
	require.NoError(t, err)
	require.Equal(t, a, b)
	require.Equal(t, "acme-1", a.Inputs.Get("externalId").AsString())
	d, err := s.Diff(p.DiffRequest{Urn: u, ID: created.ID, State: a.Properties, Inputs: a.Inputs, OldInputs: a.Inputs})
	require.NoError(t, err)
	require.False(t, d.HasChanges)
	for _, key := range []string{"name", "externalId", "externalUserOnboardedAt"} {
		state := a.Properties.Set(key, property.New("2026-01-01T00:00:00Z"))
		desired := a.Inputs.Delete(key)
		d, e := s.Diff(p.DiffRequest{Urn: u, ID: created.ID, State: state, Inputs: desired, OldInputs: state})
		require.NoError(t, e)
		require.Equal(t, p.DeleteReplace, d.DetailedDiff[key].Kind)
	}
	d, err = s.Diff(p.DiffRequest{Urn: u, ID: created.ID, State: a.Properties.Set("externalId", property.New("")), Inputs: a.Inputs.Set("externalId", property.New(property.Computed))})
	require.NoError(t, err)
	require.True(t, d.HasChanges)
	api.Handle("DELETE /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "uprof_test", "type": "user_profile_deleted"})
	})
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: u, ID: created.ID, Properties: created.Properties}))
	require.Equal(t, http.MethodDelete, api.Last().Method)
}

func TestUserProfileUnusableCreateThenExplicitRefresh(t *testing.T) {
	for _, fault := range []string{`"created_at":"2026-01-01T00:00:00Z"`, `"name":"Acme"`, `"plan":"pro"`, `"status":"pending"`} {
		t.Run(fault, func(t *testing.T) {
			s, api := providertest.New(t)
			u := providertest.URN("anthropic:agents:UserProfile")
			raw := strings.Replace(profileJSON, fault, strings.Split(fault, ":")[0]+`:{"invalid":true}`, 1)
			api.Handle("POST /v1/user_profiles", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteJSON(w, profileObject(t, raw))
			})
			api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteJSON(w, profileObject(t, profileJSON))
			})
			in := providertest.Props(map[string]any{"name": "Acme", "accessType": "passthrough", "externalId": "acme-1", "metadata": map[string]any{"plan": "pro", "region": "us"}})
			c, e := s.Create(p.CreateRequest{Urn: u, Properties: in})
			require.Error(t, e)
			require.Nil(t, c.PartialState)
			require.Empty(t, c.ID)
			require.NotContains(t, e.Error(), "failed to initialize")
			require.Zero(t, c.Properties.Len())
			api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteJSON(w, profileObject(t, profileJSON))
			})
			read, e := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Inputs: in})
			require.NoError(t, e)
			n := len(api.Requests())
			r, e := s.Update(p.UpdateRequest{Urn: u, ID: read.ID, State: read.Properties, OldInputs: in, Inputs: in})
			require.NoError(t, e)
			require.Nil(t, r.PartialState)
			require.Len(t, api.Requests(), n+1)
			require.Equal(t, http.MethodPost, api.Last().Method)
		})
	}
}

func TestUserProfileReadStrictErrorsAndRejectedMutation(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	badResponse := profileJSON
	api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, profileObject(t, badResponse))
	})
	for _, bad := range []string{strings.Replace(profileJSON, `"plan":"pro"`, `"plan":null`, 1), strings.Replace(profileJSON, `"status":"pending"`, `"status":{}`, 1), strings.Replace(profileJSON, `"access_type":"passthrough"`, `"access_type":"unknown"`, 1), strings.Replace(profileJSON, `"name":"Acme"`, `"name":42`, 1)} {
		badResponse = bad
		_, e := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test"})
		require.Error(t, e)
	}
	for _, status := range []int{403, 404, 500} {
		s, api := providertest.New(t)
		for _, method := range []string{"GET", "POST", "DELETE"} {
			api.Handle(method+" /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteError(w, status, "synthetic failure")
			})
		}
		r, e := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test"})
		if status == 404 {
			require.NoError(t, e)
			require.Empty(t, r.ID)
		} else {
			require.Error(t, e)
		}
		updated, e := s.Update(p.UpdateRequest{Urn: u, ID: "uprof_test", State: providertest.Props(map[string]any{"createdAt": "", "updatedAt": ""}), Inputs: providertest.Props(map[string]any{"name": "unaccepted"})})
		require.Error(t, e)
		require.True(t, updated.Properties.Get("name").IsNull())
		e = s.Delete(p.DeleteRequest{Urn: u, ID: "uprof_test", Properties: providertest.Props(map[string]any{"createdAt": "", "updatedAt": ""})})
		if status == 404 {
			require.NoError(t, e)
		} else {
			require.Error(t, e)
		}
	}
}

func TestUserProfileUpdateExcludesForeignObservations(t *testing.T) {
	for _, responseID := range []any{"uprof_foreign", nil, 123} {
		s, api := providertest.New(t)
		u := providertest.URN("anthropic:agents:UserProfile")
		foreign := profileObject(t, profileJSON)
		foreign["id"] = responseID
		foreign["name"] = "FOREIGN"
		foreign["external_id"] = "FOREIGN-REFERENCE"
		foreign["metadata"] = map[string]any{"foreign": "unsafe"}
		foreign["created_at"] = "2026-03-01T00:00:00Z"
		foreign["updated_at"] = "2026-03-02T00:00:00Z"
		api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, foreign) })
		state := providertest.Props(map[string]any{"name": "before", "accessType": "passthrough", "metadata": map[string]any{"remove": "old", "external": "keep"}, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
		old := providertest.Props(map[string]any{"name": "before", "accessType": "passthrough", "metadata": map[string]any{"remove": "old"}})
		desired := providertest.Props(map[string]any{"name": "accepted", "metadata": map[string]any{"desired": "accepted"}})
		out, err := s.Update(p.UpdateRequest{Urn: u, ID: "uprof_test", State: state, OldInputs: old, Inputs: desired})
		require.ErrorContains(t, err, "profile response identity mismatch")
		require.NotContains(t, err.Error(), "failed to initialize")
		require.Nil(t, out.PartialState)
		require.Zero(t, out.Properties.Len(), "foreign or desired fields must not be returned as reconstructed state")
		require.Len(t, api.Requests(), 1, "no internal refresh may repair an identity mismatch")
		require.Equal(t, fakeapi.Object{"name": "accepted", "access_type": "application", "metadata": map[string]any{"desired": "accepted", "remove": ""}}, api.Last().Body)
	}
}

func TestUserProfileLegacyReadOwnershipWithoutTimestamps(t *testing.T) {
	for _, inputs := range []property.Map{
		providertest.Props(map[string]any{}),
		providertest.Props(map[string]any{"name": "Acme"}),
		providertest.Props(map[string]any{"metadata": map[string]any{}}),
	} {
		s, api := providertest.New(t)
		u := providertest.URN("anthropic:agents:UserProfile")
		api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
			fakeapi.WriteJSON(w, profileObject(t, profileJSON))
		})
		legacy := providertest.Props(map[string]any{"name": "Acme", "metadata": map[string]any{"plan": "pro"}})
		read, err := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Properties: legacy, Inputs: inputs})
		require.NoError(t, err)
		require.Equal(t, inputs, read.Inputs, "refresh preserves ownership even with empty inputs and missing timestamps")
		require.Equal(t, "2026-01-01T00:00:00Z", read.Properties.Get("createdAt").AsString())
		require.Equal(t, "passthrough", read.Properties.Get("accessType").AsString())
		require.Equal(t, "acme-1", read.Properties.Get("externalId").AsString())
		require.Equal(t, "pro", read.Properties.Get("metadata").AsMap().Get("plan").AsString())
		imported, err := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test"})
		require.NoError(t, err)
		require.Equal(t, "passthrough", imported.Inputs.Get("accessType").AsString())
		require.Equal(t, "acme-1", imported.Inputs.Get("externalId").AsString())
		require.Equal(t, "pro", imported.Inputs.Get("metadata").AsMap().Get("plan").AsString())
	}
}

func TestUserProfileResponseOnlyExtraUnexposed(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	// Representative response-only SDK field; this test does not assert a platform value.
	response := profileObject(t, strings.Replace(profileJSON, `"type":"user_profile"`, `"type":"user_profile","policy_attestations":[]`, 1))
	for _, route := range []string{"POST /v1/user_profiles", "GET /v1/user_profiles/uprof_test"} {
		api.Handle(route, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, response) })
	}
	out, e := s.Create(p.CreateRequest{Urn: u, Properties: providertest.Props(map[string]any{"name": "Acme"})})
	require.NoError(t, e)
	read, e := s.Read(p.ReadRequest{Urn: u, ID: out.ID})
	require.NoError(t, e)
	for _, bag := range []property.Map{out.Properties, read.Properties, read.Inputs} {
		for _, key := range []string{"policy_attestations", "policyAttestations"} {
			_, present := bag.GetOk(key)
			require.False(t, present, "response-only SDK extra must remain unexposed")
		}
	}
}

func TestUserProfileCreateRuntimeInputMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	api.Handle("POST /v1/user_profiles", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, profileObject(t, profileJSON))
	})
	v := property.New("Acme").WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::x"})
	in := providertest.Props(map[string]any{"name": v, "metadata": map[string]any{"plan": property.New("pro").WithSecret(true).WithDependencies(v.Dependencies())}})
	out, e := s.Create(p.CreateRequest{Urn: u, Properties: in})
	require.NoError(t, e)
	requireOutputValue(t, v, out.Properties.Get("name"))
	metadata := out.Properties.Get("metadata")
	requireNoOutputDependencies(t, metadata)
	require.Equal(t, "pro", metadata.AsMap().Get("plan").AsString())
	require.True(t, metadata.Secret() || metadata.AsMap().Get("plan").Secret())
	api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, profileObject(t, profileJSON))
	})
	read, e := s.Read(p.ReadRequest{Urn: u, ID: out.ID, Inputs: in, Properties: out.Properties})
	require.NoError(t, e)
	requireOutputValue(t, v, read.Properties.Get("name"))
	requireOutputValue(t, v, read.Inputs.Get("name"))
}

func TestUserProfileIncompleteUpdateOrdinaryError(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	// Unmanaged fields must not cause resets or external-key deletion patches,
	// even when the mutation response is unusable.
	state := providertest.Props(map[string]any{"name": "Acme", "accessType": "passthrough", "externalId": "acme-1", "metadata": map[string]any{"plan": "pro", "external": "retained"}, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z", "trustGrants": map[string]any{"known": map[string]any{"status": "active"}}})
	observed := profileObject(t, `{"id":"uprof_test","type":"user_profile","name":"server canonical","updated_at":"2026-01-02T00:00:00Z"}`)
	api.Handle("POST /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, observed) })
	inputs := providertest.Props(map[string]any{"name": "new", "metadata": map[string]any{"plan": "accepted"}})
	old := providertest.Props(map[string]any{"name": "Acme", "metadata": map[string]any{"plan": "pro"}})
	n := len(api.Requests())
	out, e := s.Update(p.UpdateRequest{Urn: u, ID: "uprof_test", State: state, OldInputs: old, Inputs: inputs})
	require.Error(t, e)
	require.Nil(t, out.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, out.Properties.Len())
	require.Len(t, api.Requests(), n+1)
	require.Equal(t, fakeapi.Object{"name": "new", "metadata": map[string]any{"plan": "accepted"}}, api.Last().Body)
	require.Equal(t, "Acme", state.Get("name").AsString(), "request state is immutable")
	require.Equal(t, "pro", state.Get("metadata").AsMap().Get("plan").AsString())
	// Managed access-type removal must still request an application reset.
	delete(observed, "updated_at")
	old = old.Set("accessType", property.New("passthrough"))
	out, e = s.Update(p.UpdateRequest{Urn: u, ID: "uprof_test", State: state, OldInputs: old, Inputs: providertest.Props(map[string]any{"name": "new"})})
	require.Error(t, e)
	require.Nil(t, out.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, out.Properties.Len())
	require.Equal(t, fakeapi.Object{"name": "new", "access_type": "application", "metadata": map[string]any{"plan": ""}}, api.Last().Body)
}

func TestUserProfileReadOwnershipAndManagedDrift(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	raw := strings.Replace(profileJSON, `"region":"us"`, `"region":"us","external":"retained"`, 1)
	api.Handle("GET /v1/user_profiles/uprof_test", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, profileObject(t, raw))
	})
	state := providertest.Props(map[string]any{"createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"})
	inputs := providertest.Props(map[string]any{"name": "desired", "metadata": map[string]any{"plan": "pro"}})
	r, e := s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Inputs: inputs, Properties: state})
	require.NoError(t, e)
	require.Equal(t, "Acme", r.Inputs.Get("name").AsString())
	require.True(t, r.Inputs.Get("accessType").IsNull())
	require.Equal(t, "passthrough", r.Properties.Get("accessType").AsString())
	require.True(t, r.Inputs.Get("metadata").AsMap().Get("external").IsNull())
	require.Equal(t, "retained", r.Properties.Get("metadata").AsMap().Get("external").AsString())
	d, e := s.Diff(p.DiffRequest{Urn: u, ID: r.ID, State: r.Properties, OldInputs: r.Inputs, Inputs: inputs})
	require.NoError(t, e)
	require.True(t, d.HasChanges)
	// Explicit empty strings remain managed scalar values; readable nonempty drift
	// cannot be masked by zero-value projection.
	empty := inputs.Set("name", property.New(""))
	r, e = s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Inputs: empty, Properties: state})
	require.NoError(t, e)
	require.Equal(t, "Acme", r.Inputs.Get("name").AsString())
	// Engine refresh may supply a prior partial checkpoint without a timestamp.
	partial := state.Delete("createdAt")
	r, e = s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Inputs: inputs, Properties: partial})
	require.NoError(t, e)
	require.Equal(t, "2026-01-01T00:00:00Z", r.Properties.Get("createdAt").AsString())
	require.True(t, r.Inputs.Get("accessType").IsNull())
	// Metadata owns individual declared keys; an empty declaration owns none and
	// does not authorize deletion of arbitrary externally added keys.
	empty = providertest.Props(map[string]any{"metadata": map[string]any{}})
	r, e = s.Read(p.ReadRequest{Urn: u, ID: "uprof_test", Inputs: empty, Properties: state})
	require.NoError(t, e)
	require.True(t, r.Inputs.Get("metadata").IsMap())
	require.Zero(t, r.Inputs.Get("metadata").AsMap().Len())
}

func TestUserProfileTimestampEquivalenceAndUnknownEnums(t *testing.T) {
	s, api := providertest.New(t)
	u := providertest.URN("anthropic:agents:UserProfile")
	for _, key := range []string{"accessType", "externalUserOnboardedAt"} {
		in := providertest.Props(map[string]any{key: property.New(property.Computed)})
		c, e := s.Check(p.CheckRequest{Urn: u, Inputs: in})
		require.NoError(t, e)
		require.Empty(t, c.Failures)
		require.Equal(t, in, c.Inputs)
	}
	a := providertest.Props(map[string]any{"externalUserOnboardedAt": "2026-01-01T01:00:00+01:00"})
	b := providertest.Props(map[string]any{"externalUserOnboardedAt": "2026-01-01T00:00:00Z"})
	d, e := s.Diff(p.DiffRequest{Urn: u, State: a, OldInputs: a, Inputs: b})
	require.NoError(t, e)
	require.False(t, d.HasChanges)
	require.Empty(t, api.Requests())
}
