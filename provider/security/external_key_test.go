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

package security_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const externalKeyToken = "anthropic:security:ExternalKey"

var externalKeyURN = resource.NewURN("test", "test", "", externalKeyToken, "key")

const keysPath = "/v1/organizations/external_keys"

// externalKeyServerBehavior mimics the API's defaults and answers validation with the
// given outcome.
func externalKeyServerBehavior(api *fakeapi.Server, validationError string) {
	api.SetHooks("external_keys", fakeapi.Hooks{OnUpdate: func(_, after, _ fakeapi.Object) error {
		if _, ok := after["display_name"]; !ok {
			after["display_name"] = nil
		}
		return nil
	}, OnCreate: func(obj fakeapi.Object) {
		obj["attachment"] = map[string]any{"type": "unattached"}
		obj["geo"] = "us"
		if _, ok := obj["user_profile_id"]; !ok {
			obj["user_profile_id"] = nil
		}
		if obj["display_name"] == nil {
			obj["display_name"] = "Generated name"
		}
	}})
	api.Handle("POST "+keysPath+"/{id}/validate", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		if validationError != "" {
			fakeapi.WriteJSON(w, fakeapi.Object{"type": "external_key_validation", "status": "failure", "error": validationError})
			return
		}
		fakeapi.WriteJSON(w, fakeapi.Object{"type": "external_key_validation", "status": "success", "error": nil})
	})
}

func awsKey(arn string) map[string]any {
	return map[string]any{
		"displayName":    "Production CMEK",
		"providerConfig": map[string]any{"type": "aws", "kmsArn": arn},
	}
}

func TestExternalKeyLifecycle(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "")

	renamed := awsKey("arn:aws:kms:us-east-1:111122223333:key/k1")
	renamed["displayName"] = "Primary CMEK"

	integration.LifeCycleTest{
		Resource: externalKeyToken,
		Create: integration.Operation{
			Inputs: providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1")),
			Hook: func(_, output property.Map) {
				req := api.LastRequest(t, http.MethodPost, keysPath)
				assert.Equal(t, fakeapi.Object{
					"display_name":    "Production CMEK",
					"provider_config": map[string]any{"type": "aws", "kms_arn": "arn:aws:kms:us-east-1:111122223333:key/k1"},
				}, req.Body)
				assert.Equal(t, "sk-ant-admin-test", req.Header.Get("x-api-key"))
				assert.Regexp(t, `^`+keysPath+`/external_key_\d+/validate$`, api.Requests()[len(api.Requests())-1].Path,
					"a new key is validated")
				assert.Equal(t, "unattached", output.Get("attachment").AsString())
				_, hasGeo := output.GetOk("geo")
				assert.True(t, hasGeo, "state includes the observed geography")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(renamed),
			Hook: func(_, _ property.Map) {
				assert.Equal(t, fakeapi.Object{"display_name": "Primary CMEK"}, api.LastRequest(t, http.MethodPost, keysPath+"/external_key_0001").Body)
			},
		}},
	}.Run(t, server)

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodDelete, last.Method, "deleting a key deletes it")
}

func TestExternalKeyValidationFailure(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "kms:Decrypt was denied")

	_, err := server.Create(p.CreateRequest{Urn: externalKeyURN,
		Properties: providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "kms:Decrypt was denied")

	last := api.Requests()[len(api.Requests())-1]
	assert.Equal(t, http.MethodDelete, last.Method, "a key that fails validation is not left behind")
	assert.Nil(t, api.Object(last.Path))
}

func TestExternalKeyRefreshAndReplace(t *testing.T) {
	t.Parallel()
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "")

	inputs := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))
	check, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: inputs})
	require.NoError(t, err)
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: check.Inputs})
	require.NoError(t, err)

	read, err := server.Read(p.ReadRequest{ID: created.ID, Urn: externalKeyURN,
		Properties: created.Properties, Inputs: check.Inputs})
	require.NoError(t, err)
	assert.Equal(t, check.Inputs, read.Inputs)

	api.Mutate(keysPath+"/"+created.ID, func(obj fakeapi.Object) {
		obj["attachment"] = map[string]any{"type": "attached"}
	})
	read, err = server.Read(p.ReadRequest{ID: created.ID, Urn: externalKeyURN,
		Properties: created.Properties, Inputs: check.Inputs})
	require.NoError(t, err)
	assert.Equal(t, "attached", read.Properties.Get("attachment").AsString())

	moved := providertest.Props(awsKey("arn:aws:kms:us-west-2:111122223333:key/k2"))
	check, err = server.Check(p.CheckRequest{Urn: externalKeyURN, State: created.Properties, Inputs: moved})
	require.NoError(t, err)
	d, err := server.Diff(p.DiffRequest{ID: created.ID, Urn: externalKeyURN, State: created.Properties, Inputs: check.Inputs})
	require.NoError(t, err)
	assert.Equal(t, p.UpdateReplace, d.DetailedDiff["providerConfig"].Kind, "moving the key replaces it")
}

