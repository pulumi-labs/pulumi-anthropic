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

func assertNoProfileAttestations(t *testing.T, outputs map[string]any) {
	t.Helper()
	for _, key := range []string{"policy_attestations", "policyAttestations"} {
		_, present := outputs[key]
		require.False(t, present, "response-only SDK extra must not become a Pulumi output")
	}
}
func prepareUserProfile(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, e := c.ResourceID("profile")
		require.NoError(t, e)
		sdk := client()
		_, e = sdk.Beta.UserProfiles.Get(context.Background(), id, anthropic.BetaUserProfileGetParams{})
		var ae *anthropic.Error
		require.True(t, errors.As(e, &ae))
		require.Equal(t, 404, ae.StatusCode)
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, e := c.ResourceID("profile")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.UserProfiles.Get(context.Background(), id, anthropic.BetaUserProfileGetParams{})
		require.NoError(t, e)
		expected := map[string]string{"owner": "test", "remove": "old"}
		if observations == 1 {
			delete(expected, "remove")
			require.Equal(t, r.Name, stack.Outputs["observedName"])
		}
		require.True(t, maps.Equal(expected, r.Metadata), "independent SDK observation must prove intended metadata deletion/retention")
		require.Equal(t, c.RunName(), r.Name)
		require.Equal(t, "application", string(r.AccessType))
		require.Equal(t, "synthetic-reference", r.ExternalID)
		require.Equal(t, id, stack.Outputs["profileId"])
		require.Equal(t, r.Name, stack.Outputs["name"])
		require.Equal(t, r.CreatedAt.Format(time.RFC3339Nano), stack.Outputs["createdAt"])
		external := false
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:UserProfile" {
				assertNoProfileAttestations(t, s.Outputs)
				require.Empty(t, s.InitErrors)
				require.Equal(t, id, string(s.ID))
				require.Equal(t, r.Name, s.Outputs["name"])
				if s.External {
					external = true
				}
			}
		}
		if observations == 1 {
			require.True(t, external, "YAML ID-only get must checkpoint the external resource")
		}
		observations++
	}
}
func TestLiveUserProfile(t *testing.T) {
	edit := func(path string) string {
		raw, e := os.ReadFile(path)
		require.NoError(t, e)
		raw = program(t, raw)
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "Pulumi.yaml"), raw, 0600))
		return dir
	}
	opts := liveOptions(t, "user-profile-lifecycle", prepareUserProfile)
	opts.RequireEmptyPreviewAfterRefresh = true
	opts.EditDirs = []integration.EditDir{{Dir: edit("testdata/user-profile-edit/Pulumi.yaml"), Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	t.Run("replacement", liveUserProfileReplacement)
}

func liveUserProfileReplacement(t *testing.T) {

	opts := liveOptions(t, "user-profile-replacement", prepareUserProfileReplacement)
	opts.SkipRefresh = true
	raw, e := os.ReadFile("testdata/user-profile-replacement-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	edit := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
func prepareUserProfileReplacement(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	client := func() anthropic.Client { return c.WorkspaceClient(t) }
	t.Cleanup(func() {
		for index := range 2 {
			id, e := c.ResourceID("profile", index)
			require.NoError(t, e)
			sdk := client()
			_, e = sdk.Beta.UserProfiles.Get(context.Background(), id, anthropic.BetaUserProfileGetParams{})
			var ae *anthropic.Error
			require.True(t, errors.As(e, &ae))
			require.Equal(t, 404, ae.StatusCode)
		}
	})
	observations := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		binding := "first"
		if observations == 1 {
			binding = "second"
		}
		id, e := c.ResourceID("profile")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Beta.UserProfiles.Get(context.Background(), id, anthropic.BetaUserProfileGetParams{})
		require.NoError(t, e)
		require.Equal(t, c.RunName()+"-"+binding, r.Name)
		require.Equal(t, id, stack.Outputs["profileId"])
		found := 0
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:agents:UserProfile" {
				assertNoProfileAttestations(t, s.Outputs)
				found++
				require.False(t, s.Delete)
				require.Empty(t, s.InitErrors)
				require.Equal(t, id, string(s.ID))
				if observations == 1 {
					_, present := s.Inputs["externalUserOnboardedAt"]
					require.False(t, present)
					_, present = s.Outputs["externalUserOnboardedAt"]
					require.False(t, present)
				}
			}
		}
		require.Equal(t, 1, found)
		if observations == 1 {
			first, e := c.ResourceID("profile", 0)
			require.NoError(t, e)
			require.NotEqual(t, first, id)
			require.True(t, r.ExternalUserOnboardedAt.IsZero())
			create, del := false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && pre.Metadata.Type == "anthropic:agents:UserProfile" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("onboarding removal must not call Update")
					case "create-replacement":
						create = true
					case "delete-replaced":
						del = true
					}
				}
			}
			require.True(t, create, "engine must allocate a replacement")
			require.True(t, del, "engine must delete the old attribution identity")
		} else {
			require.False(t, r.ExternalUserOnboardedAt.IsZero())
		}
		observations++
	}
}
