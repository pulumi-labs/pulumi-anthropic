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
	"maps"
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

const environmentToken = "anthropic:agents:Environment"

var environmentURN = providertest.URN(environmentToken)

func baseEnvironment() map[string]any {
	return map[string]any{
		"name": "sandbox", "description": "old", "scope": "organization",
		"config":   map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited", "allowedHosts": []any{"example.com"}, "allowMcpServers": true, "allowPackageManagers": true}, "packages": map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{"pandas"}}},
		"metadata": map[string]any{"owner": "test", "remove": "old"},
	}
}

// Explicit synthetic defaults, not claims about Anthropic server behavior.
func environmentServerBehavior(api *fakeapi.Server) {
	api.SetHooks("environments", fakeapi.Hooks{OnCreate: func(o fakeapi.Object) {
		if o["config"] == nil {
			o["config"] = map[string]any{"type": "cloud"}
		}
		o["archived_at"] = nil
	}, OnUpdate: func(before, after, body fakeapi.Object) error {
		if _, ok := after["description"]; !ok {
			after["description"] = nil
		}
		return nil
	}})
}

func TestEnvironmentCheck(t *testing.T) {
	s, api := providertest.New(t)
	for _, where := range []string{"config", "networking", "packages", "allowPackageManagers"} {
		in := baseEnvironment()
		c := in["config"].(map[string]any)
		unknown := property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::v"})
		switch where {
		case "config":
			in["config"] = unknown
		case "networking", "packages":
			c[where] = unknown
		default:
			c["networking"].(map[string]any)[where] = unknown
		}
		inputs := providertest.Props(in)
		checked, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: inputs})
		require.NoError(t, e)
		require.Empty(t, checked.Failures)
		require.Equal(t, inputs, checked.Inputs)
		again, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: checked.Inputs})
		require.NoError(t, e)
		require.Equal(t, checked.Inputs, again.Inputs)
	}
	for _, permission := range []any{nil, false} {
		in := baseEnvironment()
		n := in["config"].(map[string]any)["networking"].(map[string]any)
		if permission == nil {
			delete(n, "allowPackageManagers")
		} else {
			n["allowPackageManagers"] = permission
		}
		r, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: providertest.Props(in)})
		require.NoError(t, e)
		require.NotEmpty(t, r.Failures)
		require.Equal(t, "config.networking.allowPackageManagers", r.Failures[0].Property)
	}
	for _, in := range []map[string]any{{"name": ""}, {"name": strings.Repeat("é", 257)}, {"name": "ok", "config": map[string]any{"type": "self_hosted", "packages": map[string]any{}}}, {"name": "ok", "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "unrestricted", "allowedHosts": []any{}}}}} {
		r, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: providertest.Props(in)})
		require.NoError(t, e)
		require.NotEmpty(t, r.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestEnvironmentFrameworkLifecycle(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	initial := providertest.Props(map[string]any{"name": "sandbox", "description": "description", "scope": "organization", "config": map[string]any{"type": "self_hosted"}, "metadata": map[string]any{"owner": "test", "remove": "old"}})
	// The framework helper omits OldInputs; deletion ownership is tested by
	// TestEnvironmentLifecycle with actual previous checked inputs instead.
	desired := initial.Set("name", property.New("renamed"))
	integration.LifeCycleTest{
		Resource: environmentToken,
		Create:   integration.Operation{Inputs: initial},
		Updates: []integration.Operation{{Inputs: desired, Hook: func(_, output property.Map) {
			require.Equal(t, "renamed", output.Get("name").AsString())
			require.Equal(t, fakeapi.Object{"name": "renamed", "description": "description", "scope": "organization", "metadata": map[string]any{"owner": "test", "remove": "old"}}, api.Last().Body)
		}}},
	}.Run(t, s)
	require.Equal(t, http.MethodDelete, api.Last().Method)
}

func TestEnvironmentLifecycle(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	created, inputs := providertest.Create(t, s, environmentURN, providertest.Props(baseEnvironment()))
	require.Equal(t, "managed-agents-2026-04-01", api.Last().Header.Get("anthropic-beta"))
	require.Equal(t, "sandbox", created.Properties.Get("name").AsString())
	read := providertest.Read(t, s, environmentURN, created, inputs)
	require.Equal(t, inputs, read.Inputs)
	again := providertest.Read(t, s, environmentURN, created, inputs)
	require.Equal(t, read, again)
	imported, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: created.ID})
	require.NoError(t, e)
	require.Equal(t, "sandbox", imported.Inputs.Get("name").AsString())
	// Desired full state, explicit empty hosts, removed packages/description/key,
	// and false permission values must reach the generated SDK exactly once.
	in := baseEnvironment()
	delete(in, "description")
	in["metadata"] = map[string]any{"owner": "test"}
	c := in["config"].(map[string]any)
	delete(c, "packages")
	n := c["networking"].(map[string]any)
	n["allowedHosts"] = []any{}
	n["allowMcpServers"] = false
	n["allowPackageManagers"] = false
	desired := providertest.Props(in)
	snapshot := created.Properties
	count := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: desired})
	require.NoError(t, e)
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
	require.Equal(t, fakeapi.Object{"name": "sandbox", "description": nil, "scope": "organization", "metadata": map[string]any{"owner": "test", "remove": nil}, "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{}, "allow_mcp_servers": false, "allow_package_managers": false}, "packages": map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{}}}}, api.Last().Body)
	require.Equal(t, snapshot, created.Properties)
	require.False(t, updated.Properties.Get("config").AsMap().Get("networking").AsMap().Get("allowMcpServers").AsBool())
	api.Mutate("/v1/environments/"+created.ID, func(o fakeapi.Object) { o["name"] = "drift"; o["scope"] = "account" })
	drift := providertest.Read(t, s, environmentURN, created, inputs)
	require.Equal(t, "drift", drift.Inputs.Get("name").AsString())
	require.Equal(t, "account", drift.Properties.Get("scope").AsString())
	require.NoError(t, s.Delete(p.DeleteRequest{Urn: environmentURN, ID: created.ID, Properties: updated.Properties}))
	require.Equal(t, http.MethodDelete, api.Last().Method)
	missing, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: created.ID})
	require.NoError(t, e)
	require.Empty(t, missing.ID)
}

