//go:build acceptance

package test

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/stretchr/testify/require"
)

func prepareFederationRule(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	publicKeys := issuerPublicKeys(t, c)

	setupseedWorkspace := c.OAuthClient(t)
	seedWorkspace, err := setupseedWorkspace.Organization.Workspaces.New(t.Context(), sdk.OrganizationWorkspaceNewParams{Name: strings.ToLower(c.RunName() + "-seedWorkspace")})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Workspaces.Archive(context.Background(), seedWorkspace.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Workspaces.Get(context.Background(), seedWorkspace.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "seedWorkspaceId", seedWorkspace.ID)
	c.SetConfig(t, "seedWorkspaceName", seedWorkspace.Name)

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
	for _, id := range map[string]string{"seedWorkspace": seedWorkspace.ID} {
		grant := c.OAuthClient(t)
		_, err := grant.Organization.Workspaces.ServiceAccounts.Add(t.Context(), id, sdk.OrganizationWorkspaceServiceAccountAddParams{ServiceAccountID: account.ID, WorkspaceRole: sdk.NoBillingWorkspaceRoleWorkspaceDeveloper})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanup := c.OAuthClient(t)
			_, err := cleanup.Organization.Workspaces.ServiceAccounts.Remove(context.Background(), account.ID, sdk.OrganizationWorkspaceServiceAccountRemoveParams{WorkspaceID: id})
			require.NoError(t, err)
		})
	}

	setupfirstIssuer := c.OAuthClient(t)
	firstIssuer, err := setupfirstIssuer.Organization.Federation.Issuers.New(t.Context(), sdk.OrganizationFederationIssuerNewParams{Name: strings.ToLower(c.RunName() + "-firstIssuer"), IssuerURL: "https://" + c.RunName() + "-first-issuer.invalid", JWKS: sdk.OrganizationFederationIssuerNewParamsJWKSUnion{OfInline: &sdk.JWKSInlineParam{Keys: publicKeys}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Federation.Issuers.Archive(context.Background(), firstIssuer.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Federation.Issuers.Get(context.Background(), firstIssuer.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "firstIssuerId", firstIssuer.ID)

	setupsecondIssuer := c.OAuthClient(t)
	secondIssuer, err := setupsecondIssuer.Organization.Federation.Issuers.New(t.Context(), sdk.OrganizationFederationIssuerNewParams{Name: strings.ToLower(c.RunName() + "-secondIssuer"), IssuerURL: "https://" + c.RunName() + "-second-issuer.invalid", JWKS: sdk.OrganizationFederationIssuerNewParamsJWKSUnion{OfInline: &sdk.JWKSInlineParam{Keys: publicKeys}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanup := c.OAuthClient(t)
		_, err := cleanup.Organization.Federation.Issuers.Archive(context.Background(), secondIssuer.ID)
		require.NoError(t, err)
		read := c.OAuthClient(t)
		observed, err := read.Organization.Federation.Issuers.Get(context.Background(), secondIssuer.ID)
		require.NoError(t, err)
		require.False(t, observed.ArchivedAt.IsZero())
	})
	c.SetConfig(t, "secondIssuerId", secondIssuer.ID)

	client := func() sdk.Client { return c.OAuthClient(t) }
	t.Cleanup(func() {
		s := client()
		for index := range 2 {
			id, err := c.ResourceID("rule", index)
			require.NoError(t, err)
			r, err := s.Organization.Federation.Rules.Get(context.Background(), id)
			require.NoError(t, err)
			require.False(t, r.ArchivedAt.IsZero())
		}
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, ok := stack.Outputs["ruleId"].(string)
		require.True(t, ok)
		s := client()
		r, err := s.Organization.Federation.Rules.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, r.Name, stack.Outputs["name"])
		require.Equal(t, r.IssuerID, stack.Outputs["issuerId"])
		require.True(t, r.ArchivedAt.IsZero())
		if !r.AppliesToAllWorkspaces {
			require.Equal(t, int64(3600), r.TokenLifetimeSeconds)
			require.Equal(t, map[string]string{"owner": "synthetic"}, r.Match.Claims)
			require.Empty(t, r.Match.Audience)
			require.Empty(t, r.Match.Condition)
			require.Equal(t, []string{c.Config("seedWorkspaceId")}, r.WorkspaceIDs)
		}
	}
}
func TestLiveFederationRule(t *testing.T) {
	opts := liveOptions(t, "federation-rule-lifecycle", prepareFederationRule)
	opts.RequireEmptyPreviewAfterRefresh = true
	replacement := issuerEdit(t, "testdata/federation-rule-replacement/Pulumi.yaml", func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		opts.ExtraRuntimeValidation(t, stack)
		newID := stack.Outputs["ruleId"].(string)
		require.NotEmpty(t, newID)
		archived, created := false, false
		for _, e := range stack.Events {
			if pre := e.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:identity:FederationRule" {
				switch string(pre.Metadata.Op) {
				case "delete-replaced":
					require.NotNil(t, pre.Metadata.Old)
					require.NotEqual(t, pre.Metadata.Old.ID, newID)
					archived = true
				case "create-replacement":
					require.True(t, archived, "same-name issuer replacement archives first")
					created = true
				}
			}
		}
		require.True(t, archived)
		require.True(t, created)
	})
	opts.EditDirs = []integration.EditDir{issuerEdit(t, "testdata/federation-rule-edit/Pulumi.yaml", opts.ExtraRuntimeValidation), replacement}
	integration.ProgramTest(t, &opts)
}
