// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package skills

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCanonicalPathArchiveFromEngine covers a FileArchive as the Pulumi engine delivers it
// over gRPC: with an empty asset map alongside its path. The path must win; reading the
// empty assets instead uploads an empty skill and drops the path from state.
func TestCanonicalPathArchiveFromEngine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o600))
	fromEngine := &resource.Archive{Sig: resource.ArchiveSig, Path: dir, Assets: map[string]any{}}

	c, err := canonical(fromEngine)
	require.NoError(t, err)
	assert.Equal(t, dir, c.Path)
	assert.Nil(t, c.Assets)

	contents, err := c.Open()
	require.NoError(t, err)
	defer contents.Close()
	name, _, err := contents.Next()
	require.NoError(t, err)
	assert.Equal(t, "SKILL.md", name)
}