func TestEnvironmentDiffAndPreview(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	created, inputs := providertest.Create(t, s, environmentURN, providertest.Props(baseEnvironment()))
	same := providertest.Diff(t, s, environmentURN, created, inputs, inputs)
	require.False(t, same.HasChanges)
	changed := baseEnvironment()
	changed["name"] = "new"
	d := providertest.Diff(t, s, environmentURN, created, inputs, providertest.Props(changed))
	require.Equal(t, p.Update, d.DetailedDiff["name"].Kind)
	require.Len(t, d.DetailedDiff, 1)
	for _, config := range []any{nil, map[string]any{"type": "self_hosted"}} {
		changed = baseEnvironment()
		if config == nil {
			delete(changed, "config")
		} else {
			changed["config"] = config
		}
		d = providertest.Diff(t, s, environmentURN, created, inputs, providertest.Props(changed))
		found := false
		for _, v := range d.DetailedDiff {
			if v.Kind == p.DeleteReplace || v.Kind == p.UpdateReplace {
				found = true
			}
		}
		require.True(t, found)
	}
	count := len(api.Requests())
	unknown := inputs.Set("name", property.New(property.Computed))
	checked, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: unknown})
	require.NoError(t, e)
	require.Empty(t, checked.Failures)
	preview, e := s.Create(p.CreateRequest{Urn: environmentURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, e)
	require.True(t, preview.Properties.Get("name").IsComputed())
	_, e = s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, Inputs: unknown, DryRun: true})
	require.NoError(t, e)
	require.Len(t, api.Requests(), count)
}

func TestEnvironmentFailedCreateRefreshAndErrors(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	const id = "env_partial"
	obj := fakeapi.Object{"id": id, "name": "sandbox", "config": map[string]any{"type": "self_hosted"}, "metadata": map[string]any{}, "description": nil, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	api.Put("/v1/environments/"+id, obj)
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		partial := fakeapi.Object{}
		for k, v := range obj {
			partial[k] = v
		}
		delete(partial, "updated_at")
		fakeapi.WriteJSON(w, partial)
	})
	api.Handle("POST /v1/environments/"+id, func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	inputs := providertest.Props(map[string]any{"name": "sandbox", "config": map[string]any{"type": "self_hosted"}})
	partial, e := s.Create(p.CreateRequest{Urn: environmentURN, Properties: inputs})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Empty(t, partial.ID)
	// Refresh legacy incomplete state explicitly before Update.
	read, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: id, Inputs: inputs, Properties: inputs})
	require.NoError(t, e)
	count := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: id, State: read.Properties, OldInputs: inputs, Inputs: inputs})
	require.NoError(t, e)
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, fakeapi.Object{"name": "sandbox"}, api.Last().Body)
	require.Equal(t, "2026-01-01T00:00:00Z", updated.Properties.Get("updatedAt").AsString())
	status := 403
	api.Handle("DELETE /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic refusal")
	})
	for _, code := range []int{403, 409} {
		status = code
		e = s.Delete(p.DeleteRequest{Urn: environmentURN, ID: id, Properties: updated.Properties})
		require.Error(t, e)
		require.Equal(t, http.MethodDelete, api.Last().Method)
	}
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, 403, "synthetic denial")
	})
	_, e = s.Read(p.ReadRequest{Urn: environmentURN, ID: id})
	require.Error(t, e)
}

func TestEnvironmentConservativeCreateRetry(t *testing.T) {
	s, api := providertest.New(t)
	calls := 0
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		calls++
		fakeapi.WriteError(w, 500, "synthetic possibly allocated create")
	})
	_, e := s.Create(p.CreateRequest{Urn: environmentURN, Properties: providertest.Props(map[string]any{"name": "sandbox"})})
	require.Error(t, e)
	require.Equal(t, 1, calls)
}

