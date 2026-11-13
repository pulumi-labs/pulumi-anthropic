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

// Package resourceutil contains pure resource identity helpers,
// not lifecycle controllers or state-conversion policy.
package resourceutil

import "strings"

// SplitID accepts exactly two nonempty slash-separated components. Resources
// supply the component meanings and their own import/validation diagnostics.
func SplitID(id string) (string, string, bool) {
	first, second, ok := strings.Cut(id, "/")
	if !ok || first == "" || second == "" || strings.Contains(second, "/") {
		return "", "", false
	}
	return first, second, true
}
