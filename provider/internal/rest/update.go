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

import "reflect"

// UpdateBody computes the partial-update request body that takes an object from the old
// inputs to the new ones. Only changed top-level fields are sent, which also keeps
// immutable fields (whose changes are replacements, not updates) out of the body. Fields
// tagged wire:"createOnly" are never sent.
//
// Anything the program no longer declares is cleared by sending null, at every depth: a
// changed object is sent whole, so that an endpoint replacing it gets all of it, along
// with null for each key that was removed, so that an endpoint merging it drops them.
//
// Fields named in tombstones are maps with patch semantics whose key removals take a
// value other than null, such as "".
func UpdateBody[I any](old, new I, tombstones map[string]any) (map[string]any, error) {
	oldWire, err := wireFields(old)
	if err != nil {
		return nil, err
	}
	newWire, err := wireFields(new)
	if err != nil {
		return nil, err
	}

	body := map[string]any{}
	for k := range union(oldWire, newWire) {
		o, n := oldWire[k], newWire[k]
		if reflect.DeepEqual(o, n) {
			continue
		}
		tombstone, patchMap := tombstones[k]
		switch oldMap, _ := o.(map[string]any); {
		case n == nil && patchMap && oldMap != nil:
			body[k] = withRemovals(oldMap, map[string]any{}, tombstone)
		case patchMap:
			body[k] = withRemovals(oldMap, n, tombstone)
		default:
			body[k] = withRemovals(o, n, nil)
		}
	}
	return body, nil
}

// withRemovals returns new, adding the tombstone for each key of old that new lacks,
// recursively through objects present in both.
func withRemovals(old, new any, tombstone any) any {
	oldMap, _ := old.(map[string]any)
	newMap, ok := new.(map[string]any)
	if !ok || oldMap == nil {
		return new
	}
	out := make(map[string]any, len(newMap))
	for k, v := range newMap {
		out[k] = withRemovals(oldMap[k], v, tombstone)
	}
	for k := range oldMap {
		if _, ok := newMap[k]; !ok {
			out[k] = tombstone
		}
	}
	return out
}

func union(a, b map[string]any) map[string]struct{} {
	keys := map[string]struct{}{}
	for k := range a {
		keys[k] = struct{}{}
	}
	for k := range b {
		keys[k] = struct{}{}
	}
	return keys
}

// wireFields returns the top-level fields of v's API encoding for an update.
func wireFields(v any) (map[string]any, error) {
	tree, err := encodeUpdate(v)
	if err != nil {
		return nil, err
	}
	fields, _ := tree.(map[string]any)
	return fields, nil
}
