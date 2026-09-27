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

package rest

import (
	"encoding/json"
	"testing"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type level string

func (l level) MarshalJSON() ([]byte, error) { return MarshalTypeTag(string(l)) }

func (l *level) UnmarshalJSON(raw []byte) error {
	s, err := UnmarshalTypeTag(raw)
	*l = level(s)
	return err
}

type mcpServer struct {
	ServerURL string `pulumi:"serverUrl"`
}

func (mcpServer) ExtendWire(obj map[string]any) { obj["type"] = "url" }

type args struct {
	Name     string            `pulumi:"name"`
	Count    *int              `pulumi:"count,optional"`
	Enabled  *bool             `pulumi:"enabled,optional"`
	Level    *level            `pulumi:"level,optional"`
	Servers  []mcpServer       `pulumi:"mcpServers,optional"`
	Metadata map[string]string `pulumi:"metadata,optional"`
	Schema   map[string]any    `pulumi:"inputSchema,optional"`
	Secret   *string           `pulumi:"secretValue,optional" provider:"secret"`
	Local    *string           `pulumi:"local,optional"       wire:"-"`
}

type match struct {
	Audience      *string `pulumi:"audience,optional"`
	SubjectPrefix *string `pulumi:"subjectPrefix,optional"`
}

type rule struct {
	Match *match `pulumi:"match,optional"`
}

// TestUpdateBodyRemovesNestedFields covers a field removed from inside an object. The
// object is sent whole, with null for the removed key, so that the field is cleared
// whether the server replaces the object or merges it.
func TestUpdateBodyRemovesNestedFields(t *testing.T) {
	t.Parallel()
	old := rule{Match: &match{Audience: ptr("aud"), SubjectPrefix: ptr("repo:")}}
	new := rule{Match: &match{SubjectPrefix: ptr("repo:")}}
	body, err := UpdateBody(old, new, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"match": map[string]any{"subject_prefix": "repo:", "audience": nil}}, body)
}

type residency struct {
	Geo    *string `pulumi:"geo,optional"    provider:"replaceOnChanges"`
	Region *string `pulumi:"region,optional"`
}

type workspace struct {
	Residency *residency `pulumi:"residency,optional"`
}

// TestDiffReplacesWhenAddingAnObjectWithAnImmutableField covers an object that goes from
// unset to set: an immutable field inside it cannot be applied by an update.
func TestDiffReplacesWhenAddingAnObjectWithAnImmutableField(t *testing.T) {
	t.Parallel()
	assert.Equal(t, p.AddReplace,
		Diff(workspace{}, workspace{Residency: &residency{Geo: ptr("us")}}).DetailedDiff["residency"].Kind)
	assert.Equal(t, p.Add,
		Diff(workspace{}, workspace{Residency: &residency{Region: ptr("x")}}).DetailedDiff["residency"].Kind,
		"adding an object whose immutable fields are unset is an update")
}

type state struct {
	args
	CreatedAt string `pulumi:"createdAt"`
}

func (s *state) Inputs() *args { return &s.args }

func ptr[T any](v T) *T { return &v }

func TestCodecRoundTrip(t *testing.T) {
	t.Parallel()
	in := args{
		Name:     "a",
		Count:    ptr(3),
		Enabled:  ptr(false),
		Level:    ptr(level("high")),
		Servers:  []mcpServer{{ServerURL: "https://x"}},
		Metadata: map[string]string{"camelKey": "v"},
		Schema:   map[string]any{"type": "object", "properties": map[string]any{"fooBar": map[string]any{}}},
		Local:    ptr("not sent"),
	}
	tree, err := Encode(in)
	require.NoError(t, err)
	raw, err := json.Marshal(tree)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"name": "a",
		"count": 3,
		"enabled": false,
		"level": {"type": "high"},
		"mcp_servers": [{"type": "url", "server_url": "https://x"}],
		"metadata": {"camelKey": "v"},
		"input_schema": {"type": "object", "properties": {"fooBar": {}}}
	}`, string(raw), "field names are snake_cased, map keys are not, and unset optionals are omitted")

	var out args
	require.NoError(t, Decode(raw, &out))
	in.Local = nil
	assert.Equal(t, in, out)
}

func TestDecodeState(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"id": "x_1", "name": "server-name", "level": "low", "created_at": "t", "extra": 1}`)
	declared := args{Name: "declared"}
	out, id, err := DecodeState[args, state](raw, declared)
	require.NoError(t, err)
	assert.Equal(t, "x_1", id)
	assert.Equal(t, "t", out.CreatedAt)
	assert.Equal(t, declared, out.args, "declared inputs are kept as written")
}

