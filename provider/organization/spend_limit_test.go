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

	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/assert"

	"github.com/pulumi/pulumi-anthropic/provider/internal/fakeapi"
	"github.com/pulumi/pulumi-anthropic/provider/internal/providertest"
)

const spendLimitsPath = "/v1/organizations/spend_limits"

// spendLimitServerBehavior mimics the upsert: a limit is keyed by its scope and period,
// and setting it again overwrites it in place. The period defaults to monthly.
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
		api.Put(spendLimitsPath+"/"+ids[key], obj)
		fakeapi.WriteJSON(w, obj)
	})
}

func spendLimit(amount any, period string) map[string]any {
	limit := map[string]any{"scope": map[string]any{"type": "workspace", "workspaceId": "wrkspc_1"}}
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
					"scope":  map[string]any{"type": "workspace", "workspace_id": "wrkspc_1"},
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
