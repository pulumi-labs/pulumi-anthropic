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
	"reflect"

	p "github.com/pulumi/pulumi-go-provider"
)

// Diff compares declared inputs, reporting each changed top-level input. A change
// replaces the resource when it touches a field tagged provider:"replaceOnChanges" at
// any depth. The framework's default diff honors the tag only on top-level inputs, which
// would turn a change to a nested immutable field, such as a credential's auth.type,
// into an update the API rejects.
//
// The framework decodes values that are unknown during a preview as zero values, so a
// preview cannot tell an input going from unset to unknown from no change at all, and
// reports none; the update itself, which sees the known value, applies it.
func Diff[I any](old, new I) p.DiffResponse {
	o, n := reflect.ValueOf(old), reflect.ValueOf(new)
	detailed := map[string]p.PropertyDiff{}
	for _, f := range fieldsOf(o.Type()) {
		of, nf := o.Field(f.index), n.Field(f.index)
		if f.embedded || equal(of, nf) {
			continue
		}
		kind := p.Update
		switch {
		case isEmpty(of):
			kind = p.Add
		case isEmpty(nf):
			kind = p.Delete
		}
		if f.replaceOnChanges || replaces(of, nf) {
			kind = AsReplace(kind)
		}
		detailed[f.name] = p.PropertyDiff{Kind: kind, InputDiff: true}
	}
	return p.DiffResponse{HasChanges: len(detailed) > 0, DetailedDiff: detailed}
}

// AsReplace returns the replacing form of a diff kind.
func AsReplace(kind p.DiffKind) p.DiffKind {
	switch kind {
	case p.Add:
		return p.AddReplace
	case p.Update:
		return p.UpdateReplace
	case p.Delete:
		return p.DeleteReplace
	}
	return kind
}

// IsReplacement reports whether a diff replaces the resource.
func IsReplacement(diff p.DiffResponse) bool {
	for _, d := range diff.DetailedDiff {
		if d.Kind == p.AddReplace || d.Kind == p.UpdateReplace || d.Kind == p.DeleteReplace {
			return true
		}
	}
	return false
}

// replaces reports whether a change from o to n touches a nested replaceOnChanges field.
// An object that is added or removed is compared with its zero value, so that adding one
// with a replaceOnChanges field set replaces the resource just as setting the field would.
func replaces(o, n reflect.Value) bool {
	switch o.Kind() {
	case reflect.Pointer:
		if o.IsNil() && n.IsNil() {
			return false
		}
		return replaces(elemOrZero(o), elemOrZero(n))
	case reflect.Struct:
		for _, f := range fieldsOf(o.Type()) {
			of, nf := o.Field(f.index), n.Field(f.index)
			if equal(of, nf) {
				continue
			}
			if f.replaceOnChanges || replaces(of, nf) {
				return true
			}
		}
	case reflect.Slice:
		for i := 0; i < min(o.Len(), n.Len()); i++ {
			if replaces(o.Index(i), n.Index(i)) {
				return true
			}
		}
	}
	return false
}

func elemOrZero(v reflect.Value) reflect.Value {
	if v.IsNil() {
		return reflect.Zero(v.Type().Elem())
	}
	return v.Elem()
}

// equal compares values, treating nil and empty slices and maps as equal: a program
// cannot distinguish them.
func equal(o, n reflect.Value) bool {
	if isEmpty(o) && isEmpty(n) {
		return true
	}
	return reflect.DeepEqual(o.Interface(), n.Interface())
}
