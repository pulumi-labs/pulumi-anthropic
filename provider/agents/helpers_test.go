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

package agents_test

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"
)

// secret marks a test input secret.
func secret(s string) property.Value {
	return property.New(s).WithSecret(true)
}

func requireNoOutputDependencies(t *testing.T, v property.Value) {
	t.Helper()
	require.Empty(t, v.Dependencies(), "custom-resource outputs must not forward input dependencies")
	if v.IsMap() {
		for _, x := range v.AsMap().All {
			requireNoOutputDependencies(t, x)
		}
	} else if v.IsArray() {
		for _, x := range v.AsArray().All {
			requireNoOutputDependencies(t, x)
		}
	}
}

// Compare data and knownness exactly, but allow infer to protect a secret leaf
// by marking an ancestor instead. Dependency annotations are never output data.
func requireOutputValue(t *testing.T, want, got property.Value) {
	t.Helper()
	requireNoOutputDependencies(t, got)
	var compare func(property.Value, property.Value, bool)
	compare = func(want, got property.Value, protected bool) {
		t.Helper()
		protected = protected || got.Secret()
		if want.Secret() {
			require.True(t, protected, "configured secret must be protected at its path or an ancestor")
		}
		if want.IsMap() && got.IsMap() {
			require.Equal(t, want.AsMap().Len(), got.AsMap().Len())
			for k, v := range want.AsMap().All {
				compare(v, got.AsMap().Get(k), protected)
			}
		} else if want.IsArray() && got.IsArray() {
			require.Equal(t, want.AsArray().Len(), got.AsArray().Len())
			for i, v := range want.AsArray().All {
				compare(v, got.AsArray().Get(i), protected)
			}
		} else {
			require.Equal(t, want.WithSecret(false).WithDependencies(nil), got.WithSecret(false).WithDependencies(nil))
		}
	}
	compare(want, got, false)
}
