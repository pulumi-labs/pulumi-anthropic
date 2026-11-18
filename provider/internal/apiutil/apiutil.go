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

// Package apiutil contains small, explicit helpers for Anthropic SDK responses
// and merged string maps. Request construction and lifecycle policy stay local
// to each resource.
package apiutil

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/respjson"
)

// IsStatus reports whether err wraps an official SDK error with this status.
func IsStatus(err error, status int) bool {
	var apiErr *sdk.Error
	return errors.As(err, &apiErr) && apiErr != nil && apiErr.StatusCode == status
}

func IsNotFound(err error) bool { return IsStatus(err, http.StatusNotFound) }

// MergedStringMap sends every desired entry and deletes previously managed keys
// now absent. It always returns a fresh, non-nil map. Callers decide whether the
// field should be sent at all. This is not suitable for APIs with different
// removal encodings, such as UserProfile metadata.
func MergedStringMap(previous, desired map[string]string) map[string]any {
	patch := make(map[string]any, len(previous)+len(desired))
	for k, v := range desired {
		patch[k] = v
	}
	for k := range previous {
		if _, ok := desired[k]; !ok {
			patch[k] = nil
		}
	}
	return patch
}

// JSONStringValue requires an actual JSON string, never null or a coerced number.
func JSONStringValue(raw string) (string, bool) {
	var value string
	if strings.TrimSpace(raw) == "null" || json.Unmarshal([]byte(raw), &value) != nil {
		return "", false
	}
	return value, true
}

func JSONString(raw string) bool {
	_, ok := JSONStringValue(raw)
	return ok
}

func JSONNonEmptyString(raw string) bool {
	value, ok := JSONStringValue(raw)
	return ok && value != ""
}

func JSONInt64(raw string) bool {
	var value int64
	return strings.TrimSpace(raw) != "null" && json.Unmarshal([]byte(raw), &value) == nil
}

func JSONBool(raw string) bool {
	var value bool
	return strings.TrimSpace(raw) != "null" && json.Unmarshal([]byte(raw), &value) == nil
}

// StringField and the other field checks require both SDK validity and the
// documented JSON shape. SDK validity alone permits some primitive coercions.
func StringField(field respjson.Field) bool {
	return field.Valid() && JSONString(field.Raw())
}

func Int64Field(field respjson.Field) bool {
	return field.Valid() && JSONInt64(field.Raw())
}

func BoolField(field respjson.Field) bool {
	return field.Valid() && JSONBool(field.Raw())
}

func ObjectField(field respjson.Field) bool {
	raw := strings.TrimSpace(field.Raw())
	return field.Valid() && strings.HasPrefix(raw, "{") && json.Valid([]byte(raw))
}

func StringListField(field respjson.Field) bool {
	raw := strings.TrimSpace(field.Raw())
	if !field.Valid() || !strings.HasPrefix(raw, "[") {
		return false
	}
	var values []json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil {
		return false
	}
	for _, value := range values {
		if !JSONString(string(value)) {
			return false
		}
	}
	return true
}

func StringMapField(field respjson.Field) bool {
	raw := strings.TrimSpace(field.Raw())
	if !field.Valid() || !strings.HasPrefix(raw, "{") {
		return false
	}
	var values map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &values) != nil {
		return false
	}
	for _, value := range values {
		if !JSONString(string(value)) {
			return false
		}
	}
	return true
}
