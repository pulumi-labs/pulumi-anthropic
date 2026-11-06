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

//go:build livetrial

// Package livetrial answers questions about the live Anthropic API that the fake
// cannot answer, and that a refresh cannot answer either.
//
// A refresh reports no drift for a field the program no longer declares, because
// rest.Project keeps a declared field unset rather than take the server's value. That
// is deliberate: it stops server defaults from reading as drift. The cost is that a
// value the API failed to clear is invisible to a refresh.
//
// These trials read the server through an import instead. An import has no prior state,
// so rest.Refresh adopts the server's values wholesale, which is the only view that
// shows a value the program stopped declaring.
//
// Every trial creates real objects in the workspace the key belongs to, and deletes
// them again. Run with: make trial
package livetrial

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blang/semver"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/integration"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/common/tokens"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/pulumi-labs/pulumi-anthropic/provider"
)

// namePrefix marks every object a trial creates, so that a leftover can be told from
// an object that was already in the workspace. guinsnotes/agent-surface-baseline.json
// records what was there before.
const namePrefix = "pulumi-trial"

// trialName returns a name unique to this run.
func trialName(kind string) string {
	return fmt.Sprintf("%s-%s-%d", namePrefix, kind, time.Now().UnixNano())
}

// newLive builds a provider against the real API. It omits baseUrl, which the fake-backed
// harness in internal/providertest sets, so requests go to api.anthropic.com.
//
// The server takes context.Background() rather than t.Context(). A cleanup function runs
// after the test's context is cancelled, so a server built on t.Context() cannot delete
// what the test created, and every object leaks.
func newLive(t *testing.T) integration.Server {
	t.Helper()
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Fatal("ANTHROPIC_API_KEY is not set, so this trial cannot reach the Anthropic API")
	}
	prov, err := provider.Provider()
	require.NoError(t, err)
	server, err := integration.NewServer(context.Background(), provider.Name, semver.MustParse("0.1.0"),
		integration.WithProvider(prov))
	require.NoError(t, err)
	require.NoError(t, server.Configure(p.ConfigureRequest{Args: property.NewMap(
		map[string]property.Value{"apiKey": property.New(key).WithSecret(true)},
	)}))
	return server
}

func urn(token string) resource.URN {
	return resource.NewURN("trial", "trial", "", tokens.Type(token), "trial")
}

// create checks and creates a resource, and registers its deletion.
func create(t *testing.T, s integration.Server, u resource.URN, inputs property.Map) p.CreateResponse {
	t.Helper()
	check, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	resp, err := s.Create(p.CreateRequest{Urn: u, Properties: check.Inputs})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ID, "create returned no ID")
	t.Cleanup(func() {
		if err := s.Delete(p.DeleteRequest{ID: resp.ID, Urn: u, Properties: resp.Properties}); err != nil {
			t.Errorf("LEAKED %s %s: delete failed: %v", u.Type(), resp.ID, err)
		}
	})
	return resp
}

// update applies new inputs to a created resource.
func update(
	t *testing.T, s integration.Server, u resource.URN, created p.CreateResponse,
	oldInputs, newInputs property.Map,
) p.UpdateResponse {
	t.Helper()
	check, err := s.Check(p.CheckRequest{Urn: u, State: created.Properties, Inputs: newInputs})
	require.NoError(t, err)
	require.Empty(t, check.Failures)
	resp, err := s.Update(p.UpdateRequest{
		ID: created.ID, Urn: u, State: created.Properties, Inputs: check.Inputs,
	})
	require.NoError(t, err)
	return resp
}

// onServer reads an object as an import, which adopts the server's values wholesale.
// This is the only read that shows a field the program has stopped declaring.
func onServer(t *testing.T, s integration.Server, u resource.URN, id string) property.Map {
	t.Helper()
	resp, err := s.Read(p.ReadRequest{ID: id, Urn: u})
	require.NoError(t, err)
	require.NotEmpty(t, resp.ID, "import read returned no ID, so the object is gone")
	return resp.Properties
}

// at walks a dotted path through a property map, reporting whether it is present.
func at(m property.Map, path ...string) (property.Value, bool) {
	cur := property.New(m)
	for _, key := range path {
		if !cur.IsMap() {
			return property.Value{}, false
		}
		v, ok := cur.AsMap().GetOk(key)
		if !ok {
			return property.Value{}, false
		}
		cur = v
	}
	return cur, true
}

// baseModel is the smallest valid model for an agent.
func baseModel(extra map[string]property.Value) property.Value {
	m := map[string]property.Value{"id": property.New("claude-opus-5-5")}
	for k, v := range extra {
		m[k] = v
	}
	return property.New(property.NewMap(m))
}

const agentToken = "anthropic:agents:Agent"

// recordPath is provider/livebehavior.json, relative to this package.
const recordPath = "../../livebehavior.json"

// verdict is one entry in the record. It mirrors behaviorRecord in
// provider/behavior_test.go, which guards the file's completeness.
type verdict struct {
	Status   string `json:"status"`
	Observed string `json:"observed,omitempty"`
	Note     string `json:"note,omitempty"`
	Issue    int    `json:"issue,omitempty"`
}

