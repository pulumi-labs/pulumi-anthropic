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

// createForTrial creates a resource for a trial, and registers its deletion.
//
// The deletion is verified, not assumed. rest.Forget returns no error while leaving the
// object behind, so a resource that declares it accumulates garbage no sweeper can find.
// agents:UserProfile declared Forget while the API does support DELETE, which left 29
// profiles in a workspace before anyone noticed.
func createForTrial(
	t *testing.T, s integration.Server, u resource.URN, inputs property.Map,
) (p.CreateResponse, error) {
	t.Helper()
	check, err := s.Check(p.CheckRequest{Urn: u, Inputs: inputs})
	if err != nil {
		return p.CreateResponse{}, err
	}
	if len(check.Failures) > 0 {
		return p.CreateResponse{}, fmt.Errorf("the provider rejected the inputs: %v", check.Failures)
	}
	resp, err := s.Create(p.CreateRequest{Urn: u, Properties: check.Inputs})
	if err != nil {
		return p.CreateResponse{}, err
	}
	require.NotEmpty(t, resp.ID, "create returned no ID")
	t.Cleanup(func() {
		if err := s.Delete(p.DeleteRequest{ID: resp.ID, Urn: u, Properties: resp.Properties}); err != nil {
			t.Errorf("LEAKED %s %s: delete failed: %v", u.Type(), resp.ID, err)
			return
		}
		// A read after the delete must report the object gone. An archived object
		// reads as gone too, so this covers rest.Archive as well as rest.Delete.
		got, err := s.Read(p.ReadRequest{ID: resp.ID, Urn: u})
		if err == nil && got.ID != "" {
			t.Errorf("LEAKED %s %s: delete reported success and the object is still there. "+
				"Check the resource's EndOfLife: rest.Forget abandons an object silently",
				u.Type(), resp.ID)
		}
	})
	return resp, nil
}

// update applies new inputs to a created resource. An error is a finding rather than a
// harness fault: the API refuses to clear some fields and says so, and the record needs
// to hold what it said.
func update(
	t *testing.T, s integration.Server, u resource.URN, created p.CreateResponse,
	oldInputs, newInputs property.Map,
) error {
	t.Helper()
	check, err := s.Check(p.CheckRequest{Urn: u, State: created.Properties, Inputs: newInputs})
	if err != nil {
		return err
	}
	if len(check.Failures) > 0 {
		return fmt.Errorf("the provider rejected the inputs: %v", check.Failures)
	}
	_, err = s.Update(p.UpdateRequest{
		ID: created.ID, Urn: u, State: created.Properties, Inputs: check.Inputs,
	})
	return err
}

