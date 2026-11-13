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

func prepareInvite(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	recipient := c.RunName() + "@example.invalid"
	c.SetConfig(t, "inviteEmail", recipient)
	client := func() sdk.Client { return c.AdminClient(t) }
	var firstID, lastID string
	t.Cleanup(func() {
		s := client()
		require.NotEmpty(t, firstID)
		ids := []string{firstID}
		if lastID != firstID {
			ids = append(ids, lastID)
		}
		for _, id := range ids {
			_, err := s.Organization.Invites.Get(context.Background(), id)
			var apiErr *sdk.Error
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, 404, apiErr.StatusCode)
		}
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id := stack.Outputs["inviteId"].(string)
		lastID = id
		if firstID == "" {
			firstID = id
		}
		if id != firstID {
			deleted, created := false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && !pre.Planning && pre.Metadata.Type == "anthropic:organization:Invite" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("immutable invitation must replace")
					case "delete-replaced":
						require.False(t, created)
						deleted = true
					case "create-replacement":
						require.True(t, deleted)
						created = true
					}
				}
			}
			require.True(t, deleted)
			require.True(t, created)
		}
		s := client()
		r, err := s.Organization.Invites.Get(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, "pending", string(r.Status))
		require.Equal(t, recipient, r.Email)
	}
}

func TestLiveInvite(t *testing.T) {
	requireBlockedScenario(t, "requires a disposable recipient accepted by the invitation service")
	edit := func(source string) string {
		dir := t.TempDir()
		raw, err := os.ReadFile(source)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), program(t, raw), 0600))
		return dir
	}
	opts := liveOptions(t, "invite-lifecycle", prepareInvite)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/invite-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