// Synthetic SDK response shapes, not observed KMS behavior.
func externalKeyObject(id string) fakeapi.Object {
	return fakeapi.Object{"id": id, "type": "external_key", "display_name": "Production CMEK", "geo": "us", "user_profile_id": "up_customer", "provider_config": map[string]any{"type": "aws", "kms_arn": "arn:aws:kms:us-east-1:111122223333:key/k1", "region": "us-east-1"}, "attachment": map[string]any{"type": "unattached"}, "created_at": "2026-01-01T00:00:00Z", "updated_at": "2026-01-01T00:00:00Z"}
}

func TestExternalKeyCheckMetadata(t *testing.T) {
	server, api := providertest.New(t)
	dep := []resource.URN{"urn:pulumi:test::test::test:input:Input::cloud"}
	input := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))
	config := input.Get("providerConfig").AsMap().Set("kmsArn", property.New(property.Computed).WithSecret(true).WithDependencies(dep))
	input = input.Set("providerConfig", property.New(config).WithDependencies(dep)).Set("displayName", property.New(property.Computed).WithSecret(true).WithDependencies(dep))
	original := input
	checked, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: input})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, input, checked.Inputs)
	require.Equal(t, original, input)
	again, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: checked.Inputs})
	require.NoError(t, err)
	require.Equal(t, checked.Inputs, again.Inputs)
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(created.Properties))
	configOutput := created.Properties.Get("providerConfig")
	require.True(t, configOutput.IsMap())
	require.Equal(t, "aws", configOutput.AsMap().Get("type").AsString())
	require.True(t, configOutput.AsMap().Get("kmsArn").IsComputed())
	require.True(t, configOutput.Secret() || configOutput.AsMap().Get("kmsArn").Secret())
	require.True(t, created.Properties.Get("displayName").IsComputed())
	require.True(t, created.Properties.Get("displayName").Secret())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: "ekey_one", State: created.Properties, Inputs: input, DryRun: true})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(updated.Properties))
	require.True(t, updated.Properties.Get("displayName").IsComputed())
	require.True(t, updated.Properties.Get("displayName").Secret())
	require.Empty(t, api.Requests())
	for _, c := range []struct {
		input map[string]any
		path  string
	}{
		{map[string]any{"providerConfig": map[string]any{"type": "aws"}}, "providerConfig.kmsArn"},
		{map[string]any{"providerConfig": map[string]any{"type": "gcp", "keyName": "key", "region": "region"}}, "providerConfig.region"},
		{map[string]any{"providerConfig": map[string]any{"type": "azure", "keyName": "key", "tenantId": "tenant"}}, "providerConfig.vaultUri"},
		{map[string]any{"providerConfig": map[string]any{"type": "other"}}, "providerConfig.type"},
		{map[string]any{"providerConfig": map[string]any{"type": "gcp", "keyName": "key"}, "geo": "eu"}, "geo"},
	} {
		out, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: providertest.Props(c.input)})
		require.NoError(t, err)
		found := false
		for _, f := range out.Failures {
			found = found || f.Property == c.path
		}
		require.True(t, found, "%s: %v", c.path, out.Failures)
	}
	for _, v := range []property.Value{property.New(property.Computed), property.New(property.NewMap(map[string]property.Value{"type": property.New(property.Computed)}))} {
		checked, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: property.NewMap(map[string]property.Value{"providerConfig": v})})
		require.NoError(t, err)
		require.Empty(t, checked.Failures)
	}
	require.Empty(t, api.Requests())
}

