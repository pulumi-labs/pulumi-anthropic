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
	"fmt"
	"maps"
	"net/http"
	"sync"
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

const spendLimitsPath = "/v1/organizations/spend_limits"

// spendLimitServerBehavior mimics the upsert: a limit is keyed by its scope and period,
// and setting it again overwrites it in place. This synthetic fixture assumes
// monthly for omitted periods; it is not evidence of the server's default.
func spendLimitServerBehavior(api *fakeapi.Server) {
	var mu sync.Mutex
	ids := map[string]string{}
	api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
		mu.Lock()
		defer mu.Unlock()
		obj := maps.Clone(body)
		if obj["period"] == nil {
			obj["period"] = "monthly"
		}
		key := fmt.Sprint(obj["scope"], obj["period"])
		if ids[key] == "" || api.Object(spendLimitsPath+"/"+ids[key]) == nil {
			ids[key] = api.NewID("spl")
		}
		obj["id"], obj["type"], obj["currency"] = ids[key], "spend_limit", "USD"
		obj["is_enabled"] = true
		obj["created_at"], obj["updated_at"] = "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"
		api.Put(spendLimitsPath+"/"+ids[key], obj)
		fakeapi.WriteJSON(w, obj)
	})
}

func spendLimit(amount any, period string) map[string]any {
	limit := map[string]any{"scope": map[string]any{"type": "user", "userId": "user_1"}}
	if amount != nil {
		limit["amount"] = amount
	}
	if period != "" {
		limit["period"] = period
	}
	return limit
}

func TestSpendLimitLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	spendLimitServerBehavior(api)

	integration.LifeCycleTest{
		Resource: "anthropic:organization:SpendLimit",
		Create: integration.Operation{
			Inputs: providertest.Props(spendLimit("50000", "monthly")),
			Hook: func(_, output property.Map) {
				assert.Equal(t, "USD", output.Get("currency").AsString())
				req := api.LastRequest(t, http.MethodPost, spendLimitsPath)
				assert.Equal(t, fakeapi.Object{
					"scope":  map[string]any{"type": "user", "user_id": "user_1"},
					"period": "monthly",
					"amount": "50000",
				}, req.Body)
			},
		},
		Updates: []integration.Operation{
			{
				// There is no update-by-ID endpoint: the limit is set again for the same
				// scope and period.
				Inputs: providertest.Props(spendLimit("60000", "monthly")),
				Hook: func(_, output property.Map) {
					req := api.LastRequest(t, http.MethodPost, spendLimitsPath)
					assert.Equal(t, "60000", req.Body["amount"])
					assert.Equal(t, "monthly", req.Body["period"])
				},
			},
			{
				// Dropping the amount sets an explicit "no limit" override.
				Inputs: providertest.Props(spendLimit(nil, "monthly")),
				Hook: func(_, output property.Map) {
					req := api.LastRequest(t, http.MethodPost, spendLimitsPath)
					amount, sent := req.Body["amount"]
					assert.True(t, sent)
					assert.Nil(t, amount)
				},
			},
			{
				// A new period is a different limit.
				Inputs: providertest.Props(spendLimit("100", "daily")),
			},
		},
	}.Run(t, server)

	var deletes []string
	for _, req := range api.Requests() {
		if req.Method == http.MethodDelete {
			deletes = append(deletes, req.Path)
		}
	}
	assert.Len(t, deletes, 2, "the monthly limit is deleted when replaced, then the daily one")
	assert.NotEqual(t, deletes[0], deletes[1])
}

func TestSpendLimitRefresh(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	spendLimitServerBehavior(api)

	created, checked := providertest.Create(t, server, urn("SpendLimit"), providertest.Props(spendLimit("50000", "")))
	resp := providertest.Read(t, server, urn("SpendLimit"), created, checked)
	assert.Equal(t, checked, resp.Inputs, "the server's default period is not drift")

	api.Mutate(spendLimitsPath+"/"+created.ID, func(obj fakeapi.Object) { obj["amount"] = "1" })
	resp = providertest.Read(t, server, urn("SpendLimit"), created, checked)
	assert.Equal(t, "1", resp.Inputs.Get("amount").AsString())
}