func TestEnvironmentEnumValidation(t *testing.T) {
	s, api := providertest.New(t)
	for _, field := range []string{"scope", "config.type", "config.networking.type"} {
		makeInput := func(v any) map[string]any {
			in := map[string]any{"name": "sandbox"}
			switch field {
			case "scope":
				in["scope"] = v
			case "config.type":
				in["config"] = map[string]any{"type": v}
			default:
				in["config"] = map[string]any{"type": "cloud", "networking": map[string]any{"type": v}}
			}
			return in
		}
		checked, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: providertest.Props(makeInput("typo"))})
		require.NoError(t, e)
		require.Contains(t, checked.Failures, p.CheckFailure{Property: field, Reason: map[string]string{"scope": "must be organization or account", "config.type": "must be cloud or self_hosted", "config.networking.type": "must be limited or unrestricted"}[field]})
		// Even a direct, unchecked mutation must not substitute a valid variant.
		_, e = s.Create(p.CreateRequest{Urn: environmentURN, Properties: providertest.Props(makeInput("typo"))})
		require.Error(t, e)
		for _, valid := range map[string][]string{"scope": {"organization", "account"}, "config.type": {"cloud", "self_hosted"}, "config.networking.type": {"limited", "unrestricted"}}[field] {
			good, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: providertest.Props(makeInput(valid))})
			require.NoError(t, e)
			require.Empty(t, good.Failures)
		}
		unknown := property.New(property.Computed).WithSecret(true).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::enum"})
		input := providertest.Props(makeInput(unknown))
		good, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: input})
		require.NoError(t, e)
		require.Empty(t, good.Failures)
		actual := good.Inputs.Get("scope")
		if field == "config.type" {
			actual = good.Inputs.Get("config").AsMap().Get("type")
		}
		if field == "config.networking.type" {
			actual = good.Inputs.Get("config").AsMap().Get("networking").AsMap().Get("type")
		}
		require.Equal(t, unknown, actual, "enum validation must preserve unknown/secret/dependency metadata")
		again, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: good.Inputs})
		require.NoError(t, e)
		require.Equal(t, good.Inputs, again.Inputs)
	}
	require.Empty(t, api.Requests())
}

func TestEnvironmentUnmanagedUpdateAndIncompleteObservation(t *testing.T) {
	s, api := providertest.New(t)
	obj := fakeapi.Object{"id": "env_unmanaged", "name": "sandbox", "description": "external description", "metadata": map[string]any{"external": "kept"}, "config": map[string]any{"type": "self_hosted"}, "scope": "organization", "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	partial := false
	api.Handle("POST /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		if partial {
			fakeapi.WriteJSON(w, fakeapi.Object{"id": "env_unmanaged", "name": "renamed", "updated_at": "2026-01-02T00:00:00Z", "archived_at": nil})
			return
		}
		copy := fakeapi.Object{}
		for k, v := range obj {
			copy[k] = v
		}
		copy["name"] = "renamed"
		fakeapi.WriteJSON(w, copy)
	})
	created, inputs := providertest.Create(t, s, environmentURN, providertest.Props(map[string]any{"name": "sandbox"}))
	require.Equal(t, "external description", created.Properties.Get("description").AsString())
	desired := inputs.Set("name", property.New("renamed"))
	before := created.Properties
	count := len(api.Requests())
	updated, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: desired})
	require.NoError(t, e)
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, fakeapi.Object{"name": "renamed"}, api.Last().Body, "rename must not clear undeclared observations")
	require.Equal(t, "external description", updated.Properties.Get("description").AsString())
	require.Equal(t, "kept", updated.Properties.Get("metadata").AsMap().Get("external").AsString())
	partial = true
	count = len(api.Requests())
	incomplete, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: inputs, Inputs: desired})
	require.Error(t, e)
	require.Nil(t, incomplete.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, incomplete.Properties.Len())
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, before, created.Properties)
}

func TestEnvironmentMetadataRemovalUsesCheckedOwnership(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	inputs := providertest.Props(map[string]any{"name": "sandbox", "description": "managed", "config": map[string]any{"type": "self_hosted"}, "metadata": map[string]any{"owned": "remove"}})
	created, checked := providertest.Create(t, s, environmentURN, inputs)
	// An unowned observation in engine-supplied state is not deletion permission.
	api.Mutate("/v1/environments/"+created.ID, func(o fakeapi.Object) { o["metadata"] = map[string]any{"owned": "remove", "external": "keep"} })
	refreshed := providertest.Read(t, s, environmentURN, created, checked)
	require.Equal(t, "keep", refreshed.Properties.Get("metadata").AsMap().Get("external").AsString())
	require.True(t, refreshed.Inputs.Get("metadata").AsMap().Get("external").IsNull(), "refresh must not adopt external key deletion ownership")
	desired := checked.Delete("description").Set("metadata", property.New(providertest.Props(map[string]any{"desired": "send"})))
	_, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: refreshed.Properties, OldInputs: refreshed.Inputs, Inputs: desired})
	require.NoError(t, e)
	require.Equal(t, fakeapi.Object{"name": "sandbox", "description": nil, "metadata": map[string]any{"owned": nil, "desired": "send"}}, api.Last().Body)
}