func TestProject(t *testing.T) {
	t.Parallel()
	server := args{
		Name:     "renamed",
		Count:    ptr(5),
		Level:    ptr(level("high")),
		Servers:  []mcpServer{{ServerURL: "https://y"}},
		Metadata: map[string]string{"a": "1", "b": "2"},
	}

	declared := args{
		Name:     "original",
		Enabled:  ptr(true),
		Servers:  []mcpServer{{ServerURL: "https://x"}},
		Metadata: map[string]string{"a": "1"},
		Secret:   ptr("s3cret"),
		Local:    ptr("kept"),
	}
	assert.Equal(t, args{
		Name:     "renamed",                             // declared: the server's value shows drift
		Enabled:  nil,                                   // declared, but the server has none: drift
		Servers:  []mcpServer{{ServerURL: "https://y"}}, // projected element-wise
		Metadata: map[string]string{"a": "1", "b": "2"}, // maps are compared whole
		Secret:   ptr("s3cret"),                         // write-only: kept
		Local:    ptr("kept"),                           // not on the wire: kept
	}, Project(server, declared), "undeclared Count and Level stay unset despite server defaults")

	assert.Equal(t, args{}, Project(server, args{}),
		"a resource that declares nothing keeps declaring nothing; only an import adopts the server's object")
}

func TestUpdateBody(t *testing.T) {
	t.Parallel()
	old := args{Name: "a", Count: ptr(1), Enabled: ptr(true), Metadata: map[string]string{"a": "1", "b": "2"}}
	new := args{Name: "a", Count: ptr(2), Metadata: map[string]string{"a": "1"}}

	body, err := UpdateBody(old, new, map[string]any{"metadata": nil})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"count":    int64(2),
		"enabled":  nil,
		"metadata": map[string]any{"a": "1", "b": nil},
	}, body)

	body, err = UpdateBody(old, args{Name: "a", Count: ptr(1), Enabled: ptr(true)}, map[string]any{"metadata": ""})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"metadata": map[string]any{"a": "", "b": ""}}, body,
		"dropping a patch map removes each of its keys")

	body, err = UpdateBody(old, old, nil)
	require.NoError(t, err)
	assert.Empty(t, body)
}

func TestSnakeCase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"name": "name", "mcpServerName": "mcp_server_name", "caCertificatePem": "ca_certificate_pem",
	} {
		assert.Equal(t, want, snakeCase(in))
	}
}

func TestSplitID(t *testing.T) {
	t.Parallel()
	parent, child, err := SplitID(JoinID("wrkspc_1", "user_2"))
	require.NoError(t, err)
	assert.Equal(t, []string{"wrkspc_1", "user_2"}, []string{parent, child})
	_, _, err = SplitID("wrkspc_1")
	assert.Error(t, err)
}

// TestRefreshOfAResourceThatDeclaresNothing guards against mistaking a resource whose
// inputs are all optional and unset for an import. Adopting the server's defaults as
// inputs would make the next update try to remove them, or replace the resource.
func TestRefreshOfAResourceThatDeclaresNothing(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"id": "x_1", "name": "server-default", "created_at": "t"}`)

	refreshed, err := Refresh[args, state](raw, infer.ReadRequest[args, state]{
		ID: "x_1", State: state{CreatedAt: "t"},
	})
	require.NoError(t, err)
	assert.Equal(t, args{}, refreshed.Inputs)

	imported, err := Refresh[args, state](raw, infer.ReadRequest[args, state]{ID: "x_1"})
	require.NoError(t, err)
	assert.Equal(t, args{Name: "server-default"}, imported.Inputs)
}
