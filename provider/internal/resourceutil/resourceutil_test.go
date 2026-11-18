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

package resourceutil_test

import (
	"testing"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	"github.com/stretchr/testify/require"
)

func TestSplitID(t *testing.T) {
	for _, tt := range []struct {
		id, first, second string
	}{
		{"parent/child", "parent", "child"},
		{"a%2Fb/c", "a%2Fb", "c"},
		{"日本語/child", "日本語", "child"},
		{" / ", " ", " "},
		{"a/..", "a", ".."},
	} {
		first, second, ok := resourceutil.SplitID(tt.id)
		require.True(t, ok, tt.id)
		require.Equal(t, tt.first, first)
		require.Equal(t, tt.second, second)
	}
	for _, id := range []string{"", "a", "/", "/a", "a/", "a/b/c", "a//b", "a/b/", "//"} {
		first, second, ok := resourceutil.SplitID(id)
		require.False(t, ok, id)
		require.Empty(t, first)
		require.Empty(t, second)
	}
}
