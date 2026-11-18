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

package config

import (
	"fmt"
	"sync"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// CheckConfig and DiffConfig decode proposed values on local receivers. They
// must not mutate the receiver that owns the provider's configured SDK clients.
func TestConfigurationChecksDoNotMutateConfiguredReceiver(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_ADMIN_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		t.Setenv(key, "")
	}
	client := &sdk.Client{}
	configured := &Config{
		APIKey: "configured-synthetic-key", BaseURL: "https://configured.invalid/",
		WorkspaceID: "configured-workspace", clients: map[Plane]*sdk.Client{Workspace: client},
	}
	provider := PreserveConfigMetadata(infer.Wrap(p.Provider{}, infer.Options{Config: infer.Config(configured)}))
	server, err := integration.NewServer(t.Context(), "anthropic", semver.MustParse("0.1.0"), integration.WithProvider(provider))
	require.NoError(t, err)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			baseURL := fmt.Sprintf("https://proposed-%d.invalid/", i)
			inputs := property.NewMap(map[string]property.Value{"baseUrl": property.New(baseURL)})
			checked, err := server.CheckConfig(p.CheckRequest{Inputs: inputs})
			if err != nil {
				errors <- err
				return
			}
			if len(checked.Failures) != 0 || checked.Inputs.Get("baseUrl").AsString() != baseURL {
				errors <- fmt.Errorf("CheckConfig response belongs to another invocation")
				return
			}
			_, err = server.DiffConfig(p.DiffRequest{
				State:  property.NewMap(map[string]property.Value{"baseUrl": property.New("https://old.invalid/")}),
				Inputs: checked.Inputs,
			})
			if err != nil {
				errors <- err
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, "configured-synthetic-key", configured.APIKey)
	require.Equal(t, "https://configured.invalid/", configured.BaseURL)
	require.Equal(t, "configured-workspace", configured.WorkspaceID)
	require.Nil(t, configured.checkInputs)
	got, err := configured.Client(Workspace)
	require.NoError(t, err)
	require.Same(t, client, got)
}
