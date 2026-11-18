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

func complianceSettingsOptions(t *testing.T) integration.ProgramTestOptions {
	return liveOptions(t, "compliance-settings-lifecycle", prepareComplianceSettings)
}

func prepareComplianceSettings(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	setup := c.AdminClient(t)
	original, err := setup.Organization.ComplianceSettings.Get(t.Context())
	require.NoError(t, err)
	require.Contains(t, []string{"enabled", "disabled"}, original.State.Type)
	t.Cleanup(func() {
		state := sdk.ComplianceSettingsStateParamUnion{}
		if original.State.Type == "enabled" {
			v := sdk.NewComplianceSettingsStateEnabledParam()
			state.OfEnabled = &v
		} else {
			v := sdk.NewComplianceSettingsStateDisabledParam()
			state.OfDisabled = &v
		}
		restore := c.AdminClient(t)
		_, err := restore.Organization.ComplianceSettings.Update(context.Background(), sdk.OrganizationComplianceSettingUpdateParams{State: state})
		require.NoError(t, err)
		read := c.AdminClient(t)
		observed, err := read.Organization.ComplianceSettings.Get(context.Background())
		require.NoError(t, err)
		require.Equal(t, original.State.Type, observed.State.Type)
	})

	observe := func(t *testing.T, want bool) {
		s := c.AdminClient(t)
		v, err := s.Organization.ComplianceSettings.Get(context.Background())
		require.NoError(t, err)
		expected := "disabled"
		if want {
			expected = "enabled"
		}
		require.Equal(t, expected, v.State.Type)
	}
	last := true
	t.Cleanup(func() { observe(t, last) }) // Forget must leave the singleton unchanged.
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		for _, r := range stack.Deployment.Resources {
			if r.Type == "anthropic:organization:ComplianceSettings" {
				require.Equal(t, "compliance-settings", string(r.ID))
				require.Empty(t, r.InitErrors)
				require.IsType(t, false, r.Outputs["enabled"])
				if r.External {
					require.Equal(t, false, r.Outputs["enabled"])
				}
			}
		}
		last = stack.Outputs["enabled"].(bool)
		observe(t, last)
	}
}
func complianceSettingsEdit(t *testing.T, path string, validate func(*testing.T, integration.RuntimeValidationStackInfo)) integration.EditDir {
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	raw = program(t, raw)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
	return integration.EditDir{Dir: dir, Additive: true, ExtraRuntimeValidation: validate}
}
func TestLiveComplianceSettings(t *testing.T) {
	requireBlockedScenario(t, "requires an organization with directly writable compliance settings and restorable original values")
	opts := complianceSettingsOptions(t)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{complianceSettingsEdit(t, "testdata/compliance-settings-edit/Pulumi.yaml", opts.ExtraRuntimeValidation)}
	integration.ProgramTest(t, &opts)
}
