//go:build acceptance

package test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi/pulumi/pkg/v3/testing/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/require"
)

func prepareSkill(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {
	client := func() anthropic.Client { return c.WorkspaceClient(t) }

	t.Cleanup(func() {
		id, e := c.ResourceID("skill")
		if e != nil {
			return
		} // No committed allocation identity; never guess cleanup IDs.
		sdk := client()
		_, e = sdk.Skills.Get(context.Background(), id, anthropic.SkillGetParams{})
		var apiErr *anthropic.Error
		require.True(t, errors.As(e, &apiErr), "independent delete observation must establish absence")
		require.Equal(t, 404, apiErr.StatusCode)
	})
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		id, e := c.ResourceID("skill")
		require.NoError(t, e)
		sdk := client()
		observed, e := sdk.Skills.Get(context.Background(), id, anthropic.SkillGetParams{})
		require.NoError(t, e)
		require.Equal(t, id, observed.ID)
		require.Equal(t, id, stack.Outputs["skillId"])
		require.Equal(t, observed.LatestVersionID, stack.Outputs["version"])
		version, e := sdk.Skills.Versions.Get(context.Background(), observed.LatestVersionID, anthropic.SkillVersionGetParams{SkillID: id})
		require.NoError(t, e)
		require.Equal(t, id, version.SkillID)
		require.Equal(t, "pulumi-test-skill", version.Name)
		// Metadata establishes a version identity, not recoverability of uploaded bytes.
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:skills:Skill" {
				require.Empty(t, s.InitErrors)
				if s.External {
					require.Nil(t, s.Inputs["source"], "ID-only get cannot recover local archive bytes")
					require.Equal(t, version.Name, s.Outputs["name"])
				} else {
					require.NotNil(t, s.Inputs["source"])
				}
			}
		}
	}
}

func TestLiveSkill(t *testing.T) {
	opts := liveOptions(t, "skill-lifecycle", prepareSkill)
	opts.RequireEmptyPreviewAfterRefresh = true
	edit := t.TempDir()
	raw, e := os.ReadFile("testdata/skill-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(edit, "source"), 0700))
	md, e := os.ReadFile("testdata/skill-edit/source/SKILL.md")
	require.NoError(t, e)
	require.NoError(t, os.WriteFile(filepath.Join(edit, "source", "SKILL.md"), md, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
	liveSkillReplacement(t)
}

// Exercise unknown archive inputs and immutable-name replacement.
func liveSkillReplacement(t *testing.T) {

	opts := liveOptions(t, "skill-replacement", prepareSkillReplacement)
	opts.SkipRefresh = true
	opts.SkipEmptyPreviewUpdate = true
	raw, e := os.ReadFile("testdata/skill-replacement-edit/Pulumi.yaml")
	require.NoError(t, e)
	raw = program(t, raw)
	edit := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(edit, "Pulumi.yaml"), raw, 0600))
	opts.EditDirs = []integration.EditDir{{Dir: edit, Additive: true, ExtraRuntimeValidation: opts.ExtraRuntimeValidation}}
	integration.ProgramTest(t, &opts)
}
func prepareSkillReplacement(t *testing.T, c *ScenarioContext) func(*testing.T, integration.RuntimeValidationStackInfo) {

	client := func() anthropic.Client { return c.WorkspaceClient(t) }
	t.Cleanup(func() {
		ids := append(c.ResourceIDs("seed"), c.ResourceIDs("skill")...)
		require.Len(t, ids, 3)
		for _, id := range ids {
			sdk := client()
			_, e := sdk.Skills.Get(context.Background(), id, anthropic.SkillGetParams{})
			var apiErr *anthropic.Error
			require.True(t, errors.As(e, &apiErr))
			require.Equal(t, 404, apiErr.StatusCode)
		}
	})
	count := 0
	return func(t *testing.T, stack integration.RuntimeValidationStackInfo) {
		expectedName := "pulumi-test-skill"
		if count == 1 {
			expectedName = "pulumi-test-renamed-skill"
		}
		id, e := c.ResourceID("skill")
		require.NoError(t, e)
		sdk := client()
		r, e := sdk.Skills.Get(context.Background(), id, anthropic.SkillGetParams{})
		require.NoError(t, e)
		v, e := sdk.Skills.Versions.Get(context.Background(), r.LatestVersionID, anthropic.SkillVersionGetParams{SkillID: id})
		require.NoError(t, e)
		require.Equal(t, id, v.SkillID)
		require.Equal(t, expectedName, v.Name)
		require.Equal(t, id, stack.Outputs["skillId"])
		require.Equal(t, v.ID, skillUnsecret(stack.Outputs["version"]))
		var seedURN resource.URN
		for _, s := range stack.Deployment.Resources {
			if s.Type == "anthropic:skills:Skill" && s.URN.Name() == "seed" {
				seedURN = s.URN
			}
		}
		require.NotEmpty(t, seedURN)
		for _, s := range stack.Deployment.Resources {
			if s.Type != "anthropic:skills:Skill" || s.URN.Name() != "skill" {
				continue
			}
			require.Equal(t, id, string(s.ID))
			require.False(t, s.Delete)
			require.Empty(t, s.InitErrors)
			require.Equal(t, expectedName, skillUnsecret(s.Outputs["name"]))
			for _, bag := range []map[string]any{s.Inputs, s.Outputs} {
				source, ok := bag["source"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, resource.SecretSig, source[resource.SigKey], "archive source must remain runtime secret")
			}
			if count == 0 {
				require.Contains(t, s.PropertyDependencies["source"], seedURN, "source dependency must survive the unknown-archive preview")
			}
		}
		if count == 1 {
			first, e := c.ResourceID("skill", 0)
			require.NoError(t, e)
			require.NotEqual(t, first, id)
			created, deleted := false, false
			for _, event := range stack.Events {
				if pre := event.ResourcePreEvent; pre != nil && pre.Metadata.Type == "anthropic:skills:Skill" && resource.URN(pre.Metadata.URN).Name() == "skill" {
					switch string(pre.Metadata.Op) {
					case "update":
						t.Fatal("immutable name/label edit must replace, not upload a version")
					case "create-replacement":
						created = true
					case "delete-replaced":
						deleted = true
					}
				}
			}
			require.True(t, created)
			require.True(t, deleted)
		}
		count++
	}
}
func skillUnsecret(value any) any {
	if m, ok := value.(map[string]any); ok && m[resource.SigKey] == resource.SecretSig {
		return m["value"]
	}
	return value
}