// writeVerdicts merges this run's findings into provider/livebehavior.json. It replaces
// only the fields the run covered, so a partial run does not erase an earlier one. The
// diff is the reviewable artifact.
func writeVerdicts(t *testing.T, token string, found map[string]verdict) {
	t.Helper()
	if len(found) == 0 {
		return
	}
	raw, err := os.ReadFile(recordPath)
	require.NoError(t, err, "the record is missing; the guard in provider/behavior_test.go needs it")

	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	removal, ok := doc["removal"].(map[string]any)
	require.True(t, ok, "the record has no removal section")
	fields, ok := removal[token].(map[string]any)
	require.True(t, ok, "the record has no entry for %s", token)

	for path, v := range found {
		fields[path] = v
	}
	doc["recorded"] = time.Now().UTC().Format("2006-01-02")

	out, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(recordPath, append(out, '\n'), 0o644))
	t.Logf("recorded %d verdict(s) for %s in %s", len(found), token, recordPath)
}

// show renders a property value for the record, so that an entry reads "low" rather
// than the struct's default formatting.
func show(v property.Value) string {
	switch {
	case v.IsString():
		return v.AsString()
	case v.IsBool():
		return fmt.Sprint(v.AsBool())
	case v.IsNumber():
		return fmt.Sprint(v.AsNumber())
	default:
		return fmt.Sprint(v)
	}
}

// removalCase declares a field to set, then remove, then look for on the server.
type removalCase struct {
	// field names the case, and is the dotted path to look up afterwards.
	field []string
	// record is the key this case writes in provider/livebehavior.json. It is the
	// schema's path, which can differ from the wire path that field walks.
	record string
	// set is the declared value that should reach the server.
	set property.Value
	// extra adds the field to the inputs. The path within the inputs can differ from
	// the path on the wire, so each case states both.
	withField func(name string) property.Map
	// bare omits the field.
	bare func(name string) property.Map
	// defaulted is the value the API chooses for an object that never declared the
	// field, where one is known. A removal that leaves this value is correct; a
	// removal that leaves the declared value is not.
	defaulted string
}

func agentInputs(name string, extra map[string]property.Value) property.Map {
	m := map[string]property.Value{"name": property.New(name)}
	for k, v := range extra {
		m[k] = v
	}
	if _, ok := m["model"]; !ok {
		m["model"] = baseModel(nil)
	}
	return property.NewMap(m)
}

// TestAgentOptionalFieldsClear asks, for each optional field of an agent, whether the
// API forgets the field once the program stops declaring it.
//
// The provider sends an explicit null for a removed key at any depth, which
// rest.UpdateBody is documented to do "so that removal works whether the API merges or
// replaces it". These cases check that claim against the API.
func TestAgentOptionalFieldsClear(t *testing.T) {
	cases := []removalCase{{
		field:     []string{"model", "effort"},
		record:    "model.effort",
		defaulted: "medium",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"model": baseModel(map[string]property.Value{"effort": property.New("low")}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		field:     []string{"model", "speed"},
		record:    "model.speed",
		defaulted: "standard",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"model": baseModel(map[string]property.Value{"speed": property.New("fast")}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		field:  []string{"description"},
		record: "description",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"description": property.New("a trial agent"),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		field:  []string{"system"},
		record: "system",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"system": property.New("You answer briefly."),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		// metadata is a map the API merges key by key, so agent.go lists it in
		// Tombstones with nil as the value that removes a key. This case checks
		// that the tombstone works.
		field:  []string{"metadata", "tier"},
		record: "metadata",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"metadata": property.New(property.NewMap(map[string]property.Value{
					"team": property.New("trial"),
					"tier": property.New("1"),
				})),
			})
		},
		bare: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"metadata": property.New(property.NewMap(map[string]property.Value{
					"team": property.New("trial"),
				})),
			})
		},
	}}

	s := newLive(t)
	u := urn(agentToken)
	verdicts := map[string]verdict{}

	for _, c := range cases {
		name := strings.Join(c.field, ".")
		t.Run(name, func(t *testing.T) {
			objName := trialName(strings.ReplaceAll(name, ".", "-"))
			withField := c.withField(objName)
			created := create(t, s, u, withField)

			before := onServer(t, s, u, created.ID)
			set, ok := at(before, c.field...)
			if !ok {
				t.Fatalf("the server does not report %s after a create that set it, so this "+
					"case cannot distinguish a removal from a field the API never stored", name)
			}
			t.Logf("after create:  %s = %v", name, set)

			bare := c.bare(objName)
			update(t, s, u, created, withField, bare)

			after := onServer(t, s, u, created.ID)
			got, present := at(after, c.field...)
			t.Logf("after removal: %s present=%v value=%v", name, present, got)

			switch {
			case !present:
				verdicts[c.record] = verdict{Status: "cleared"}
			case c.defaulted != "" && fmt.Sprint(got) == fmt.Sprint(property.New(c.defaulted)):
				verdicts[c.record] = verdict{Status: "cleared",
					Observed: c.defaulted + ", the server default"}
			case fmt.Sprint(got) == fmt.Sprint(set):
				verdicts[c.record] = verdict{Status: "kept", Observed: show(got),
					Note: "The provider sends this field as null and the API keeps the old " +
						"value, so a program cannot un-set it. Neither the update nor a later " +
						"refresh reports the stale value.", Issue: 11}
				t.Errorf("%s still holds the declared value %v after removal, so the API "+
					"ignored the null the provider sent", name, got)
			default:
				verdicts[c.record] = verdict{Status: "kept", Observed: show(got),
					Note: "The API left an unexpected value after the removal.", Issue: 11}
				t.Errorf("%s holds %v after removal, which is neither absent, the default, "+
					"nor the declared value", name, got)
			}
		})
	}

	t.Cleanup(func() { writeVerdicts(t, agentToken, verdicts) })
}
