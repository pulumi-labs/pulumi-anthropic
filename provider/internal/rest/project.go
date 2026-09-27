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

// Project maps a server-side object onto the shape of the declared inputs, so that a
// refresh reports genuine drift without reporting the values the server fills in by
// default. The server's value is taken wherever the program declared one; wherever the
// program left a field unset, it stays unset. The rule applies recursively through
// structs and through equal-length slices, which pair up element by element.
//
// Secret inputs are write-only in the Anthropic API and never returned, so their
// declared values are kept: drift in them cannot be detected. Fields that are not on the
// wire (tagged wire:"-") are likewise kept.
func Project[I any](server, declared I) I {
	d := reflect.ValueOf(&declared).Elem()
	s := reflect.ValueOf(&server).Elem()
	project(s, d)
	return declared
}

// project overwrites the declared value d, in place, with the server value s wherever d
// is set.
func project(s, d reflect.Value) {
	if d.IsZero() {
		return
	}
	switch d.Kind() {
	case reflect.Struct:
		for _, f := range fieldsOf(d.Type()) {
			if !f.embedded && (f.secret || f.wire == "") {
				continue
			}
			project(s.Field(f.index), d.Field(f.index))
		}
	case reflect.Pointer:
		if s.IsNil() {
			d.Set(s)
			return
		}
		project(s.Elem(), d.Elem())
	case reflect.Slice:
		if s.Len() != d.Len() {
			d.Set(s)
			return
		}
		for i := 0; i < d.Len(); i++ {
			project(s.Index(i), d.Index(i))
		}
	default:
		d.Set(s)
	}
}
