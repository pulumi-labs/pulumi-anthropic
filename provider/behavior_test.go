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

package provider

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This guard keeps provider/livebehavior.json honest. That file records what the live
// Anthropic API does when a program stops declaring an optional field, which no unit
// test can discover: the fake answers whatever its hooks declare, and a refresh never
// compares a field the program dropped.
//
// The guard calls nothing. It derives the set of fields that need a verdict from the
// schema, and fails when a field has neither a verdict nor an acknowledged gap. A new
// resource therefore arrives with its fields listed as pending rather than silently
// uncovered.
//
// provider/internal/livetrial produces the verdicts. Run it with `make trial`.

// behaviorRecord is provider/livebehavior.json.
type behaviorRecord struct {
	Recorded string `json:"recorded"`
	API      string `json:"api"`
	// Removal maps a resource token to its fields, and each field to a verdict.
	Removal map[string]map[string]struct {
		// Status is cleared, kept, pending, or unverifiable.
		Status   string `json:"status"`
		Observed string `json:"observed"`
		Note     string `json:"note"`
		Issue    int    `json:"issue"`
	} `json:"removal"`
}

// needsVerdict walks a resource's optional input fields, nested ones included, and
// returns the paths whose removal behavior the API alone can settle.
//
// A secret is exempt: the API never returns one, so a removal cannot be observed. A
// replaceOnChanges field is exempt: removing it replaces the resource, so there is no
// clear to verify.
func needsVerdict(doc schemaDoc, props map[string]schemaProperty, required map[string]bool,
	prefix string, seen map[string]bool,
) []string {
	var paths []string
	for name, prop := range props {
		path := prefix + name
		if prop.Secret || prop.ReplaceOnChanges {
			continue
		}
		if obj, ok := typeOf(doc, prop); ok {
			if len(obj.Enum) > 0 {
				if prefix != "" || !required[name] {
					paths = append(paths, path)
				}
				continue
			}
			key := strings.TrimPrefix(prop.Ref, "#/types/")
			if seen[key] {
				continue
			}
			seen[key] = true
			req := map[string]bool{}
			for _, r := range obj.Required {
				req[r] = true
			}
			paths = append(paths, needsVerdict(doc, obj.Properties, req, path+".", seen)...)
			delete(seen, key)
			continue
		}
		if prefix != "" || !required[name] {
			paths = append(paths, path)
		}
	}
	return paths
}

// TestEveryOptionalFieldHasARemovalVerdict requires each optional field to carry a
// recorded verdict, or an acknowledged gap, in provider/livebehavior.json. A field that
// appears in neither is a field nobody has thought about.
func TestEveryOptionalFieldHasARemovalVerdict(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)

	raw, err := os.ReadFile("livebehavior.json")
	require.NoError(t, err, "provider/livebehavior.json is missing; regenerate it")
	var record behaviorRecord
	require.NoError(t, json.Unmarshal(raw, &record))

	var missing []string
	for token, res := range doc.Resources {
		required := map[string]bool{}
		for _, r := range res.RequiredInputs {
			required[r] = true
		}
		for _, path := range needsVerdict(doc, res.InputProperties, required, "", map[string]bool{}) {
			entry, ok := record.Removal[token][path]
			if !ok {
				missing = append(missing, token+" "+path)
				continue
			}
			assert.Contains(t, []string{"cleared", "kept", "pending", "unverifiable"},
				entry.Status, "%s %s has an unknown status", token, path)
			if entry.Status == "kept" {
				assert.NotEmpty(t, entry.Note,
					"%s %s is recorded as kept, so it needs a note saying what a user sees",
					token, path)
			}
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "these optional fields have no entry in provider/livebehavior.json. "+
		"Add each one as pending, or record a verdict with `make trial`:\n  %s",
		strings.Join(missing, "\n  "))
}

// TestRemovalVerdictsDescribeRealFields requires every entry to name a field that still
// exists, so that the record does not accumulate verdicts for fields the API dropped.
func TestRemovalVerdictsDescribeRealFields(t *testing.T) {
	t.Parallel()
	doc := loadSchema(t)

	raw, err := os.ReadFile("livebehavior.json")
	require.NoError(t, err)
	var record behaviorRecord
	require.NoError(t, json.Unmarshal(raw, &record))

	var stale []string
	for token, fields := range record.Removal {
		res, ok := doc.Resources[token]
		if !ok {
			stale = append(stale, token+" (the resource is gone)")
			continue
		}
		required := map[string]bool{}
		for _, r := range res.RequiredInputs {
			required[r] = true
		}
		live := map[string]bool{}
		for _, p := range needsVerdict(doc, res.InputProperties, required, "", map[string]bool{}) {
			live[p] = true
		}
		for path := range fields {
			if !live[path] {
				stale = append(stale, token+" "+path)
			}
		}
	}
	sort.Strings(stale)
	assert.Empty(t, stale, "provider/livebehavior.json holds entries for fields that are no "+
		"longer optional inputs, so they need removing:\n  %s", strings.Join(stale, "\n  "))
}