func TestEnvironmentAllPackageListsRemainManaged(t *testing.T) {
	s, api := providertest.New(t)
	environmentServerBehavior(api)
	raw := providertest.Props(map[string]any{"name": "sandbox", "description": "description", "metadata": map[string]any{}, "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "unrestricted"}}})
	created, checked := providertest.Create(t, s, environmentURN, raw)
	require.True(t, raw.Get("config").AsMap().Get("packages").IsNull(), "Check must not mutate the original bag")
	packages := checked.Get("config").AsMap().Get("packages").AsMap()
	require.Equal(t, 6, packages.Len())
	for _, key := range []string{"apt", "cargo", "gem", "go", "npm", "pip"} {
		require.True(t, packages.Get(key).IsArray())
		require.Zero(t, packages.Get(key).AsArray().Len())
	}
	again, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: checked})
	require.NoError(t, e)
	require.Equal(t, checked, again.Inputs)
	read := providertest.Read(t, s, environmentURN, created, checked)
	require.Equal(t, checked, read.Inputs)
	converged := providertest.Diff(t, s, environmentURN, p.CreateResponse{ID: created.ID, Properties: read.Properties}, read.Inputs, checked)
	require.False(t, converged.HasChanges)
	api.Mutate("/v1/environments/"+created.ID, func(o fakeapi.Object) {
		o["config"].(map[string]any)["packages"].(map[string]any)["apt"] = []any{"external-package"}
	})
	drift := providertest.Read(t, s, environmentURN, created, checked)
	require.Equal(t, "external-package", drift.Inputs.Get("config").AsMap().Get("packages").AsMap().Get("apt").AsArray().Get(0).AsString())
	d := providertest.Diff(t, s, environmentURN, p.CreateResponse{ID: created.ID, Properties: drift.Properties}, drift.Inputs, checked)
	require.True(t, d.HasChanges)
	require.Equal(t, p.Delete, d.DetailedDiff["config.packages.apt[0]"].Kind, "the engine must remove independently added managed packages")
}

func TestEnvironmentScopeObservationValidity(t *testing.T) {
	s, api := providertest.New(t)
	obj := fakeapi.Object{"id": "env_scope", "name": "sandbox", "description": nil, "metadata": map[string]any{}, "config": map[string]any{"type": "self_hosted"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	for _, invalid := range []any{map[string]any{"invalid": true}, []any{}, false, 42, "typo"} {
		obj["scope"] = invalid
		_, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_scope"})
		require.Error(t, e)
	}
	for _, valid := range []string{"organization", "account"} {
		obj["scope"] = valid
		read, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_scope"})
		require.NoError(t, e)
		require.Equal(t, valid, read.Inputs.Get("scope").AsString())
	}
	// Optional scope may be omitted, but the response contract does not permit null.
	obj["scope"] = nil
	_, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_scope"})
	require.Error(t, e)
	delete(obj, "scope")
	read, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_scope"})
	require.NoError(t, e)
	require.True(t, read.Inputs.Get("scope").IsNull())
}

func TestEnvironmentPackageCanonicalMetadata(t *testing.T) {
	s, api := providertest.New(t)
	dep := []resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::packages"}
	pip := property.New([]property.Value{property.New("pandas").WithSecret(true).WithDependencies(dep)})
	packages := property.New(property.NewMap(map[string]property.Value{"pip": pip})).WithSecret(true).WithDependencies(dep)
	config := property.New(property.NewMap(map[string]property.Value{"type": property.New("cloud"), "packages": packages})).WithDependencies(dep)
	in := property.NewMap(map[string]property.Value{"name": property.New("sandbox"), "config": config})
	checked, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: in})
	require.NoError(t, e)
	require.Empty(t, checked.Failures)
	c := checked.Inputs.Get("config")
	require.Equal(t, dep, c.Dependencies())
	v := c.AsMap().Get("packages")
	require.True(t, v.Secret())
	require.Equal(t, dep, v.Dependencies())
	require.Equal(t, pip, v.AsMap().Get("pip"))
	require.Equal(t, 6, v.AsMap().Len())
	again, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: checked.Inputs})
	require.NoError(t, e)
	require.Equal(t, checked.Inputs, again.Inputs)
	require.Equal(t, 1, in.Get("config").AsMap().Get("packages").AsMap().Len())
	require.Empty(t, api.Requests())
}

func environmentStringCollectionResponse() fakeapi.Object {
	return fakeapi.Object{"id": "env_collections", "name": "sandbox", "description": nil, "scope": "organization", "metadata": map[string]any{"good": "value"}, "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{"observed.example"}, "allow_mcp_servers": true, "allow_package_managers": true}, "packages": map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{"observed-package"}}}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
}

func TestEnvironmentRejectsCoercedResponseIdentity(t *testing.T) {
	s, api := providertest.New(t)
	obj := environmentStringCollectionResponse()
	obj["id"] = 42
	handler := func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) }
	api.Handle("POST /v1/environments", handler)
	api.Handle("GET /v1/environments/{id}", handler)
	created, err := s.Create(p.CreateRequest{Urn: environmentURN, Properties: providertest.Props(map[string]any{"name": "sandbox"})})
	require.Error(t, err)
	require.Empty(t, created.ID)
	require.Nil(t, created.PartialState, "a coerced numeric ID does not establish usable allocation identity")
	_, err = s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_collections"})
	require.Error(t, err)
}