func spendLimitObservation(id string, amount any) fakeapi.Object {
	return fakeapi.Object{"id": id, "type": "spend_limit", "scope": map[string]any{"type": "user", "user_id": "user_1"}, "period": "monthly", "amount": amount, "currency": "USD", "is_enabled": false, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
}

func TestSpendLimitValidationAndMetadata(t *testing.T) {
	s, api := providertest.New(t)
	u := urnForSpendLimit()
	cases := []struct {
		name   string
		inputs map[string]any
		field  string
	}{
		{"money", spendLimit("-1", "monthly"), "amount"},
		{"decimal", spendLimit("1.5", "monthly"), "amount"},
		{"period", spendLimit("0", "yearly"), "period"},
		{"scope", map[string]any{"scope": map[string]any{"type": "group"}}, "scope.type"},
		{"missingID", map[string]any{"scope": map[string]any{"type": "user"}}, "scope.userId"},
		{"extraID", map[string]any{"scope": map[string]any{"type": "user", "userId": "user_1", "workspaceId": "w"}}, "scope.workspaceId"},
		{"consoleAmount", map[string]any{"scope": map[string]any{"type": "organization"}, "period": "monthly"}, "amount"},
		{"consolePeriod", map[string]any{"scope": map[string]any{"type": "organization"}, "period": "daily", "amount": "0"}, "period"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := s.Check(p.CheckRequest{Urn: u, Inputs: providertest.Props(tc.inputs)})
			require.NoError(t, err)
			found := false
			for _, f := range out.Failures {
				found = found || f.Property == tc.field
			}
			require.True(t, found, "%v", out.Failures)
		})
	}
	deps := []resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::dependency"}
	for _, v := range []property.Value{property.New("0"), property.New(property.Computed)} {
		v = v.WithSecret(true).WithDependencies(deps)
		in := providertest.Props(spendLimit("0", "monthly")).Set("amount", v)
		original := in
		out, err := s.Check(p.CheckRequest{Urn: u, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, out.Failures)
		require.Equal(t, in, out.Inputs)
		again, err := s.Check(p.CheckRequest{Urn: u, Inputs: out.Inputs})
		require.NoError(t, err)
		require.Equal(t, out.Inputs, again.Inputs)
		require.Equal(t, original, in)
		preview, err := s.Create(p.CreateRequest{Urn: u, Properties: out.Inputs, DryRun: true})
		require.NoError(t, err)
		requireOutputValue(t, v, preview.Properties.Get("amount"))
		require.True(t, preview.Properties.Get("isEnabled").IsComputed())
	}
	unknown := property.New(property.Computed).WithDependencies(deps)
	for _, key := range []string{"scope", "period"} {
		in := providertest.Props(spendLimit("0", "monthly")).Set(key, unknown)
		out, err := s.Check(p.CheckRequest{Urn: u, Inputs: in})
		require.NoError(t, err)
		require.Empty(t, out.Failures)
		require.Equal(t, in, out.Inputs)
	}
	require.Empty(t, api.Requests())
}

func urnForSpendLimit() resourceurn.URN { return providertest.URN("anthropic:organization:SpendLimit") }

func TestSpendLimitUnusableCreateAndRefreshedUpdate(t *testing.T) {
	s, api := providertest.New(t)
	u := urnForSpendLimit()
	in := providertest.Props(spendLimit("50000", "monthly"))
	calls := 0
	api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, r *http.Request, body fakeapi.Object) {
		calls++
		require.Equal(t, fakeapi.Object{"scope": map[string]any{"type": "user", "user_id": "user_1"}, "period": "monthly", "amount": "50000"}, body)
		obj := spendLimitObservation("spl_partial", "50000")
		if calls == 1 {
			obj["currency"] = map[string]any{"invalid": true}
		}
		fakeapi.WriteJSON(w, obj)
	})
	first, err := s.Create(p.CreateRequest{Urn: u, Properties: in})
	requireOrdinaryError(t, err)
	require.Empty(t, first.ID)
	require.Nil(t, first.PartialState)
	require.Empty(t, first.Properties.AsMap())
	api.Put(spendLimitsPath+"/spl_partial", spendLimitObservation("spl_partial", "50000"))
	refreshed, err := s.Read(p.ReadRequest{Urn: u, ID: "spl_partial", Properties: in, Inputs: in})
	require.NoError(t, err)
	require.False(t, refreshed.Properties.Get("isEnabled").AsBool())
	before := len(api.Requests())
	next, err := s.Update(p.UpdateRequest{Urn: u, ID: refreshed.ID, State: refreshed.Properties, OldInputs: in, Inputs: in})
	require.NoError(t, err)
	require.Nil(t, next.PartialState)
	require.Equal(t, "USD", next.Properties.Get("currency").AsString())
	require.Len(t, api.Requests(), before+1, "Update must not issue a GET or suppress an unchanged desired request")
}

