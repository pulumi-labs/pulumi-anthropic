//go:build acceptance

package test

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"github.com/stretchr/testify/require"
)

// YAML syntax checks do not validate Pulumi project configuration declarations.
// Check base programs and additive edits without credentials, plugins or API calls.
func TestPulumiProjectsLoad(t *testing.T) {
	projects := 0
	for _, root := range []string{".", "../examples"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				switch entry.Name() {
				case "node_modules", ".venv", "venv", "bin", ".git", "__pycache__":
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Name() != "Pulumi.yaml" {
				return nil
			}
			projects++
			t.Run(path, func(t *testing.T) {
				_, err := workspace.LoadProject(path)
				require.NoError(t, err)
			})
			return nil
		})
		require.NoError(t, err)
	}
	require.Positive(t, projects, "no Pulumi projects found")
}
