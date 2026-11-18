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

package apiutil_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/respjson"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/stretchr/testify/require"
)

func TestStatus(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			apiErr := &sdk.Error{StatusCode: status}
			for _, err := range []error{apiErr, fmt.Errorf("wrapped: %w", apiErr), errors.Join(errors.New("other"), apiErr)} {
				require.True(t, apiutil.IsStatus(err, status))
				require.False(t, apiutil.IsStatus(err, 201))
				require.Equal(t, status == 404, apiutil.IsNotFound(err))
			}
		})
	}
	var typedNil *sdk.Error
	for _, err := range []error{nil, typedNil, errors.New("404 Not Found"), errors.New("connection reset")} {
		require.False(t, apiutil.IsStatus(err, 404))
		require.False(t, apiutil.IsNotFound(err))
	}
}

func TestMergedStringMap(t *testing.T) {
	for _, tt := range []struct {
		name              string
		previous, desired map[string]string
		want              map[string]any
	}{
		{"omitted", nil, nil, map[string]any{}},
		{"empty", map[string]string{}, map[string]string{}, map[string]any{}},
		{"add", nil, map[string]string{"a": "value"}, map[string]any{"a": "value"}},
		{"unchanged", map[string]string{"a": "same"}, map[string]string{"a": "same"}, map[string]any{"a": "same"}},
		{"update", map[string]string{"a": "old"}, map[string]string{"a": "new"}, map[string]any{"a": "new"}},
		{"remove", map[string]string{"a": "old"}, nil, map[string]any{"a": nil}},
		{"explicit empty", map[string]string{"a": "old"}, map[string]string{}, map[string]any{"a": nil}},
		{"retain empty value", map[string]string{"a": "old"}, map[string]string{"a": ""}, map[string]any{"a": ""}},
		{"mixed", map[string]string{"keep": "x", "gone": "y"}, map[string]string{"keep": "x", "new": "z"}, map[string]any{"keep": "x", "gone": nil, "new": "z"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old, desired := maps.Clone(tt.previous), maps.Clone(tt.desired)
			got := apiutil.MergedStringMap(tt.previous, tt.desired)
			require.NotNil(t, got)
			require.Equal(t, tt.want, got)
			got["injected"] = "not shared"
			require.Equal(t, old, tt.previous)
			require.Equal(t, desired, tt.desired)
			require.Equal(t, tt.want, apiutil.MergedStringMap(tt.previous, tt.desired))
		})
	}
	shared := map[string]string{"a": "same"}
	require.Equal(t, map[string]any{"a": "same"}, apiutil.MergedStringMap(shared, shared))
	require.Equal(t, map[string]string{"a": "same"}, shared)
}

func TestJSONPrimitives(t *testing.T) {
	for _, tt := range []struct {
		name  string
		check func(string) bool
		valid []string
	}{
		{"string", apiutil.JSONString, []string{`""`, `"hello"`, ` "null" `, `"\u0061"`}},
		{"nonempty string", apiutil.JSONNonEmptyString, []string{`"hello"`, `" "`}},
		{"int64", apiutil.JSONInt64, []string{"0", "-1", " 123 ", "-9223372036854775808", "9223372036854775807"}},
		{"bool", apiutil.JSONBool, []string{"true", "false", " false "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, raw := range tt.valid {
				require.True(t, tt.check(raw), "raw %q", raw)
			}
			for _, raw := range []string{"", " ", "null", " null ", "[]", "{}", "{", "[", "true false", `"unterminated`} {
				require.False(t, tt.check(raw), "raw %q", raw)
			}
		})
	}
	for _, raw := range []string{"123", "true", "false"} {
		require.False(t, apiutil.JSONString(raw))
		require.False(t, apiutil.JSONNonEmptyString(raw))
	}
	require.False(t, apiutil.JSONNonEmptyString(`""`))
	for _, raw := range []string{`"1"`, "1.0", "1e3", "9223372036854775808", "-9223372036854775809", "true"} {
		require.False(t, apiutil.JSONInt64(raw), "raw %q", raw)
	}
	for _, raw := range []string{`"true"`, "0", "1", "True"} {
		require.False(t, apiutil.JSONBool(raw), "raw %q", raw)
	}
	value, ok := apiutil.JSONStringValue(` "a\n\u0062" `)
	require.True(t, ok)
	require.Equal(t, "a\nb", value)
	value, ok = apiutil.JSONStringValue("null")
	require.False(t, ok)
	require.Empty(t, value)
}

func TestResponseFields(t *testing.T) {
	for _, tt := range []struct {
		name    string
		check   func(respjson.Field) bool
		valid   []string
		invalid []string
	}{
		{"string", apiutil.StringField, []string{`""`, ` "value" `}, []string{"1", "true", "[]", "{}", `"bad`}},
		{"int64", apiutil.Int64Field, []string{"0", "-1"}, []string{`"1"`, "true", "1.5", "1e3", "9223372036854775808"}},
		{"bool", apiutil.BoolField, []string{"true", " false "}, []string{`"true"`, "1", "[]"}},
		{"object", apiutil.ObjectField, []string{"{}", ` {"a": [null, 1]} `}, []string{"[]", `"value"`, "true", "{", "{}{}"}},
		{"string list", apiutil.StringListField, []string{"[]", ` ["", "a", "\u0062"] `}, []string{"{}", `"a"`, `["a", 1]`, `["a", false]`, `["a", null]`, `[["a"]]`, `["a",]`}},
		{"string map", apiutil.StringMapField, []string{"{}", ` {"a": "", "b": "value"} `}, []string{"[]", `"a"`, `{"a": 1}`, `{"a": false}`, `{"a": null}`, `{"a": {}}`, `{"a": "x",}`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, raw := range tt.valid {
				require.True(t, tt.check(respjson.NewField(raw)), "raw %q", raw)
				require.False(t, tt.check(respjson.NewInvalidField(raw)), "invalid SDK field %q", raw)
			}
			for _, raw := range append(tt.invalid, "", " ", "null", " null ") {
				require.False(t, tt.check(respjson.NewField(raw)), "raw %q", raw)
			}
			require.False(t, tt.check(respjson.Field{}))
		})
	}
}

func TestStringFieldRejectsSDKCoercion(t *testing.T) {
	var agent sdk.BetaManagedAgentsAgent
	require.NoError(t, json.Unmarshal([]byte(`{"id":123}`), &agent))
	require.Equal(t, "123", agent.ID)
	require.False(t, apiutil.StringField(agent.JSON.ID))
}
