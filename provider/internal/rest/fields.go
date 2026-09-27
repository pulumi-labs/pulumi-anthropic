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
	"strings"
	"sync"
)

// field describes a struct field as the provider sees it. Its tags are parsed once per
// type: pulumi:"name[,optional]" names the property, provider:"secret" and
// provider:"replaceOnChanges" qualify it, and wire:"-" or wire:"createOnly" say how it
// appears in the API object.
type field struct {
	// index is the field's index in its struct.
	index int
	// embedded marks an anonymous struct field, whose fields are the struct's own.
	embedded bool
	// name is the Pulumi property name.
	name string
	// wire is the API field name, or "" for a field that is not part of the API object.
	wire string

	optional, secret, replaceOnChanges, createOnly bool
}

var fieldCache sync.Map // reflect.Type -> []field

// fieldsOf returns the fields of a struct type that the provider sees: those with a
// pulumi tag, and embedded structs.
func fieldsOf(t reflect.Type) []field {
	if cached, ok := fieldCache.Load(t); ok {
		return cached.([]field)
	}
	var fields []field
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.Anonymous {
			fields = append(fields, field{index: i, embedded: true})
			continue
		}
		tag, ok := sf.Tag.Lookup("pulumi")
		if !ok || !sf.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		provider, wire := sf.Tag.Get("provider"), sf.Tag.Get("wire")
		f := field{
			index:            i,
			name:             name,
			wire:             snakeCase(name),
			optional:         strings.Contains(opts, "optional"),
			secret:           strings.Contains(provider, "secret"),
			replaceOnChanges: strings.Contains(provider, "replaceOnChanges"),
			createOnly:       wire == "createOnly",
		}
		if wire == "-" {
			f.wire = ""
		}
		fields = append(fields, f)
	}
	fieldCache.Store(t, fields)
	return fields
}
