//go:build acceptance

package test

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareMemoryStore(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, e := c.ResourceID("memory")
		require.NoError(t, e)
		sdk := client()
		_, e = sdk.Beta.MemoryStores.Get(context.Background(), id, anthropic.BetaMemoryStoreGetParams{})
		var apiError *anthropic.Error
		require.True(t, errors.As(e, &apiError), "independent delete readback must report SDK absence")
		require.Equal(t, 404, apiError.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, e := c.ResourceID("memory")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.MemoryStores.Get(context.Background(), id, anthropic.BetaMemoryStoreGetParams{})
		require.NoError(t, e)
		// Observation phases come from scenario execution, never from returned
		// provider fields.
		clearing := observations == 1
		require.NoError(t, validateMemoryStoreEffect(r, c.RunName(), clearing))
		observations++
		require.Equal(t, id, r.ID)
		require.Equal(t, r.ID, stack.Outputs["storeId"])
		require.Equal(t, r.Name, stack.Outputs["storeName"])
		require.Equal(t, r.CreatedAt.Format(time.RFC3339Nano), stack.Outputs["createdAt"])
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:MemoryStore" {
				require.Empty(t, s.InitErrors)
				if r.Description != "" {
					require.Equal(t, r.Description, s.Outputs["description"])
				} else {
					require.True(t, s.Outputs["description"] == nil || s.Outputs["description"] == "")
				}
				expected := map[string]any{}
				for k, v := range r.Metadata {
					expected[k] = v
				}
				require.Equal(t, expected, s.Outputs["metadata"])
			}
		}
	}
}
func TestLiveMemoryStore(t *testing.T) {
	opts := liveOptions(t, "memory-store-lifecycle", prepareMemoryStore)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/memory-store-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}

// These are scenario expectations from the YAML declaration/edit, not a
// projection of provider outputs or an invocation of the resource converter.
func validateMemoryStoreEffect(observed *anthropic.BetaManagedAgentsMemoryStore, runName string, clearing bool) error {
	if observed == nil || runName == "" || observed.Name != runName {
		return errors.New("independent memory store observation has the wrong scenario name")
	}
	description := "synthetic description"
	metadata := map[string]string{"owner": "test", "remove": "old"}
	if clearing {
		description = ""
		metadata = map[string]string{"owner": "test"}
	}
	if observed.Description != description || !maps.Equal(observed.Metadata, metadata) {
		return errors.New("independent memory store observation does not have the intended description and exact metadata")
	}
	return nil
}
