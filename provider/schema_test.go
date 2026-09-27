// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package provider

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite the golden schema")

// TestSchema pins the provider's public surface across every SDK language, so that any
// change to it is deliberate and visible in review. Run with -update to accept changes.
func TestSchema(t *testing.T) {
	t.Parallel()
	got := schema(t)

	const golden = "schema.json"
	if *update {
		require.NoError(t, os.WriteFile(golden, got, 0o600))
		return
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err, "reading the golden schema (run with -update to create it)")
	assert.JSONEq(t, string(want), string(got))
}

// schema returns the provider's schema, indented.
func schema(t *testing.T) []byte {
	t.Helper()
	prov, err := Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(t.Context(), Name, semver.MustParse("0.1.0"),
		integration.WithProvider(prov))
	require.NoError(t, err)
	resp, err := server.GetSchema(p.GetSchemaRequest{})
	require.NoError(t, err)

	var doc json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(resp.Schema), &doc))
	out, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	return append(out, '\n')
}