func TestSpendLimitImportDriftErrorsAndIdentity(t *testing.T) {
	s, api := providertest.New(t)
	u := urnForSpendLimit()
	const id = "spl_import"
	api.Put(spendLimitsPath+"/"+id, spendLimitObservation(id, nil))
	read, err := s.Read(p.ReadRequest{Urn: u, ID: id})
	require.NoError(t, err)
	require.True(t, read.Inputs.Get("amount").IsNull())
	require.False(t, read.Properties.Get("isEnabled").AsBool())
	api.Mutate(spendLimitsPath+"/"+id, func(o fakeapi.Object) { o["amount"] = "0" })
	drift, err := s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: read.Inputs, Properties: read.Properties})
	require.NoError(t, err)
	require.Equal(t, "0", drift.Inputs.Get("amount").AsString())
	status := 403
	api.Handle("GET "+spendLimitsPath+"/"+id, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		w.WriteHeader(status)
		fakeapi.WriteJSON(w, fakeapi.Object{"type": "error", "error": map[string]any{"type": "api_error", "message": "synthetic"}})
	})
	for _, code := range []int{403, 500} {
		status = code
		_, err = s.Read(p.ReadRequest{Urn: u, ID: id, Inputs: read.Inputs, Properties: read.Properties})
		require.Error(t, err)
	}
	api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, spendLimitObservation("spl_foreign", "0"))
	})
	out, err := s.Update(p.UpdateRequest{Urn: u, ID: id, State: drift.Properties, OldInputs: drift.Inputs, Inputs: drift.Inputs})
	require.ErrorContains(t, err, "identity changed")
	require.Nil(t, out.PartialState)
}

func TestSpendLimitMappingsAndOutputMetadata(t *testing.T) {
	for _, kind := range []string{"user", "workspace", "organization"} {
		t.Run(kind, func(t *testing.T) {
			s, api := providertest.New(t)
			scope := map[string]any{"type": kind}
			wire := map[string]any{"type": kind}
			if kind == "user" {
				scope["userId"] = "user_1"
				wire["user_id"] = "user_1"
			}
			if kind == "workspace" {
				scope["workspaceId"] = "wrkspc_1"
				wire["workspace_id"] = "wrkspc_1"
			}
			in := providertest.Props(map[string]any{"scope": scope, "amount": "0"}) // Period omission is preserved on the wire.
			deps := []resourceurn.URN{"urn:pulumi:test::test::test:index:Dependency::dependency"}
			in = in.Set("amount", in.Get("amount").WithSecret(true).WithDependencies(deps))
			in = in.Set("scope", in.Get("scope").WithSecret(true).WithDependencies(deps))
			api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, _ *http.Request, body fakeapi.Object) {
				require.Equal(t, fakeapi.Object{"scope": wire, "amount": "0"}, body)
				obj := spendLimitObservation("spl_map", "0")
				obj["scope"] = wire
				fakeapi.WriteJSON(w, obj)
			})
			checked, err := s.Check(p.CheckRequest{Urn: urnForSpendLimit(), Inputs: in})
			require.NoError(t, err)
			require.Empty(t, checked.Failures)
			created, err := s.Create(p.CreateRequest{Urn: urnForSpendLimit(), Properties: checked.Inputs})
			require.NoError(t, err)
			requireOutputValue(t, in.Get("amount"), created.Properties.Get("amount"))
			requireOutputValue(t, in.Get("scope"), created.Properties.Get("scope"))
			require.Equal(t, "monthly", created.Properties.Get("period").AsString())
			preview, err := s.Update(p.UpdateRequest{Urn: urnForSpendLimit(), ID: created.ID, State: created.Properties, OldInputs: in, Inputs: in, DryRun: true})
			require.NoError(t, err)
			requireOutputValue(t, in.Get("scope"), preview.Properties.Get("scope"))
			requireOutputValue(t, in.Get("amount"), preview.Properties.Get("amount"))
			require.Len(t, api.Requests(), 1)
		})
	}
}