func TestExternalKeyCloudMapping(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config map[string]any
		wire   fakeapi.Object
	}{
		{"aws", map[string]any{"type": "aws", "kmsArn": "arn:key", "region": "us-east-1"}, fakeapi.Object{"type": "aws", "kms_arn": "arn:key", "region": "us-east-1"}},
		{"gcp", map[string]any{"type": "gcp", "keyName": "projects/example/locations/us/keyRings/r/cryptoKeys/k"}, fakeapi.Object{"type": "gcp", "key_name": "projects/example/locations/us/keyRings/r/cryptoKeys/k"}},
		{"azure", map[string]any{"type": "azure", "keyName": "key", "tenantId": "tenant", "vaultUri": "https://synthetic.vault.azure.net", "clientId": "client"}, fakeapi.Object{"type": "azure", "key_name": "key", "tenant_id": "tenant", "vault_uri": "https://synthetic.vault.azure.net", "client_id": "client"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, api := providertest.New(t)
			externalKeyServerBehavior(api, "")
			input := providertest.Props(map[string]any{"providerConfig": tc.config, "userProfileId": "up_customer", "geo": "us", "displayName": "Name"})
			created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
			require.NoError(t, err)
			req := api.LastRequest(t, "POST", keysPath)
			require.Equal(t, fakeapi.Object{"provider_config": map[string]any(tc.wire), "user_profile_id": "up_customer", "geo": "us", "display_name": "Name"}, req.Body)
			require.Equal(t, "up_customer", created.Properties.Get("userProfileId").AsString())
			imported, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID})
			require.NoError(t, err)
			require.Equal(t, "up_customer", imported.Inputs.Get("userProfileId").AsString())
		})
	}
}

func TestExternalKeyValidationAndCleanupErrors(t *testing.T) {
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "KMS unavailable")
	api.Handle("DELETE "+keysPath+"/{id}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, 400, "synthetic cleanup denied")
	})
	input := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
	require.ErrorContains(t, err, "KMS unavailable")
	require.ErrorContains(t, err, "synthetic cleanup denied")
	require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
	require.Empty(t, created.ID)
	require.Nil(t, created.PartialState)
	require.Zero(t, created.Properties.Len())
	cleanup := api.Requests()[len(api.Requests())-1]
	require.Equal(t, "DELETE", cleanup.Method)
	id := strings.TrimPrefix(cleanup.Path, keysPath+"/")
	require.NotNil(t, api.Object(cleanup.Path), "failed compensation may leave an orphan")
	read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: id})
	require.NoError(t, err)
	baseline := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: id, State: read.Properties, Inputs: input})
	require.ErrorContains(t, err, "KMS unavailable")
	require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
	require.Nil(t, updated.PartialState)
	require.Zero(t, updated.Properties.Len())
	require.Len(t, api.Requests(), baseline+2)
	require.Equal(t, "POST", api.Requests()[baseline].Method)
	require.Equal(t, keysPath+"/"+id+"/validate", api.Requests()[baseline+1].Path)
	api.Handle("POST "+keysPath+"/"+id+"/validate", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, fakeapi.Object{"type": "external_key_validation", "status": "success", "error": nil})
	})
	baseline = len(api.Requests())
	updated, err = server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: id, State: read.Properties, Inputs: input})
	require.NoError(t, err)
	require.Nil(t, updated.PartialState)
	require.Len(t, api.Requests(), baseline+2)
	require.Equal(t, fakeapi.Object{"display_name": "Production CMEK"}, api.Requests()[baseline].Body)
	removed := input.Delete("displayName")
	updated, err = server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: id, State: updated.Properties, OldInputs: input, Inputs: removed})
	require.NoError(t, err)
	require.True(t, updated.Properties.Get("displayName").IsNull())
	require.Equal(t, fakeapi.Object{"display_name": nil}, api.LastRequest(t, "POST", keysPath+"/"+id).Body)
}

