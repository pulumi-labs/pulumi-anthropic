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

func prepareAgent(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() sdk.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, err := c.ResourceID("agent")
		require.NoError(t, err)
		s := client()
		r, err := s.Beta.Agents.Get(context.Background(), id, sdk.BetaAgentGetParams{})
		require.NoError(t, err)
		require.Equal(t, id, r.ID)
		require.False(t, r.ArchivedAt.IsZero(), "archive retains an independently readable historical object, not a fabricated hard-delete")
	})
	observations := 0
	var previousVersion int64
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, err := c.ResourceID("agent")
		require.NoError(t, err)
		s := client()
		r, err := s.Beta.Agents.Get(context.Background(), id, sdk.BetaAgentGetParams{})
		require.NoError(t, err)
		require.Equal(t, id, r.ID)
		require.Equal(t, c.RunName(), r.Name)
		clearing := observations > 0
		if clearing {
			require.Empty(t, r.System)
			require.Empty(t, r.Description)
			require.Equal(t, map[string]string{"owner": "test"}, r.Metadata)
			require.Greater(t, r.Version, previousVersion, "mutable edit must create a newer version")
			// Independent SDK observations assert stored configuration only. No
			// session, advisor call, workflow run or delegation is executed.
			require.Equal(t, "multiagent_20261001", r.Multiagent.Type)
			require.Equal(t, "disabled", r.Multiagent.Advisor.Type)
			require.Empty(t, r.Multiagent.Advisor.Model)
			require.Equal(t, "enabled", r.Multiagent.Subagents.Type)
			require.Equal(t, "enabled", r.Multiagent.Subagents.InlineAgents.Type)
			require.Empty(t, r.Multiagent.Subagents.PredefinedAgents)
			require.Equal(t, "disabled", r.Multiagent.Workflows.Type)
			require.Empty(t, r.Multiagent.Workflows.PredefinedAgents)
		} else {
			require.Equal(t, "synthetic prompt", r.System)
			require.Equal(t, "synthetic description", r.Description)
			require.Equal(t, map[string]string{"owner": "test", "remove": "old"}, r.Metadata)
			require.GreaterOrEqual(t, r.Version, int64(1))
			require.Equal(t, "null", r.JSON.Multiagent.Raw(), "initial allocation stays bare")
		}
		require.Equal(t, "claude-opus-5-5", r.Model.ID)
		require.Equal(t, id, stack.Outputs["agentId"])
		require.Equal(t, r.Name, stack.Outputs["agentName"])
		require.EqualValues(t, r.Version, stack.Outputs["agentVersion"])
		for _, state := range stack.Deployment.Resources {
			if state.Type == "anthropic:agents:Agent" {
				require.Empty(t, state.InitErrors)
				require.EqualValues(t, r.Version, state.Outputs["version"])
			}
		}
		previousVersion = r.Version
		observations++
	}
}
func TestLiveAgent(t *testing.T) {
	opts := liveOptions(t, "agent-lifecycle", prepareAgent)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, err := os.ReadFile("testdata/agent-edit/Pulumi.yaml")
	require.NoError(t, err)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
