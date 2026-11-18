//go:build acceptance

package test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareRbacGroup(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	client := func() sdk.Client { return c.AdminClient(t) }
	t.Cleanup(func() {
		id, err := c.ResourceID("group")
		require.NoError(t, err)
		s := client()
		_, err = s.Organization.RBACGroups.Get(context.Background(), id)
		var e *sdk.Error
		require.True(t, errors.As(err, &e))
		require.Equal(t, 404, e.StatusCode)
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, err := c.ResourceID("group")
		require.NoError(t, err)
		s := client()
		r, err := s.Organization.RBACGroups.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, id, stack.Outputs["groupId"])
		require.Equal(t, r.Name, stack.Outputs["name"])
		for _, state := range stack.Deployment.Resources {
			if state.Type == "anthropic:identity:RbacGroup" {
				require.Empty(t, state.InitErrors)
				require.Equal(t, r.Name, state.Outputs["name"])
				require.Equal(t, []any{}, state.Outputs["roleIds"])
				if state.External {
					// ReadStep.Apply checkpoints program-supplied inputs, not Read's
					// returned Inputs (unlike ImportStep). ID-only provider Read input
					// recovery is enforced by TestRbacGroupImportAndFullUpdate.
					require.Empty(t, state.Inputs, "external get has no program-supplied inputs")
					require.Equal(t, r.Name, state.Outputs["name"], "ID-only get exposes the observed name")
				}
			}
		}
	}
}
func TestLiveRbacGroup(t *testing.T) {
	requireBlockedScenario(t, "requires verified Enterprise RBAC API access")
	opts := liveOptions(t, "rbac-group-lifecycle", prepareRbacGroup)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, err := os.ReadFile("testdata/rbac-group-edit/Pulumi.yaml")
	require.NoError(t, err)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