func TestExternalKeyReadAndReplacement(t *testing.T) {
	server, api := providertest.New(t)
	path := keysPath + "/ekey_one"
	obj := externalKeyObject("ekey_one")
	api.Put(path, obj)
	read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: "ekey_one"})
	require.NoError(t, err)
	require.Equal(t, "up_customer", read.Inputs.Get("userProfileId").AsString())
	for _, edit := range []func(property.Map) property.Map{
		func(m property.Map) property.Map { return m.Delete("userProfileId") },
		func(m property.Map) property.Map { return m.Delete("geo") },
		func(m property.Map) property.Map {
			return m.Set("providerConfig", property.New(m.Get("providerConfig").AsMap().Delete("region")))
		},
		func(m property.Map) property.Map {
			return m.Set("providerConfig", property.New(m.Get("providerConfig").AsMap().Set("kmsArn", property.New("arn:changed"))))
		},
	} {
		diff, err := server.Diff(p.DiffRequest{Urn: externalKeyURN, ID: read.ID, State: read.Properties, OldInputs: read.Inputs, Inputs: edit(read.Inputs)})
		require.NoError(t, err)
		require.True(t, diff.HasChanges)
		replacement := false
		for _, d := range diff.DetailedDiff {
			replacement = replacement || d.Kind == p.UpdateReplace || d.Kind == p.DeleteReplace
		}
		require.True(t, replacement, "%v", diff)
		require.False(t, diff.DeleteBeforeReplace)
	}
	again, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: read.ID, Inputs: read.Inputs, Properties: read.Properties})
	require.NoError(t, err)
	require.Equal(t, read, again)
	api.Mutate(path, func(o fakeapi.Object) { o["display_name"] = "outside" })
	drift, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: read.ID, Inputs: read.Inputs, Properties: read.Properties})
	require.NoError(t, err)
	require.Equal(t, "outside", drift.Inputs.Get("displayName").AsString())
	for _, mutate := range []func(fakeapi.Object){
		func(o fakeapi.Object) { o["id"] = "ekey_foreign" }, func(o fakeapi.Object) { o["display_name"] = false }, func(o fakeapi.Object) { o["attachment"] = map[string]any{"type": "other"} }, func(o fakeapi.Object) { o["provider_config"] = map[string]any{"type": "gcp", "key_name": false} }, func(o fakeapi.Object) { o["created_at"] = "not-time" }, func(o fakeapi.Object) { o["user_profile_id"] = 42 },
	} {
		obj := externalKeyObject("ekey_one")
		mutate(obj)
		api.Put(path, obj)
		_, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: read.ID})
		require.Error(t, err)
	}
	status := 403
	api.Handle("GET "+path, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, status, "synthetic error")
	})
	for _, code := range []int{403, 404, 500} {
		status = code
		out, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: read.ID})
		if status == 404 {
			require.NoError(t, err)
			require.Empty(t, out.ID)
		} else {
			require.Error(t, err)
		}
	}
}

func TestExternalKeyCreateFaults(t *testing.T) {
	t.Run("conservative retry", func(t *testing.T) {
		server, api := providertest.New(t)
		api.Handle("POST "+keysPath, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
			fakeapi.WriteError(w, 500, "ambiguous create failure")
		})
		_, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: providertest.Props(awsKey("arn:key"))})
		require.Error(t, err)
		require.Len(t, api.Requests(), 1, "generated New must not retry ambiguous 5xx")
	})
	t.Run("malformed observed output returns ordinary error", func(t *testing.T) {
		server, api := providertest.New(t)
		api.Handle("POST "+keysPath, func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
			obj := externalKeyObject("ekey_one")
			obj["updated_at"] = false
			obj["provider_config"].(map[string]any)["kms_arn"] = nil
			fakeapi.WriteJSON(w, obj)
		})
		input := providertest.Props(awsKey("arn:key"))
		created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
		require.Error(t, err)
		require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
		require.Nil(t, created.PartialState)
		require.Empty(t, created.ID)
		require.Zero(t, created.Properties.Len())
		require.Len(t, api.Requests(), 1, "an unusable response does not trigger validation or unsafe cleanup")
		require.Equal(t, fakeapi.Object{"display_name": "Production CMEK", "provider_config": map[string]any{"type": "aws", "kms_arn": "arn:key"}}, api.LastRequest(t, "POST", keysPath).Body)
	})
	t.Run("foreign update response", func(t *testing.T) {
		server, api := providertest.New(t)
		obj := externalKeyObject("ekey_one")
		api.Put(keysPath+"/ekey_one", obj)
		read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: "ekey_one"})
		require.NoError(t, err)
		api.Handle("POST "+keysPath+"/ekey_one", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
			fakeapi.WriteJSON(w, externalKeyObject("ekey_foreign"))
		})
		updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: read.ID, State: read.Properties, Inputs: read.Inputs.Set("displayName", property.New("new"))})
		require.Error(t, err)
		require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
		require.Nil(t, updated.PartialState)
		require.Zero(t, updated.Properties.Len())
		require.Len(t, api.Requests(), 2, "a foreign response must not be validated")
	})
}