func TestSpendLimitMalformedObservations(t *testing.T) {
	for _, field := range []string{"id", "amount", "currency", "is_enabled", "created_at", "updated_at", "period", "scope"} {
		t.Run(field, func(t *testing.T) {
			s, api := providertest.New(t)
			obj := spendLimitObservation("spl_invalid", "0")
			switch field {
			case "amount":
				obj[field] = "-1"
			case "period":
				obj[field] = "yearly"
			case "scope":
				obj[field] = map[string]any{"type": "seat_tier", "seat_tier": "standard"}
			default:
				obj[field] = map[string]any{"invalid": true}
			}
			api.Put(spendLimitsPath+"/spl_invalid", obj)
			_, err := s.Read(p.ReadRequest{Urn: urnForSpendLimit(), ID: "spl_invalid"})
			require.Error(t, err)
		})
	}
}

func TestSpendLimitRejectsIncompleteResponse(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"id": "spl_minimal", "currency": "USD", "is_enabled": false})
	})
	in := providertest.Props(spendLimit("0", "monthly"))
	out, err := s.Create(p.CreateRequest{Urn: urnForSpendLimit(), Properties: in})
	requireOrdinaryError(t, err)
	require.Nil(t, out.PartialState)
	require.Empty(t, out.ID)
	require.Empty(t, out.Properties.AsMap())
	require.Len(t, api.Requests(), 1)
	require.Equal(t, fakeapi.Object{"scope": map[string]any{"type": "user", "user_id": "user_1"}, "period": "monthly", "amount": "0"}, api.Last().Body)
}

// Wrapped outputs use property.Value's secret, knownness and dependency representation.
func TestSpendLimitInputNonmutation(t *testing.T) {
	s, api := providertest.New(t)
	spendLimitServerBehavior(api)
	in := providertest.Props(spendLimit("0", "monthly"))
	before := spendLimitSnapshot(property.New(in)).AsMap()
	checked, err := s.Check(p.CheckRequest{Urn: urnForSpendLimit(), Inputs: in})
	require.NoError(t, err)
	created, err := s.Create(p.CreateRequest{Urn: urnForSpendLimit(), Properties: checked.Inputs})
	require.NoError(t, err)
	stateBefore := spendLimitSnapshot(property.New(created.Properties)).AsMap()
	_, err = s.Update(p.UpdateRequest{Urn: urnForSpendLimit(), ID: created.ID, State: created.Properties, OldInputs: in, Inputs: in})
	require.NoError(t, err)
	require.Equal(t, before, in)
	require.Equal(t, stateBefore, created.Properties)
}

func spendLimitSnapshot(v property.Value) property.Value {
	if v.IsMap() {
		m := map[string]property.Value{}
		for k, child := range v.AsMap().All {
			m[k] = spendLimitSnapshot(child)
		}
		return property.New(property.NewMap(m)).WithSecret(v.Secret()).WithDependencies(append([]resourceurn.URN(nil), v.Dependencies()...))
	}
	if v.IsArray() {
		a := []property.Value{}
		for _, child := range v.AsArray().All {
			a = append(a, spendLimitSnapshot(child))
		}
		return property.New(a).WithSecret(v.Secret()).WithDependencies(append([]resourceurn.URN(nil), v.Dependencies()...))
	}
	return v.WithDependencies(append([]resourceurn.URN(nil), v.Dependencies()...))
}

