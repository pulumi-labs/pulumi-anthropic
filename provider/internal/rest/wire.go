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
	"fmt"
)

// typeTag is the wire form of an enumerated value that the API wraps in an object, such
// as {"type": "always_allow"}.
type typeTag struct {
	Type string `json:"type"`
}

// MarshalTypeTag encodes s as {"type": s}.
func MarshalTypeTag(s string) ([]byte, error) {
	return json.Marshal(typeTag{Type: s})
}

// UnmarshalTypeTag decodes {"type": s}, or a bare string s, which the API also accepts.
func UnmarshalTypeTag(raw []byte) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var tag typeTag
	if err := json.Unmarshal(raw, &tag); err != nil {
		return "", fmt.Errorf("expected a string or an object with a type: %w", err)
	}
	return tag.Type, nil
}