func TestExternalKeyARNAndDeleteProtection(t *testing.T) {
	server, api := providertest.New(t)
	urn := providertest.URN("anthropic:security:ExternalKey")
	// SDK documents ARN identities on Claude Platform on AWS (including slashes).
	id := "arn:aws:kms:us-east-1:111122223333:key/audit"
	api.Handle("GET "+keysPath+"/{id...}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		fakeapi.WriteJSON(w, externalKeyObject(id))
	})
	read, err := server.Read(p.ReadRequest{Urn: urn, ID: id})
	require.NoError(t, err)
	require.Equal(t, id, read.ID)
	blocked := true
	api.Handle("DELETE "+keysPath+"/{id...}", func(w http.ResponseWriter, r *http.Request, _ fakeapi.Object) {
		require.Equal(t, keysPath+"/"+id, r.URL.Path)
		if blocked {
			fakeapi.WriteError(w, http.StatusBadRequest, "key is referenced by an archived workspace")
			return
		}
		fakeapi.WriteError(w, http.StatusNotFound, "already gone")
	})
	err = server.Delete(p.DeleteRequest{Urn: urn, ID: id, Properties: read.Properties})
	require.ErrorContains(t, err, "archived workspace")
	blocked = false
	err = server.Delete(p.DeleteRequest{Urn: urn, ID: id, Properties: read.Properties})
	require.NoError(t, err)
	// The custom ARN route matches all IDs; use a fresh server for absence.
	missingServer, missingAPI := providertest.New(t)
	missingAPI.Handle("GET "+keysPath+"/ekey_missing", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
		fakeapi.WriteError(w, http.StatusNotFound, "not found")
	})
	absent, err := missingServer.Read(p.ReadRequest{Urn: urn, ID: "ekey_missing"})
	require.NoError(t, err)
	require.Empty(t, absent.ID)
}

func TestExternalKeyLegacyIncompleteStateRefreshBeforeUpdate(t *testing.T) {
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "")
	input := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
	require.NoError(t, err)
	legacy := created.Properties.Delete("attachment").Delete("createdAt").Delete("updatedAt")
	read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID, Inputs: input, Properties: legacy})
	require.NoError(t, err)
	require.Equal(t, created.Properties, read.Properties)
	baseline := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: read.ID, State: read.Properties, OldInputs: input, Inputs: input})
	require.NoError(t, err)
	require.Equal(t, read.Properties.Get("providerConfig"), updated.Properties.Get("providerConfig"))
	require.Len(t, api.Requests(), baseline+2, "Update sends metadata and validates without an internal refresh")
	require.Equal(t, fakeapi.Object{"display_name": "Production CMEK"}, api.Requests()[baseline].Body)
}

