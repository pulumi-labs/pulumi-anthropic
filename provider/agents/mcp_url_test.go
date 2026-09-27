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

package agents

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCanonicalMCPServerURL pins the normalization the live API applies to a vault
// credential's mcp_server_url, as observed against it on 2026-09-26.
func TestCanonicalMCPServerURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"https://api.example.com/mcp/":   "https://api.example.com/mcp",
		"https://api.example.com/a//":    "https://api.example.com/a",
		"https://API.Example.com/B":      "https://api.example.com/B",
		"https://api.example.com/":       "https://api.example.com/",
		"https://api.example.com":        "https://api.example.com/",
		"https://api.example.com:443/c":  "https://api.example.com/c",
		"https://api.example.com/d?x=1":  "https://api.example.com/d?x=1",
		"https://api.example.com/e/../f": "https://api.example.com/e/../f",
		"https://api.example.com:8443/g": "https://api.example.com:8443/g",
		"not a url":                      "not a url",
	} {
		assert.Equal(t, want, canonicalMCPServerURL(in), in)
	}
}