func TestSpendLimitNestedScopeReplacement(t *testing.T) {
	s, _ := providertest.New(t)
	u := urnForSpendLimit()
	old := providertest.Props(spendLimit("0", "monthly"))
	state := old.Set("currency", property.New("USD")).Set("isEnabled", property.New(true)).Set("createdAt", property.New("x")).Set("updatedAt", property.New("x"))
	next := old.Set("scope", property.New(property.NewMap(map[string]property.Value{"type": property.New("user"), "userId": property.New("user_2")})))
	diff, err := s.Diff(p.DiffRequest{Urn: u, ID: "spl_1", State: state, OldInputs: old, Inputs: next})
	require.NoError(t, err)
	require.True(t, diff.DeleteBeforeReplace)
	require.True(t, diff.HasChanges)
	for _, d := range diff.DetailedDiff {
		require.Contains(t, []p.DiffKind{p.AddReplace, p.UpdateReplace, p.DeleteReplace}, d.Kind)
	}
}

// The generated SDK deliberately decodes primitives loosely. Resource state and
// identity must instead respect the API's JSON types, even when Valid is true.
func TestSpendLimitRejectsCoercedResponsePrimitives(t *testing.T) {
	for _, field := range []string{"id", "amount", "currency", "is_enabled", "created_at", "updated_at", "period", "scope.type", "scope.user_id", "scope.workspace_id", "type"} {
		primitives := []any{123, true, false}
		if field == "is_enabled" {
			primitives = []any{0, 1, "true", "false"}
		}
		for _, primitive := range primitives {
			t.Run(fmt.Sprintf("%s/%v", field, primitive), func(t *testing.T) {
				server, api := providertest.New(t)
				id := "spl_primitive"
				if field == "id" {
					id = fmt.Sprint(primitive)
				} // Also reject a coerced ID that would match the request.
				obj := spendLimitObservation(id, "0")
				in := providertest.Props(spendLimit("50000", "monthly"))
				switch field {
				case "scope.type":
					obj["scope"].(map[string]any)["type"] = primitive
				case "scope.user_id":
					obj["scope"].(map[string]any)["user_id"] = primitive
				case "scope.workspace_id":
					obj["scope"] = map[string]any{"type": "workspace", "workspace_id": primitive}
					in = providertest.Props(map[string]any{"scope": map[string]any{"type": "workspace", "workspaceId": "wrkspc_1"}, "period": "monthly", "amount": "50000"})
				default:
					obj[field] = primitive
				}
				api.Put(spendLimitsPath+"/"+id, obj)
				api.Handle("POST "+spendLimitsPath, func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
				read, err := server.Read(p.ReadRequest{Urn: urnForSpendLimit(), ID: id})
				require.Error(t, err)
				require.Empty(t, read.Properties.AsMap(), "malformed Read must not publish coerced observations")
				created, err := server.Create(p.CreateRequest{Urn: urnForSpendLimit(), Properties: in})
				requireOrdinaryError(t, err)
				state := in.Set("currency", property.New("USD")).Set("isEnabled", property.New(false)).Set("createdAt", property.New("2026-01-01T00:00:00Z")).Set("updatedAt", property.New("2026-01-01T00:00:00Z"))
				updated, updateErr := server.Update(p.UpdateRequest{Urn: urnForSpendLimit(), ID: id, State: state, OldInputs: in, Inputs: in})
				requireOrdinaryError(t, updateErr)
				require.Empty(t, created.ID, "never publish an unusable allocation")
				require.Nil(t, created.PartialState)
				require.Nil(t, updated.PartialState)
				require.Empty(t, created.Properties.AsMap())
				require.Empty(t, updated.Properties.AsMap())
				require.Len(t, api.Requests(), 3, "Read, Create and Update each make one request")
			})
		}
	}
}