func TestExternalKeyKnownMetadata(t *testing.T) {
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "")
	deps := []resource.URN{"urn:pulumi:test::test::test:config:Value::coordinates"}
	input := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1"))
	config := input.Get("providerConfig").AsMap()
	config = config.Set("kmsArn", config.Get("kmsArn").WithSecret(true).WithDependencies(deps))
	input = input.Set("providerConfig", property.New(config).WithDependencies(deps)).Set("displayName", input.Get("displayName").WithSecret(true).WithDependencies(deps))
	original := input
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(created.Properties))
	assertExternalKeySecretCoordinates(t, created.Properties.Get("providerConfig"))
	require.Equal(t, "Production CMEK", created.Properties.Get("displayName").AsString())
	require.True(t, created.Properties.Get("displayName").Secret())
	read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID, Inputs: input, Properties: created.Properties})
	require.NoError(t, err)
	assertExternalKeySecretCoordinates(t, read.Inputs.Get("providerConfig"))
	assertExternalKeyNoOutputDependencies(t, property.New(read.Properties))
	assertExternalKeySecretCoordinates(t, read.Properties.Get("providerConfig"))
	require.True(t, read.Properties.Get("displayName").Secret())
	desired := input.Set("displayName", property.New("Renamed CMEK").WithSecret(true).WithDependencies(deps))
	baseline := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: created.ID, State: read.Properties, OldInputs: input, Inputs: desired})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(updated.Properties))
	assertExternalKeySecretCoordinates(t, updated.Properties.Get("providerConfig"))
	require.Equal(t, "Renamed CMEK", updated.Properties.Get("displayName").AsString())
	require.True(t, updated.Properties.Get("displayName").Secret())
	require.Len(t, api.Requests(), baseline+2, "Update sends the name and validates, without internal refresh")
	require.Equal(t, original, input)

	// Read must not reintroduce dependencies from historical raw state, including
	// nested secret paths. Infer retains secrecy without forwarding dependencies.
	legacyConfig := read.Properties.Get("providerConfig")
	legacy := read.Properties.Set("providerConfig", property.New(legacyConfig.AsMap().Set("kmsArn", config.Get("kmsArn"))).WithSecret(legacyConfig.Secret()).WithDependencies(deps))
	refreshed, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID, Inputs: input, Properties: legacy})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(refreshed.Properties))
	assertExternalKeySecretCoordinates(t, refreshed.Properties.Get("providerConfig"))

	// Coordinates are not intrinsically secret: ID-only import recovers their
	// actual values without requiring artificial secret annotations.
	imported, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(imported.Properties))
	require.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/k1", imported.Properties.Get("providerConfig").AsMap().Get("kmsArn").AsString())
}

func TestExternalKeyWholeUnknownConfigPreview(t *testing.T) {
	server, api := providertest.New(t)
	deps := []resource.URN{"urn:pulumi:test::test::test:config:Value::coordinates"}
	input := property.NewMap(map[string]property.Value{
		"providerConfig": property.New(property.Computed).WithSecret(true).WithDependencies(deps),
	})
	checked, err := server.Check(p.CheckRequest{Urn: externalKeyURN, Inputs: input})
	require.NoError(t, err)
	require.Empty(t, checked.Failures)
	require.Equal(t, input, checked.Inputs)
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(created.Properties))
	require.True(t, created.Properties.Get("providerConfig").IsComputed())
	require.True(t, created.Properties.Get("providerConfig").Secret())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: "preview", State: created.Properties, Inputs: checked.Inputs, DryRun: true})
	require.NoError(t, err)
	assertExternalKeyNoOutputDependencies(t, property.New(updated.Properties))
	require.True(t, updated.Properties.Get("providerConfig").IsComputed())
	require.True(t, updated.Properties.Get("providerConfig").Secret())
	require.Empty(t, api.Requests())
}

func assertExternalKeySecretCoordinates(t *testing.T, v property.Value) {
	t.Helper()
	require.True(t, v.IsMap())
	arn := v.AsMap().Get("kmsArn")
	require.Equal(t, "arn:aws:kms:us-east-1:111122223333:key/k1", arn.AsString())
	require.True(t, v.Secret() || arn.Secret(), "coordinates must remain secret")
	require.Equal(t, "aws", v.AsMap().Get("type").AsString())
}

func assertExternalKeyNoOutputDependencies(t *testing.T, v property.Value) {
	t.Helper()
	require.Empty(t, v.Dependencies())
	if v.IsMap() {
		for _, child := range v.AsMap().All {
			assertExternalKeyNoOutputDependencies(t, child)
		}
	} else if v.IsArray() {
		for _, child := range v.AsArray().All {
			assertExternalKeyNoOutputDependencies(t, child)
		}
	}
}

func TestExternalKeyOmittedNameIsNotAdopted(t *testing.T) {
	server, api := providertest.New(t)
	externalKeyServerBehavior(api, "")
	input := providertest.Props(awsKey("arn:aws:kms:us-east-1:111122223333:key/k1")).Delete("displayName")
	created, err := server.Create(p.CreateRequest{Urn: externalKeyURN, Properties: input})
	require.NoError(t, err)
	require.Equal(t, "Generated name", created.Properties.Get("displayName").AsString())
	read, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: created.ID, Inputs: input, Properties: created.Properties})
	require.NoError(t, err)
	require.True(t, read.Inputs.Get("displayName").IsNull())
	diff, err := server.Diff(p.DiffRequest{Urn: externalKeyURN, ID: created.ID, State: read.Properties, OldInputs: input, Inputs: input})
	require.NoError(t, err)
	require.False(t, diff.HasChanges)
	baseline := len(api.Requests())
	updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: created.ID, State: read.Properties, OldInputs: input, Inputs: input})
	require.NoError(t, err)
	require.Equal(t, "Generated name", updated.Properties.Get("displayName").AsString())
	require.Len(t, api.Requests(), baseline+2)
	require.Empty(t, api.Requests()[baseline].Body)
}

