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

func prepareFederationRuleWorkspace(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	publicKeys := issuerPublicKeys(t, c)

	setuphomeWorkspace := c.OAuthClient(t)
	homeWorkspace, err := setuphomeWorkspace.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: strings.ToLower(c.RunName() + "-homeWorkspace")})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), homeWorkspace.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), homeWorkspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "homeWorkspaceId", homeWorkspace.ID)
	c.SetConfig(t, "homeWorkspaceName", homeWorkspace.Name)

	setupfirstWorkspace := c.OAuthClient(t)
	firstWorkspace, err := setupfirstWorkspace.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: strings.ToLower(c.RunName() + "-firstWorkspace")})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), firstWorkspace.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), firstWorkspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "firstWorkspaceId", firstWorkspace.ID)
	c.SetConfig(t, "firstWorkspaceName", firstWorkspace.Name)

	setupsecondWorkspace := c.OAuthClient(t)
	secondWorkspace, err := setupsecondWorkspace.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: strings.ToLower(c.RunName() + "-secondWorkspace")})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), secondWorkspace.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), secondWorkspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "secondWorkspaceId", secondWorkspace.ID)
	c.SetConfig(t, "secondWorkspaceName", secondWorkspace.Name)

	setupAccount := c.OAuthClient(t)
	account, err := setupAccount.Organization.ServiceAccounts.New(t.Context(), sdk.OrganizationServiceAccountNewParams{Name: c.RunName() + "-account"})
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
	c.SetConfig(t, "seedAccountId", account.ID)
	for _, id := range map[string]string{"homeWorkspace": homeWorkspace.ID, "firstWorkspace": firstWorkspace.ID, "secondWorkspace": secondWorkspace.ID} {
		grant := c.OAuthClient(t)
		_, err := grant.Organization.Workspaces.ServiceAccounts.Add(t.Context(), id, sdk.OrganizationWorkspaceServiceAccountAddParams{ServiceAccountID: account.ID, WorkspaceRole: sdk.NoBillingWorkspaceRoleWorkspaceDeveloper})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup := c.OAuthClient(t)
			_, err := cleanup.Organization.Workspaces.ServiceAccounts.Remove(context.Background(), account.ID, sdk.OrganizationWorkspaceServiceAccountRemoveParams{WorkspaceID: id})
			require.NoError(t, err)
		})
	}

	setupseedIssuer := c.OAuthClient(t)
	seedIssuer, err := setupseedIssuer.Organization.Federation.Issuers.New(t.Context(), sdk.OrganizationFederationIssuerNewParams{Name: strings.ToLower(c.RunName() + "-seedIssuer"), IssuerURL: "https://" + c.RunName() + "-issuer.invalid", JWKS: sdk.OrganizationFederationIssuerNewParamsJWKSUnion{OfInline: &sdk.JWKSInlineParam{Keys: publicKeys}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Federation.Issuers.Archive(context.Background(), seedIssuer.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Federation.Issuers.Get(context.Background(), seedIssuer.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedIssuerId", seedIssuer.ID)

	setupRule := c.OAuthClient(t)
	rule, err := setupRule.Organization.Federation.Rules.New(t.Context(), sdk.OrganizationFederationRuleNewParams{Name: c.RunName() + "-rule", IssuerID: seedIssuer.ID, WorkspaceID: sdk.String(homeWorkspace.ID), OAuthScope: "workspace:inference", Target: sdk.ServiceAccountTargetParam{ServiceAccountID: account.ID}, Match: sdk.FederationRuleMatchParam{SubjectPrefix: sdk.String(c.RunName())}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Federation.Rules.Archive(context.Background(), rule.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Federation.Rules.Get(context.Background(), rule.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedRuleId", rule.ID)

	client := func() sdk.Client { return c.OAuthClient(t) }
	t.Cleanup(func() {
		s := client()
		page, err := s.Organization.Federation.Rules.Workspaces.List(context.Background(), c.Config("seedRuleId"), sdk.OrganizationFederationRuleWorkspaceListParams{})
		require.NoError(t, err)
		require.Len(t, page.Data, 1)
		require.Equal(t, homeWorkspace.ID, page.Data[0].WorkspaceID)
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		for _, state := range stack.Deployment.Resources {
			if state.Type != "anthropic:identity:FederationRuleWorkspace" {
				continue
			}
			require.Empty(t, state.InitErrors)
			if state.External {
				// External get checkpoints omit inputs; direct Read/import recovery is
				// asserted by the fast provider-protocol regression.
				require.Equal(t, c.Config("seedRuleId"), state.Outputs["federationRuleId"])
				require.Equal(t, c.Config("secondWorkspaceId"), state.Outputs["workspaceId"])
				require.Equal(t, c.Config("secondWorkspaceName"), state.Outputs["workspaceName"])
			}
		}
		if stack.Outputs["bindingId"] == c.Config("seedRuleId")+"/"+c.Config("secondWorkspaceId") {
			create, remove := false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:identity:FederationRuleWorkspace" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("workspace identity change must replace, not update")
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
		s := client()
		page, err := s.Organization.Federation.Rules.Workspaces.List(context.Background(), c.Config("seedRuleId"), sdk.OrganizationFederationRuleWorkspaceListParams{})
		require.NoError(t, err)
		require.Len(t, page.Data, 2)
		observed := map[string]bool{}
		for _, entry := range page.Data {
			observed[entry.WorkspaceID] = true
		}
		require.True(t, observed[homeWorkspace.ID])
		current := strings.TrimPrefix(stack.Outputs["bindingId"].(string), rule.ID+"/")
		require.True(t, observed[current])
		require.Contains(t, []string{firstWorkspace.ID, secondWorkspace.ID}, current)
	}
}
func TestLiveFederationRuleWorkspace(t *testing.T) {
	edit := func(source string) string {
		dir := t.TempDir()
		raw, err := os.ReadFile(source)
		require.NoError(t, err)
		raw = program(t, raw)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
		return dir
	}
	opts := liveOptions(t, "federation-rule-workspace-lifecycle", prepareFederationRuleWorkspace)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/federation-rule-workspace-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