// apiMessage reduces a provider error to the API's own message, so that the record reads
// as evidence rather than as a Go error chain.
func apiMessage(err error) string {
	s := err.Error()
	i := strings.Index(s, `"message":"`)
	if i < 0 {
		if len(s) > 200 {
			return s[:200]
		}
		return s
	}
	s = s[i+len(`"message":"`):]
	// The message is JSON, so it can hold an escaped quote: a field name arrives as
	// \"expires_at\". Stop at the first unescaped quote and unescape what is left.
	var out []rune
	runes := []rune(s)
	for k := 0; k < len(runes); k++ {
		if runes[k] == '\\' && k+1 < len(runes) {
			out = append(out, runes[k+1])
			k++
			continue
		}
		if runes[k] == '"' {
			break
		}
		out = append(out, runes[k])
	}
	return string(out)
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

// emptyAs reports how the API represents a cleared value that keeps its key, and "" when
// the value is not empty. The API is not consistent about this: it drops
// agents:Agent description, reports a cleared array as [], and reports a cleared
// agents:MemoryStore description as the empty string. Each form carries no
// configuration, so each counts as cleared, and the record states which one appeared.
func emptyAs(v property.Value) string {
	switch {
	case v.IsString() && v.AsString() == "":
		return "the empty string; the key survives and holds nothing"
	case v.IsArray() && v.AsArray().Len() == 0:
		return "an empty array; the key survives and holds nothing"
	case v.IsMap() && v.AsMap().Len() == 0:
		return "an empty map; the key survives and holds nothing"
	default:
		return ""
	}
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
	}, {
		field:  []string{"model", "inferenceGeo"},
		record: "model.inferenceGeo",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"model": baseModel(map[string]property.Value{
					"inferenceGeo": property.New("us"),
				}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		// rest.Project treats a slice differently from a struct: it takes the
		// server's slice whole unless the lengths match. These three array cases are
		// the only live cover that path has.
		// The provider's Check refuses an MCP server that no mcp_toolset tool
		// references, so the server and its tool arrive and depart together.
		field:  []string{"mcpServers"},
		record: "mcpServers",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"mcpServers": property.New([]property.Value{
					property.New(property.NewMap(map[string]property.Value{
						"name": property.New("docs"),
						"url":  property.New("https://mcp.example.com"),
					})),
				}),
				"tools": property.New([]property.Value{
					property.New(property.NewMap(map[string]property.Value{
						"type":          property.New("mcp_toolset"),
						"mcpServerName": property.New("docs"),
					})),
				}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		field:  []string{"tools"},
		record: "tools",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"tools": property.New([]property.Value{
					property.New(property.NewMap(map[string]property.Value{
						"type": property.New("agent_toolset_20260401"),
					})),
				}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		// An anthropic skill reference needs no skill of our own, which keeps this
		// case free of a second object. pdf is one of the four skills every
		// workspace holds; see guinsnotes/agent-surface-baseline.json.
		field:  []string{"skills"},
		record: "skills",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"skills": property.New([]property.Value{
					property.New(property.NewMap(map[string]property.Value{
						"type":    property.New("anthropic"),
						"skillId": property.New("pdf"),
					})),
				}),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}, {
		// A roster entry of type self needs no other agent, which keeps this case
		// free of a second object.
		field:  []string{"multiagent", "agents"},
		record: "multiagent",
		withField: func(n string) property.Map {
			return agentInputs(n, map[string]property.Value{
				"multiagent": property.New(property.NewMap(map[string]property.Value{
					"agents": property.New([]property.Value{
						property.New(property.NewMap(map[string]property.Value{
							"type": property.New("self"),
						})),
					}),
				})),
			})
		},
		bare: func(n string) property.Map { return agentInputs(n, nil) },
	}}

	runRemovals(t, agentToken, cases)
}

// runRemovals drives each case for one resource and records what it found. Every case
// creates its own object, so one failure does not hide the rest.
func runRemovals(t *testing.T, token string, cases []removalCase) {
	t.Helper()
	runRemovalsWith(t, newLive(t), token, cases)
}

// runRemovalsWith is runRemovals against an existing server, for a resource whose cases
// need a parent object created first.
func runRemovalsWith(t *testing.T, s integration.Server, token string, cases []removalCase) {
	t.Helper()
	u := urn(token)
	verdicts := map[string]verdict{}
	t.Cleanup(func() { writeVerdicts(t, token, verdicts) })

	for _, c := range cases {
		name := strings.Join(c.field, ".")
		t.Run(name, func(t *testing.T) {
			objName := trialName(strings.ReplaceAll(name, ".", "-"))
			withField := c.withField(objName)

			// A create the API refuses settles the question: the field is not on the
			// surface, whatever the schema says. Record what the API said.
			created, err := createForTrial(t, s, u, withField)
			if err != nil {
				verdicts[c.record] = verdict{Status: "unverifiable",
					Observed: apiMessage(err),
					Note: "The API refuses a create that sets this field, so a removal " +
						"cannot be observed. The schema offers a field the API lacks.",
					Issue: 21}
				t.Errorf("%s: the API refuses a create that sets it: %s", name, apiMessage(err))
				return
			}

			before := onServer(t, s, u, created.ID)
			set, ok := at(before, c.field...)
			if !ok {
				t.Fatalf("the server does not report %s after a create that set it, so this "+
					"case cannot distinguish a removal from a field the API never stored", name)
			}
			t.Logf("after create:  %s = %v", name, set)

			// An update the API refuses is also an answer: the field cannot be
			// cleared, and the API says so rather than ignoring the null.
			if err := update(t, s, u, created, withField, c.bare(objName)); err != nil {
				verdicts[c.record] = verdict{Status: "kept", Observed: apiMessage(err),
					Note: "The API refuses the null the provider sends, so removing this " +
						"field from a program fails the deployment rather than clearing it.",
					Issue: 11}
				t.Errorf("%s: the API refuses the removal: %s", name, apiMessage(err))
				return
			}

			after := onServer(t, s, u, created.ID)
			got, present := at(after, c.field...)
			t.Logf("after removal: %s present=%v value=%v", name, present, got)

			switch {
			case !present:
				verdicts[c.record] = verdict{Status: "cleared"}
			case emptyAs(got) != "":
				verdicts[c.record] = verdict{Status: "cleared", Observed: emptyAs(got)}
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
}

// inputs builds a property map from a required base and the field under test.
func inputs(base, extra map[string]property.Value) property.Map {
	m := map[string]property.Value{}
	for k, v := range base {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return property.NewMap(m)
}

// metadataCases returns the two cases every resource with a metadata map needs: the map
// itself, and one key inside it. A key removal travels as the resource's tombstone.
func metadataCases(base func(name string) map[string]property.Value) []removalCase {
	withMeta := func(n string) property.Map {
		return inputs(base(n), map[string]property.Value{
			"metadata": property.New(property.NewMap(map[string]property.Value{
				"team": property.New("trial"),
				"tier": property.New("1"),
			})),
		})
	}
	return []removalCase{{
		field:     []string{"metadata", "tier"},
		record:    "metadata",
		withField: withMeta,
		bare: func(n string) property.Map {
			return inputs(base(n), map[string]property.Value{
				"metadata": property.New(property.NewMap(map[string]property.Value{
					"team": property.New("trial"),
				})),
			})
		},
	}}
}

func TestVaultOptionalFieldsClear(t *testing.T) {
	base := func(n string) map[string]property.Value {
		return map[string]property.Value{"displayName": property.New(n)}
	}
	runRemovals(t, "anthropic:agents:Vault", metadataCases(base))
}

func TestUserProfileOptionalFieldsClear(t *testing.T) {
	base := func(n string) map[string]property.Value { return nil }
	str := func(field, value string) removalCase {
		return removalCase{
			field:  []string{field},
			record: field,
			withField: func(n string) property.Map {
				return inputs(base(n), map[string]property.Value{field: property.New(value)})
			},
			bare: func(n string) property.Map { return inputs(base(n), nil) },
		}
	}
	cases := append(metadataCases(base),
		str("name", "pulumi trial profile"),
		str("externalId", "trial-external-id"),
		str("externalUserOnboardedAt", "2026-01-01T00:00:00Z"),
		removalCase{
			field:     []string{"accessType"},
			record:    "accessType",
			defaulted: "application",
			withField: func(n string) property.Map {
				return inputs(base(n), map[string]property.Value{
					"accessType": property.New("passthrough"),
				})
			},
			bare: func(n string) property.Map { return inputs(base(n), nil) },
		})
	runRemovals(t, "anthropic:agents:UserProfile", cases)
}

// parentVault creates a vault for a credential to live in, and registers its deletion.
func parentVault(t *testing.T, s integration.Server) string {
	t.Helper()
	u := urn("anthropic:agents:Vault")
	created, err := createForTrial(t, s, u, property.NewMap(map[string]property.Value{
		"displayName": property.New(trialName("parent-vault")),
	}))
	require.NoError(t, err, "a credential needs a vault to live in")
	return created.ID
}

func TestVaultCredentialOptionalFieldsClear(t *testing.T) {
	s := newLive(t)
	vaultID := parentVault(t, s)

	// static_bearer is the cheapest credential type: it needs a token and nothing
	// else. The two auth.refresh fields belong to mcp_oauth, which needs an OAuth
	// endpoint, so they stay pending.
	base := func(n string) map[string]property.Value {
		return map[string]property.Value{
			"vaultId":     property.New(vaultID),
			"displayName": property.New(n),
			"auth": property.New(property.NewMap(map[string]property.Value{
				"type":         property.New("static_bearer"),
				"token":        property.New("trial-token-value").WithSecret(true),
				"mcpServerUrl": property.New("https://mcp.example.com/" + n),
			})),
		}
	}
	authWith := func(n string, extra map[string]property.Value) property.Map {
		auth := map[string]property.Value{
			"type":         property.New("static_bearer"),
			"token":        property.New("trial-token-value").WithSecret(true),
			"mcpServerUrl": property.New("https://mcp.example.com/" + n),
		}
		for k, v := range extra {
			auth[k] = v
		}
		return property.NewMap(map[string]property.Value{
			"vaultId":     property.New(vaultID),
			"displayName": property.New(n),
			"auth":        property.New(property.NewMap(auth)),
		})
	}

	cases := []removalCase{{
		field:  []string{"auth", "expiresAt"},
		record: "auth.expiresAt",
		withField: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"expiresAt": property.New("2030-01-01T00:00:00Z"),
			})
		},
		bare: func(n string) property.Map { return authWith(n, nil) },
	}, {
		field:  []string{"auth", "injectionLocation"},
		record: "auth.injectionLocation",
		withField: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"injectionLocation": property.New(property.NewMap(map[string]property.Value{
					"header": property.New(true),
					"body":   property.New(true),
				})),
			})
		},
		bare: func(n string) property.Map { return authWith(n, nil) },
	}, {
		field:  []string{"auth", "injectionLocation", "body"},
		record: "auth.injectionLocation.body",
		withField: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"injectionLocation": property.New(property.NewMap(map[string]property.Value{
					"header": property.New(true),
					"body":   property.New(true),
				})),
			})
		},
		bare: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"injectionLocation": property.New(property.NewMap(map[string]property.Value{
					"header": property.New(true),
				})),
			})
		},
	}, {
		field:  []string{"auth", "networking"},
		record: "auth.networking",
		withField: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"networking": property.New(property.NewMap(map[string]property.Value{
					"type":         property.New("limited"),
					"allowedHosts": property.New([]property.Value{property.New("api.example.com")}),
				})),
			})
		},
		bare: func(n string) property.Map { return authWith(n, nil) },
	}, {
		field:  []string{"auth", "networking", "allowedHosts"},
		record: "auth.networking.allowedHosts",
		withField: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"networking": property.New(property.NewMap(map[string]property.Value{
					"type":         property.New("limited"),
					"allowedHosts": property.New([]property.Value{property.New("api.example.com")}),
				})),
			})
		},
		bare: func(n string) property.Map {
			return authWith(n, map[string]property.Value{
				"networking": property.New(property.NewMap(map[string]property.Value{
					"type": property.New("unrestricted"),
				})),
			})
		},
	}, {
		field:  []string{"displayName"},
		record: "displayName",
		withField: func(n string) property.Map {
			return inputs(base(n), nil)
		},
		bare: func(n string) property.Map {
			m := base(n)
			delete(m, "displayName")
			return inputs(m, nil)
		},
	}}
	cases = append(cases, metadataCases(base)...)
	runRemovalsWith(t, s, "anthropic:agents:VaultCredential", cases)
}

func TestMemoryStoreOptionalFieldsClear(t *testing.T) {
	base := func(n string) map[string]property.Value {
		return map[string]property.Value{"name": property.New(n)}
	}
	cases := append(metadataCases(base), removalCase{
		field:  []string{"description"},
		record: "description",
		withField: func(n string) property.Map {
			return inputs(base(n), map[string]property.Value{
				"description": property.New("a trial memory store"),
			})
		},
		bare: func(n string) property.Map { return inputs(base(n), nil) },
	})
	runRemovals(t, "anthropic:agents:MemoryStore", cases)
}