func TestExternalKeyRejectedUpdateReturnsOrdinaryError(t *testing.T) {
	for _, transition := range []string{"remove", "change"} {
		t.Run(transition, func(t *testing.T) {
			server, api := providertest.New(t)
			api.Put(keysPath+"/ekey_one", externalKeyObject("ekey_one"))
			prior, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: "ekey_one"})
			require.NoError(t, err)
			secret := property.New("Production CMEK").WithSecret(true).WithDependencies([]resource.URN{"urn:pulumi:test::test::test:secret:Name::old"})
			oldInputs := prior.Inputs.Set("displayName", secret)
			oldState := prior.Properties.Set("displayName", secret)
			desired := oldInputs.Delete("displayName")
			if transition == "change" {
				desired = desired.Set("displayName", property.New("unaccepted name").WithDependencies([]resource.URN{"urn:pulumi:test::test::test:public:Name::new"}))
			}
			api.Handle("POST "+keysPath+"/ekey_one", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				fakeapi.WriteError(w, 400, "synthetic update rejected")
			})
			baseline := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: prior.ID, State: oldState, OldInputs: oldInputs, Inputs: desired})
			require.ErrorContains(t, err, "synthetic update rejected")
			require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
			require.Nil(t, updated.PartialState)
			require.Zero(t, updated.Properties.Len(), "a rejected request must not return reconstructed state")
			want := any(nil)
			if transition == "change" {
				want = "unaccepted name"
			}
			require.Equal(t, fakeapi.Object{"display_name": want}, api.Requests()[baseline].Body)
			require.Len(t, api.Requests(), baseline+1, "failed Update performs neither readiness validation nor internal refresh")
			require.Equal(t, secret, oldState.Get("displayName"))
			require.Equal(t, secret, oldInputs.Get("displayName"))
		})
	}
}

func TestExternalKeyMalformedSuccessfulUpdateReturnsOrdinaryError(t *testing.T) {
	for _, fault := range []string{"missing", "invalid"} {
		t.Run(fault, func(t *testing.T) {
			server, api := providertest.New(t)
			api.Put(keysPath+"/ekey_one", externalKeyObject("ekey_one"))
			prior, err := server.Read(p.ReadRequest{Urn: externalKeyURN, ID: "ekey_one"})
			require.NoError(t, err)
			desiredName := property.New("Accepted new name").WithSecret(true).WithDependencies([]resource.URN{"urn:pulumi:test::test::test:secret:Name::new"})
			desired := prior.Inputs.Set("displayName", desiredName)
			api.Handle("POST "+keysPath+"/ekey_one", func(w http.ResponseWriter, _ *http.Request, _ fakeapi.Object) {
				obj := externalKeyObject("ekey_one")
				obj["display_name"] = "Accepted new name"
				delete(obj, "created_at")
				if fault == "missing" {
					delete(obj, "updated_at")
					delete(obj, "attachment")
				} else {
					obj["updated_at"] = false
					obj["attachment"] = map[string]any{"type": "invalid"}
				}
				fakeapi.WriteJSON(w, obj)
			})
			baseline := len(api.Requests())
			updated, err := server.Update(p.UpdateRequest{Urn: externalKeyURN, ID: prior.ID, State: prior.Properties, OldInputs: prior.Inputs, Inputs: desired})
			require.Error(t, err)
			require.NotErrorAs(t, err, &infer.ResourceInitFailedError{})
			require.Nil(t, updated.PartialState)
			require.Zero(t, updated.Properties.Len())
			require.Equal(t, fakeapi.Object{"display_name": "Accepted new name"}, api.Requests()[baseline].Body)
			require.Len(t, api.Requests(), baseline+1, "unusable response triggers neither validation nor internal refresh")
			require.Equal(t, desiredName, desired.Get("displayName"))
		})
	}
}
