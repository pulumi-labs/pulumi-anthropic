//go:build acceptance

package test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

var deploymentModelID = flag.String("model-id", "", "optional model for Deployment prerequisites, checked before allocation")

func prepareDeployment(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	modelID := *deploymentModelID
	if modelID == "" {
		modelID = "claude-opus-5-5"
	}
	c.SetConfig(t, "modelId", modelID)
	setup := c.WorkspaceClient(t)
	_, err := setup.Models.Get(t.Context(), modelID, sdk.ModelGetParams{})
	require.NoError(t, err, "configured model must exist before allocating prerequisites")
	agentSDK := c.WorkspaceClient(t)
	agent, err := agentSDK.Beta.Agents.New(t.Context(), sdk.BetaAgentNewParams{Name: c.RunName() + "-agent", Model: sdk.BetaManagedAgentsModelConfigParams{ID: sdk.BetaManagedAgentsModel(modelID)}, System: sdk.String("prerequisite initial version")})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.WorkspaceClient(t)
		_, err := cleanup.Beta.Agents.Archive(context.Background(), agent.ID, sdk.BetaAgentArchiveParams{})
		require.NoError(t, err)
		read := c.WorkspaceClient(t)
		observed, err := read.Beta.Agents.Get(context.Background(), agent.ID, sdk.BetaAgentGetParams{})
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedAgentId", agent.ID)
	c.SetConfig(t, "seedVersionInitial", strconv.FormatInt(agent.Version, 10))
	update := c.WorkspaceClient(t)
	editedAgent, err := update.Beta.Agents.Update(t.Context(), agent.ID, sdk.BetaAgentUpdateParams{Model: sdk.BetaManagedAgentsModelConfigParams{ID: sdk.BetaManagedAgentsModel(modelID)}, System: sdk.String("prerequisite edited version")})
	require.NoError(t, err)
	require.Greater(t, editedAgent.Version, agent.Version)
	c.SetConfig(t, "seedVersionEdited", strconv.FormatInt(editedAgent.Version, 10))
	envSDK := c.WorkspaceClient(t)
	selfHosted := sdk.NewBetaSelfHostedConfigParams()
	environment, err := envSDK.Beta.Environments.New(t.Context(), sdk.BetaEnvironmentNewParams{Name: c.RunName() + "-environment", Config: sdk.BetaEnvironmentNewParamsConfigUnion{OfSelfHosted: &selfHosted}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.WorkspaceClient(t)
		_, err := cleanup.Beta.Environments.Delete(context.Background(), environment.ID, sdk.BetaEnvironmentDeleteParams{})
		require.NoError(t, err)
		read := c.WorkspaceClient(t)
		_, err = read.Beta.Environments.Get(context.Background(), environment.ID, sdk.BetaEnvironmentGetParams{})
		var apiError *sdk.Error
		require.ErrorAs(t, err, &apiError)
		require.Equal(t, 404, apiError.StatusCode)
	})
	c.SetConfig(t, "seedEnvironmentId", environment.ID)
	storeSDK := c.WorkspaceClient(t)
	store, err := storeSDK.Beta.MemoryStores.New(t.Context(), sdk.BetaMemoryStoreNewParams{Name: c.RunName() + "-memory"})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.WorkspaceClient(t)
		_, err := cleanup.Beta.MemoryStores.Delete(context.Background(), store.ID, sdk.BetaMemoryStoreDeleteParams{})
		require.NoError(t, err)
		_, err = cleanup.Beta.MemoryStores.Get(context.Background(), store.ID, sdk.BetaMemoryStoreGetParams{})
		var apiError *sdk.Error
		require.ErrorAs(t, err, &apiError)
		require.Equal(t, 404, apiError.StatusCode)
	})
	c.SetConfig(t, "seedMemoryStoreId", store.ID)

	client := func() sdk.Client { return c.WorkspaceClient(t) }
	t.Cleanup(func() {
		id, err := c.ResourceID("deployment")
		if err != nil {
			return
		}
		s := client()
		r, err := s.Beta.Deployments.Get(context.Background(), id, sdk.BetaDeploymentGetParams{})
		require.NoError(t, err)
		require.Equal(t, id, r.ID)
		require.False(t, r.ArchivedAt.IsZero(), "archive is retained history, not hard deletion")
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, err := c.ResourceID("deployment")
		require.NoError(t, err)
		s := client()
		observed, err := s.Beta.Deployments.Get(context.Background(), id, sdk.BetaDeploymentGetParams{})
		require.NoError(t, err)
		edited := observations == 1
		observations++
		require.Equal(t, id, observed.ID)
		require.Equal(t, c.RunName(), observed.Name)
		require.Equal(t, c.Config("seedEnvironmentId"), observed.EnvironmentID)
		require.Equal(t, "active", string(observed.Status))
		require.Equal(t, c.Config("seedAgentId"), observed.Agent.ID)
		require.True(t, observed.ArchivedAt.IsZero())
		if edited {
			require.EqualValues(t, editedAgent.Version, observed.Agent.Version)
			require.Empty(t, observed.Description)
			require.Empty(t, observed.Resources)
			require.Equal(t, map[string]string{"owner": "test"}, observed.Metadata)
			require.False(t, observed.JSON.Budget.Valid())
		} else {
			require.EqualValues(t, agent.Version, observed.Agent.Version)
			require.Equal(t, "clear me", observed.Description)
			require.Len(t, observed.Resources, 1)
			require.Equal(t, "memory_store", observed.Resources[0].Type)
			require.Equal(t, store.ID, observed.Resources[0].MemoryStoreID)
			require.Equal(t, "2500", observed.Budget.MaxListCost.Amount)
		}
		require.Equal(t, id, stack.Outputs["deploymentId"])
		external := false
		for _, r := range stack.Deployment.Resources {
			if r.Type != "anthropic:agents:Deployment" {
				continue
			}
			require.Empty(t, r.InitErrors)
			require.NotContains(t, r.Inputs, "paused")
			require.NotContains(t, r.Outputs, "paused")
			require.Equal(t, "active", r.Outputs["status"])
			if r.External {
				external = true
				require.Equal(t, id, string(r.ID))
			}
		}
		if edited {
			require.True(t, external, "ID-only get must checkpoint the observed Deployment")
		}
	}
}
func TestLiveDeployment(t *testing.T) {
	opts := liveOptions(t, "deployment-lifecycle", prepareDeployment)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := deploymentEdit(t, "testdata/deployment-edit/Pulumi.yaml")
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
func deploymentEdit(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = program(t, raw)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
	return dir
}
