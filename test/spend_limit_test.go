//go:build acceptance

package test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func spendLimitOptions(t *testing.T) integration.ProgramTestOptions {
	dir, prepare := "spend-limit-workspace-lifecycle", prepareSpendLimitWorkspace
	opts := liveOptions(t, dir, prepare)

	validate := opts.ExtraRuntimeValidation
	sawRemoval, sawReplacement := false, false
	t.Cleanup(func() {
		require.True(t, sawRemoval, "amount removal must be checkpointed")
		require.True(t, sawReplacement, "replacement must use the new physical ID")
	})
	opts.ExtraRuntimeValidation = func(t *testing.T, s integration.RuntimeValidationStackInfo) {
		validate(t, s)
		found := false
		for _, r := range s.Deployment.Resources {
			if r.Type != "anthropic:organization:SpendLimit" {
				continue
			}
			found = true
			require.NotEmpty(t, r.ID)
			require.Contains(t, []string{"monthly", "weekly"}, r.Outputs["period"])
			require.Equal(t, true, r.Outputs["isEnabled"])
			require.Empty(t, r.InitErrors)
			require.Equal(t, "USD", r.Outputs["currency"])
			if r.Outputs["period"] == "monthly" && r.Outputs["amount"] == nil {
				sawRemoval = true
			}
			if r.Outputs["period"] == "weekly" {
				sawReplacement = true
				require.Equal(t, "100", r.Outputs["amount"])
			}
			if r.External {
				// External get checkpoints intentionally have no managed inputs;
				// the Read result is exposed as observed outputs instead.
				require.Empty(t, r.Inputs)
				require.Equal(t, "100", r.Outputs["amount"])
				require.Equal(t, "weekly", r.Outputs["period"])
			}
		}
		require.True(t, found)
	}
	return opts
}
func spendLimitEdit(t *testing.T, path string, validate func(*testing.T, integration.RuntimeValidationStackInfo)) integration.EditDir {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = program(t, raw)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
	return integration.EditDir{Dir: dir, Additive: true, ExtraRuntimeValidation: validate}
}
func TestLiveSpendLimit(t *testing.T) {
	requireBlockedScenario(t, "requires an organization with Admin API spend-limit writes enabled")
	opts := spendLimitOptions(t)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{
		spendLimitEdit(t, "testdata/spend-limit-workspace-remove/Pulumi.yaml", opts.ExtraRuntimeValidation),
		spendLimitEdit(t, "testdata/spend-limit-workspace-replace/Pulumi.yaml", opts.ExtraRuntimeValidation),
	}
	integration.ProgramTest(t, &opts)
}

func prepareSpendLimitWorkspace(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	setup := c.AdminClient(t)
	workspace, err := setup.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: c.RunName() + "-workspace"})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.AdminClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), workspace.ID)
		require.NoError(t, err)
		read := c.AdminClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), workspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedWorkspaceId", workspace.ID)
	return observeSpendLimit(t, c, workspace.ID)
}

func observeSpendLimit(t *testing.T, c *ScenarioContext, workspaceID string) func(*testing.T, integration.RuntimeValidationStackInfo) {
	ids := map[string]string{}
	t.Cleanup(func() {
		client := c.AdminClient(t)
		for _, period := range []string{"monthly", "weekly"} {
			id := ids[period]
			if id == "" {
				continue
			}
			_, err := client.Beta.Organization.SpendLimits.Get(context.Background(), id)
			var apiError *sdk.Error
			require.ErrorAs(t, err, &apiError)
			require.Equal(t, 404, apiError.StatusCode)
		}
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		for _, state := range stack.Deployment.Resources {
			if state.Type != "anthropic:organization:SpendLimit" {
				continue
			}
			id := string(state.ID)
			client := c.AdminClient(t)
			observed, err := client.Beta.Organization.SpendLimits.Get(t.Context(), id)
			require.NoError(t, err)
			period := string(observed.Period)
			if old, ok := ids[period]; ok {
				require.Equal(t, old, id, "the same period retains its identity")
			} else {
				ids[period] = id
			}
			require.Equal(t, "workspace", observed.Scope.Type)
			require.Equal(t, workspaceID, observed.Scope.WorkspaceID)
			require.Equal(t, period, state.Outputs["period"])
			require.Equal(t, observed.Currency, state.Outputs["currency"])
			require.Equal(t, observed.IsEnabled, state.Outputs["isEnabled"])
			if observed.JSON.Amount.Raw() == "null" {
				require.Nil(t, state.Outputs["amount"])
			} else {
				require.Equal(t, observed.Amount, state.Outputs["amount"])
			}
		}
	}
}
