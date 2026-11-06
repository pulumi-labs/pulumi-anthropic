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
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/providertest"
)

const externalKeyToken = "anthropic:security:ExternalKey"

var externalKeyURN = resource.NewURN("test", "test", "", externalKeyToken, "key")

const keysPath = "/v1/organizations/external_keys"

// externalKeyServerBehavior mimics the API's defaults and answers validation with the
// given outcome.
func externalKeyServerBehavior(api *fakeapi.Server, validationError string) {
	api.SetHooks("external_keys", fakeapi.Hooks{OnCreate: func(obj fakeapi.Object) {
		obj["attachment"] = map[string]any{"type": "unattached"}
		obj["geo"] = "us"
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
				assert.Equal(t, "sk-ant-admin-test", req.Header.Get("X-Api-Key"))
				assert.Regexp(t, `^`+keysPath+`/external_key_\d+/validate$`, api.Requests()[len(api.Requests())-1].Path,
					"a new key is validated")
				assert.Equal(t, "unattached", output.Get("attachment").AsString())
				_, hasGeo := output.GetOk("geo")
				assert.False(t, hasGeo, "the server's default geo does not leak into the inputs")
			},
		},
		Updates: []integration.Operation{{
			Inputs: providertest.Props(renamed),
			Hook: func(_, _ property.Map) {
				reqs := api.Requests()
				assert.Equal(t, fakeapi.Object{"display_name": "Primary CMEK"}, reqs[len(reqs)-1].Body)
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