func TestEnvironmentNestedStringResponseValidation(t *testing.T) {
	s, api := providertest.New(t)
	obj := environmentStringCollectionResponse()
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	for _, field := range []string{"metadata", "hosts", "apt", "cargo", "gem", "go", "npm", "pip"} {
		t.Run(field, func(t *testing.T) {
			assign := func(value any) {
				obj = environmentStringCollectionResponse()
				c := obj["config"].(map[string]any)
				switch field {
				case "metadata":
					obj["metadata"] = map[string]any{"valid": "observation", "invalid": value}
				case "hosts":
					c["networking"].(map[string]any)["allowed_hosts"] = []any{"valid.example", value}
				default:
					c["packages"].(map[string]any)[field] = []any{"valid-package", value}
				}
			}
			for _, bad := range []any{42, true, nil, map[string]any{}, []any{}} {
				assign(bad)
				_, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_collections"})
				require.Error(t, e, "non-string elements must not become observations")
			}
			for _, empty := range []bool{true, false} {
				obj = environmentStringCollectionResponse()
				c := obj["config"].(map[string]any)
				values := []any{}
				if !empty {
					values = []any{"literal-string"}
				}
				switch field {
				case "metadata":
					obj["metadata"] = map[string]any{}
					if !empty {
						obj["metadata"] = map[string]any{"key": "literal-string"}
					}
				case "hosts":
					c["networking"].(map[string]any)["allowed_hosts"] = values
				default:
					c["packages"].(map[string]any)[field] = values
				}
				read, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_collections"})
				require.NoError(t, e)
				require.Equal(t, "env_collections", read.ID)
				if field == "metadata" {
					require.Equal(t, !empty, read.Properties.Get("metadata").AsMap().Len() > 0)
				} else {
					var list property.Value
					if field == "hosts" {
						list = read.Properties.Get("config").AsMap().Get("networking").AsMap().Get("allowedHosts")
					} else {
						list = read.Properties.Get("config").AsMap().Get("packages").AsMap().Get(field)
					}
					require.True(t, list.IsArray())
					if empty {
						require.Zero(t, list.AsArray().Len())
					} else {
						require.Equal(t, "literal-string", list.AsArray().Get(0).AsString())
					}
				}
			}
		})
	}
}

func TestEnvironmentMalformedNestedStringsRejectMutationResponse(t *testing.T) {
	s, api := providertest.New(t)
	original := environmentStringCollectionResponse()
	original["metadata"] = map[string]any{"bad": "old", "remove": "old", "external": "keep"}
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, original) })
	response := environmentStringCollectionResponse()
	response["metadata"] = map[string]any{"bad": 42, "external": 42, "remove": 42, "observed": "server"}
	config := response["config"].(map[string]any)
	config["networking"].(map[string]any)["allowed_hosts"] = []any{42}
	config["packages"].(map[string]any)["pip"] = []any{42}
	// A valid sibling manager is an observation even when another list is malformed.
	config["packages"].(map[string]any)["apt"] = []any{"observed-apt"}
	api.Handle("POST /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, response) })
	inputs := baseEnvironment()
	inputs["metadata"] = map[string]any{"bad": "old", "remove": "old"}
	created, checked := providertest.Create(t, s, environmentURN, providertest.Props(inputs))
	desired := checked.Set("metadata", property.New(providertest.Props(map[string]any{"bad": "accepted"})))
	count := len(api.Requests())
	partial, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Zero(t, partial.Properties.Len())
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, map[string]any{"bad": "accepted", "remove": nil}, api.Last().Body["metadata"])
	// Omitted networking must still remain absent from the request.
	unmanaged := providertest.Props(map[string]any{"name": "sandbox", "config": map[string]any{"type": "cloud"}, "metadata": map[string]any{"bad": "old", "remove": "old"}})
	checkedUnmanaged, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: unmanaged})
	require.NoError(t, e)
	config["networking"].(map[string]any)["allow_mcp_servers"] = false
	count = len(api.Requests())
	partial, e = s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: checkedUnmanaged.Inputs, Inputs: checkedUnmanaged.Inputs})
	require.Error(t, e)
	require.Len(t, api.Requests(), count+1)
	require.Nil(t, partial.PartialState)
	require.Zero(t, partial.Properties.Len())
	require.NotContains(t, api.Last().Body["config"].(map[string]any), "networking")
	// Entirely omitted config remains absent as well.
	nameOnly := providertest.Props(map[string]any{"name": "sandbox"})
	count = len(api.Requests())
	partial, e = s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: nameOnly, Inputs: nameOnly})
	require.Error(t, e)
	require.Nil(t, partial.PartialState)
	require.Zero(t, partial.Properties.Len())
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, fakeapi.Object{"name": "sandbox"}, api.Last().Body)
	// Create also returns an ordinary error without reconstructed state.
	original = response
	allocated, e := s.Create(p.CreateRequest{Urn: environmentURN, Properties: checked})
	require.Error(t, e)
	require.Nil(t, allocated.PartialState)
	require.NotContains(t, e.Error(), "failed to initialize")
	require.Empty(t, allocated.ID)
	require.Zero(t, allocated.Properties.Len())
}

