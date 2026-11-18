//go:build acceptance

package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareServiceAccountWorkspace(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	setup := c.OAuthClient(t)
	workspace, err := setup.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: c.RunName() + "-workspace"})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), workspace.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), workspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedWorkspaceId", workspace.ID)
	for _, name := range []string{"firstAccountId", "secondAccountId"} {
		setup := c.OAuthClient(t)
		account, err := setup.Organization.ServiceAccounts.New(t.Context(), sdk.OrganizationServiceAccountNewParams{Name: strings.ToLower(c.RunName() + "-" + name)})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup := c.OAuthClient(t)
			_, err := cleanup.Organization.ServiceAccounts.Archive(context.Background(), account.ID)
			require.NoError(t, err)
			read := c.OAuthClient(t)
			observed, err := read.Organization.ServiceAccounts.Get(context.Background(), account.ID)
			require.NoError(t, err)
			require.False(t, observed.ArchivedAt.IsZero())
		})
		c.SetConfig(t, name, account.ID)
	}

	client := func() sdk.Client { return c.OAuthClient(t) }
	t.Cleanup(func() {
		s := client()
		id := c.Config("secondAccountId")
		_, err := s.Organization.Workspaces.ServiceAccounts.Get(context.Background(), id, sdk.OrganizationWorkspaceServiceAccountGetParams{WorkspaceID: c.Config("seedWorkspaceId")})
		var e *sdk.Error
		require.ErrorAs(t, err, &e)
		require.Equal(t, 404, e.StatusCode)
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		for _, state := range stack.Deployment.Resources {
			if state.Type != "anthropic:identity:ServiceAccountWorkspace" {
				continue
			}
			require.Empty(t, state.InitErrors)
			if state.External {
				require.Equal(t, c.Config("secondAccountId"), state.Outputs["serviceAccountId"])
				require.Equal(t, "workspace_user", state.Outputs["workspaceRole"])
			}
		}
		if stack.Outputs["grantId"] == c.Config("seedWorkspaceId")+"/"+c.Config("secondAccountId") {
			create, remove := false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:identity:ServiceAccountWorkspace" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("identity change must replace")
					case "create-replacement":
						create = true
					case "delete-replaced":
						remove = true
					}
				}
			}
			require.True(t, create)
			require.True(t, remove)
		}
		id := c.Config("firstAccountId")
		if stack.Outputs["grantId"] == c.Config("seedWorkspaceId")+"/"+c.Config("secondAccountId") {
			id = c.Config("secondAccountId")
		}
		s := client()
		r, err := s.Organization.Workspaces.ServiceAccounts.Get(context.Background(), id, sdk.OrganizationWorkspaceServiceAccountGetParams{WorkspaceID: c.Config("seedWorkspaceId")})
		require.NoError(t, err)
		require.Equal(t, c.Config("seedWorkspaceId")+"/"+r.ServiceAccountID, stack.Outputs["grantId"])
		require.Equal(t, string(r.WorkspaceRole), stack.Outputs["role"])
		require.False(t, r.Implicit)
	}
}
func TestLiveServiceAccountWorkspace(t *testing.T) {
	edit := func(source string) string {
		dir := t.TempDir()
		raw, err := os.ReadFile(source)
		require.NoError(t, err)
		raw = program(t, raw)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
		return dir
	}
	opts := liveOptions(t, "service-account-workspace-lifecycle", prepareServiceAccountWorkspace)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/service-account-workspace-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}, {Dir: edit("testdata/service-account-workspace-replacement/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