func TestEnvironmentMutationNetworkFlags(t *testing.T) {
	for _, mode := range []string{"name-only", "cloud-without-networking", "managed-networking"} {
		t.Run(mode, func(t *testing.T) {
			s, api := providertest.New(t)
			api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
				fakeapi.WriteJSON(w, environmentStringCollectionResponse())
			})
			response := environmentStringCollectionResponse()
			api.Handle("POST /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, response) })
			inputs := map[string]any{"name": "sandbox"}
			if mode == "cloud-without-networking" {
				inputs["config"] = map[string]any{"type": "cloud"}
			}
			if mode == "managed-networking" {
				inputs = baseEnvironment()
			}
			created, checked := providertest.Create(t, s, environmentURN, providertest.Props(inputs))
			desired := checked.Set("name", property.New("renamed"))
			if mode == "managed-networking" {
				c := desired.Get("config").AsMap()
				n := c.Get("networking").AsMap().Set("allowMcpServers", property.New(false)).Set("allowPackageManagers", property.New(false))
				c = c.Set("networking", property.New(n)).Set("packages", property.New(providertest.Props(map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{}})))
				desired = desired.Set("config", property.New(c))
			}
			snapshot := created.Properties
			for _, flag := range []struct{ wire, property, sibling string }{{"allow_mcp_servers", "allowMcpServers", "allow_package_managers"}, {"allow_package_managers", "allowPackageManagers", "allow_mcp_servers"}} {
				for _, kind := range []string{"missing", "object", "string", "number", "null", "false", "true"} {
					t.Run(flag.property+"/"+kind, func(t *testing.T) {
						response = environmentStringCollectionResponse()
						response["name"] = "renamed"
						n := response["config"].(map[string]any)["networking"].(map[string]any)
						n["allowed_hosts"] = []any{"fresh.example"}
						n[flag.sibling] = false
						switch kind {
						case "missing":
							delete(n, flag.wire)
						case "object":
							n[flag.wire] = map[string]any{"invalid": true}
						case "string":
							n[flag.wire] = "true"
						case "number":
							n[flag.wire] = 1
						case "null":
							n[flag.wire] = nil
						case "false":
							n[flag.wire] = false
						case "true":
							n[flag.wire] = true
						}
						count := len(api.Requests())
						updated, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
						if kind == "true" || kind == "false" {
							require.NoError(t, e)
						} else {
							require.Error(t, e)
							require.Nil(t, updated.PartialState)
							require.NotContains(t, e.Error(), "failed to initialize")
							require.Zero(t, updated.Properties.Len())
						}
						require.Len(t, api.Requests(), count+1)
						require.Equal(t, http.MethodPost, api.Last().Method)
						if e == nil {
							network := updated.Properties.Get("config").AsMap().Get("networking").AsMap()
							require.Equal(t, kind == "true", network.Get(flag.property).AsBool())
							sibling := "allowPackageManagers"
							if flag.property == "allowPackageManagers" {
								sibling = "allowMcpServers"
							}
							require.False(t, network.Get(sibling).AsBool())
							require.Equal(t, "fresh.example", network.Get("allowedHosts").AsArray().Get(0).AsString())
						}
						if mode != "managed-networking" {
							if mode == "name-only" {
								require.Equal(t, fakeapi.Object{"name": "renamed"}, api.Last().Body)
							} else {
								require.NotContains(t, api.Last().Body["config"].(map[string]any), "networking")
							}
						}
						require.Equal(t, snapshot, created.Properties)
					})
				}
			}
		})
	}
}

func TestEnvironmentReadRejectsCoercedNetworkFlags(t *testing.T) {
	s, api := providertest.New(t)
	obj := environmentStringCollectionResponse()
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	for _, flag := range []string{"allow_mcp_servers", "allow_package_managers"} {
		for _, bad := range []any{1, "true", map[string]any{"invalid": true}} {
			obj = environmentStringCollectionResponse()
			obj["config"].(map[string]any)["networking"].(map[string]any)[flag] = bad
			_, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_collections"})
			require.Error(t, e, "non-boolean JSON must not become a permission observation")
		}
	}
}

func TestEnvironmentUnusableMutationResponse(t *testing.T) {
	s, api := providertest.New(t)
	body := `{"id":"env_partial_fields","name":"observed","description":null,"scope":"organization","metadata":{"observed":"server"},"config":{"type":"cloud","networking":{"type":"limited","allowed_hosts":["observed.example"],"allow_mcp_servers":{"invalid":true}},"packages":{"pip":["observed-package"],"apt":{"invalid":true}}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","archived_at":null}`
	handler := func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	api.Handle("POST /v1/environments", handler)
	api.Handle("POST /v1/environments/{id}", handler)
	inputs := providertest.Props(baseEnvironment())
	out, err := s.Create(p.CreateRequest{Urn: environmentURN, Properties: inputs})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Nil(t, out.PartialState)
	require.Empty(t, out.ID)
	require.Zero(t, out.Properties.Len())
	// Use independently refreshed state rather than resuming a failed Create.
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteJSON(w, environmentStringCollectionResponse())
	})
	read, err := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_collections", Inputs: inputs})
	require.NoError(t, err)
	count := len(api.Requests())
	updated, err := s.Update(p.UpdateRequest{Urn: environmentURN, ID: read.ID, State: read.Properties, OldInputs: inputs, Inputs: inputs})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Nil(t, updated.PartialState)
	require.Zero(t, updated.Properties.Len())
	require.Len(t, api.Requests(), count+1)
	require.Equal(t, http.MethodPost, api.Last().Method)
}

func TestEnvironmentRejectedMutationDoesNotAcceptDesiredState(t *testing.T) {
	s, api := providertest.New(t)
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		fakeapi.WriteError(w, 400, "synthetic rejected configuration")
	})
	result, err := s.Create(p.CreateRequest{Urn: environmentURN, Properties: providertest.Props(baseEnvironment())})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "failed to initialize")
	require.Empty(t, result.ID)
	require.Nil(t, result.PartialState)
	require.True(t, result.Properties.Get("name").IsNull())
}

func TestEnvironmentEmptyListPresence(t *testing.T) {
	s, api := providertest.New(t)
	obj := fakeapi.Object{"id": "env_presence", "name": "sandbox", "description": nil, "scope": "organization", "metadata": map[string]any{}, "archived_at": nil, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited", "allowed_hosts": []any{}, "allow_mcp_servers": false, "allow_package_managers": false}, "packages": map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{}}}}
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	for _, empty := range []bool{false, true} {
		n := map[string]any{"type": "limited", "allowMcpServers": false, "allowPackageManagers": false}
		if empty {
			n["allowedHosts"] = []any{}
		}
		in := providertest.Props(map[string]any{"name": "sandbox", "config": map[string]any{"type": "cloud", "networking": n}})
		created, checked := providertest.Create(t, s, environmentURN, in)
		wire := api.Last().Body["config"].(map[string]any)["networking"].(map[string]any)
		_, present := wire["allowed_hosts"]
		require.Equal(t, empty, present, "omitted and explicitly empty lists must remain distinct on Create")
		if empty {
			require.Equal(t, []any{}, wire["allowed_hosts"])
		}
		read := providertest.Read(t, s, environmentURN, created, checked)
		require.Equal(t, checked, read.Inputs, "canonical cloud inputs manage all package lists")
		if empty {
			obj["config"].(map[string]any)["networking"].(map[string]any)["allowed_hosts"] = []any{"drift.example"}
			drift := providertest.Read(t, s, environmentURN, created, checked)
			require.Equal(t, "drift.example", drift.Inputs.Get("config").AsMap().Get("networking").AsMap().Get("allowedHosts").AsArray().Get(0).AsString())
		}
	}
}

func TestEnvironmentKnownOutputMetadata(t *testing.T) {
	s, api := providertest.New(t)
	for _, unknown := range []bool{false, true} {
		for _, secret := range []bool{false, true} {
			v := property.New(true)
			if unknown {
				v = property.New(property.Computed)
			}
			v = v.WithSecret(secret).WithDependencies([]resourceurn.URN{"urn:pulumi:s::p::test:index:Dependency::permission"})
			in := baseEnvironment()
			in["config"].(map[string]any)["networking"].(map[string]any)["allowPackageManagers"] = v
			inputs := providertest.Props(in)
			checked, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: inputs})
			require.NoError(t, e)
			require.Empty(t, checked.Failures)
			require.Equal(t, inputs, checked.Inputs)
			again, e := s.Check(p.CheckRequest{Urn: environmentURN, Inputs: checked.Inputs})
			require.NoError(t, e)
			require.Equal(t, checked.Inputs, again.Inputs)
			require.Equal(t, v, inputs.Get("config").AsMap().Get("networking").AsMap().Get("allowPackageManagers"))
		}
	}
	require.Empty(t, api.Requests())
}

func TestEnvironmentReadDoesNotEraseOnMalformedResponse(t *testing.T) {
	s, api := providertest.New(t)
	obj := fakeapi.Object{"id": "env_bad", "name": "sandbox", "description": nil, "scope": "organization", "metadata": map[string]any{}, "config": map[string]any{"type": "self_hosted"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": "not-a-timestamp"}
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	_, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_bad"})
	require.Error(t, e)
	obj["archived_at"] = nil
	obj["config"] = map[string]any{"type": "cloud", "networking": map[string]any{"type": "limited", "allowed_hosts": "not-an-array"}}
	_, e = s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_bad"})
	require.Error(t, e)
	obj["config"] = map[string]any{"type": "self_hosted"}
	obj["archived_at"] = "2026-01-01T00:00:00Z"
	absent, e := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_bad"})
	require.NoError(t, e)
	require.Empty(t, absent.ID)
}

func TestEnvironmentIncompleteUpdatePreservesRequestOwnership(t *testing.T) {
	s, api := providertest.New(t)
	full := `{"id":"env_unchanged","name":"sandbox","description":"external description","scope":"organization","metadata":{"owned":"old","remove":"old","external":"keep"},"config":{"type":"cloud","networking":{"type":"limited","allowed_hosts":["observed.example"],"allow_mcp_servers":true,"allow_package_managers":true},"packages":{"apt":[],"cargo":[],"gem":[],"go":[],"npm":[],"pip":["server-package"]}},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","archived_at":null}`
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(full))
	})
	variant := "missing"
	api.Handle("POST /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) {
		reply := fakeapi.Object{"id": "env_unchanged", "name": "renamed", "updated_at": "2026-01-02T00:00:00Z", "archived_at": nil}
		if variant != "missing" {
			reply["config"] = 42
			if variant == "malformed-networking" {
				reply["config"] = map[string]any{"type": "cloud", "networking": 42}
			}
			reply["metadata"], reply["description"], reply["scope"] = 42, 42, 42
		}
		fakeapi.WriteJSON(w, reply)
	})
	in := providertest.Props(map[string]any{"name": "sandbox", "config": map[string]any{"type": "cloud"}, "metadata": map[string]any{"owned": "old", "remove": "old"}})
	created, checked := providertest.Create(t, s, environmentURN, in)
	desired := checked.Set("name", property.New("renamed")).Set("metadata", property.New(providertest.Props(map[string]any{"owned": "new"})))
	for _, mode := range []string{"missing", "malformed", "malformed-networking"} {
		variant = mode
		t.Run(mode, func(t *testing.T) {
			count := len(api.Requests())
			updated, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
			require.Error(t, e)
			require.Nil(t, updated.PartialState)
			require.NotContains(t, e.Error(), "failed to initialize")
			require.Zero(t, updated.Properties.Len())
			require.Len(t, api.Requests(), count+1)
			wire := api.Last().Body
			require.NotContains(t, wire, "description")
			require.Equal(t, map[string]any{"owned": "new", "remove": nil}, wire["metadata"])
			require.NotContains(t, wire["config"].(map[string]any), "networking")
		})
	}
}

func TestEnvironmentReadRejectsCoercedStringFields(t *testing.T) {
	s, api := providertest.New(t)
	obj := fakeapi.Object{"id": "env_coerced", "name": "sandbox", "description": "description", "scope": "organization", "metadata": map[string]any{}, "config": map[string]any{"type": "self_hosted"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, obj) })
	for _, field := range []string{"name", "description", "scope"} {
		prior := obj[field]
		obj[field] = 42
		_, err := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_coerced"})
		require.Error(t, err, "numeric JSON must not be accepted as a readable string: "+field)
		obj[field] = prior
	}
}

func TestEnvironmentRejectsNullNonnullableResponseFields(t *testing.T) {
	base := fakeapi.Object{"id": "env_nullability", "name": "sandbox", "description": nil, "scope": "organization", "metadata": map[string]any{}, "config": map[string]any{"type": "cloud", "networking": map[string]any{"type": "unrestricted"}, "packages": map[string]any{"apt": []any{}, "cargo": []any{}, "gem": []any{}, "go": []any{}, "npm": []any{}, "pip": []any{}}}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	for _, tc := range []struct {
		name   string
		change func(fakeapi.Object)
	}{
		{"cloud networking", func(o fakeapi.Object) { o["config"].(map[string]any)["networking"] = nil }},
		{"cloud packages", func(o fakeapi.Object) { o["config"].(map[string]any)["packages"] = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, api := providertest.New(t)
			response := maps.Clone(base)
			response["config"] = maps.Clone(base["config"].(map[string]any))
			tc.change(response)
			api.Handle("GET /v1/environments/{id}", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) { fakeapi.WriteJSON(w, response) })
			_, err := s.Read(p.ReadRequest{Urn: environmentURN, ID: "env_nullability"})
			require.Error(t, err)
		})
	}
}

func TestEnvironmentMalformedUntouchedFacts(t *testing.T) {
	s, api := providertest.New(t)
	original := fakeapi.Object{"id": "env_unusable", "name": "sandbox", "description": "external", "metadata": map[string]any{"external": "keep"}, "scope": "organization", "config": map[string]any{"type": "self_hosted"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z", "archived_at": nil}
	api.Handle("POST /v1/environments", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, original) })
	response := fakeapi.Object{}
	api.Handle("POST /v1/environments/{id}", func(w http.ResponseWriter, r *http.Request, b fakeapi.Object) { fakeapi.WriteJSON(w, response) })
	created, checked := providertest.Create(t, s, environmentURN, providertest.Props(map[string]any{"name": "sandbox"}))
	desired := checked.Set("name", property.New("renamed"))
	snapshot := created.Properties
	for _, variant := range []string{"malformed", "unknown-variants", "optional-nulls", "valid-observations"} {
		t.Run(variant, func(t *testing.T) {
			// Even otherwise valid responses need an actual creation timestamp.
			response = fakeapi.Object{"id": created.ID, "name": "renamed", "description": 42, "metadata": 42, "scope": 42, "config": 42, "updated_at": "2026-01-02T00:00:00Z", "archived_at": nil}
			switch variant {
			case "unknown-variants":
				response["config"] = map[string]any{"type": "future"}
				response["scope"] = "future"
			case "optional-nulls":
				response["description"] = nil
				response["scope"] = nil
			case "valid-observations":
				response["config"] = map[string]any{"type": "self_hosted"}
				response["metadata"] = map[string]any{"observed": "new"}
				response["description"] = nil
				response["scope"] = "account"
			}
			count := len(api.Requests())
			partial, e := s.Update(p.UpdateRequest{Urn: environmentURN, ID: created.ID, State: created.Properties, OldInputs: checked, Inputs: desired})
			require.Error(t, e)
			require.Nil(t, partial.PartialState)
			require.NotContains(t, e.Error(), "failed to initialize")
			require.Zero(t, partial.Properties.Len())
			require.Len(t, api.Requests(), count+1)
			require.Equal(t, fakeapi.Object{"name": "renamed"}, api.Last().Body, "no unmanaged mutation or internal GET")
			require.Equal(t, snapshot, created.Properties)
		})
	}
}
