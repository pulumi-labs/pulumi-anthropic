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
	"reflect"
	"strings"
	"unicode"
)

// The codec translates between the provider's Pulumi-shaped structs and the API's JSON.
// It rests on one rule: an API field's name is the snake_case form of the Pulumi
// property's name, so mcpServerName is mcp_server_name on the wire. A struct therefore
// declares each field once, by its Pulumi name, and has no separate wire definition.
//
// Beyond that rule:
//   - Optional fields that are nil or empty are omitted.
//   - Map keys are data, not field names, and are never renamed.
//   - A field tagged wire:"-" is not part of the API object.
//   - A field tagged wire:"createOnly" is sent on create but never in an update, for
//     fields fixed at creation that the API rejects in an update body.
//   - A struct implementing WireExtender adds fields beyond its declared properties.
//   - A type implementing json.Marshaler or json.Unmarshaler encodes itself.

// WireExtender is implemented by structs whose API object carries fields beyond their
// declared properties: a constant "type" discriminator the program never chooses, or an
// explicit value for an unset field that the API would otherwise leave unchanged.
type WireExtender interface {
	ExtendWire(obj map[string]any)
}

// Encode converts a Pulumi-shaped value to its API form: a tree of maps, slices, and
// scalars that encoding/json can marshal.
func Encode(v any) (any, error) {
	return encoder{}.encode(reflect.ValueOf(v))
}

// encodeUpdate is Encode for an update body, which leaves out createOnly fields.
func encodeUpdate(v any) (any, error) {
	return encoder{update: true}.encode(reflect.ValueOf(v))
}

type encoder struct {
	update bool
}

// Decode converts API JSON to a Pulumi-shaped value. Unrecognized fields are ignored.
func Decode(raw []byte, v any) error {
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	return decode(tree, reflect.ValueOf(v).Elem())
}

var (
	marshalerType   = reflect.TypeFor[json.Marshaler]()
	unmarshalerType = reflect.TypeFor[json.Unmarshaler]()
)

func (e encoder) encode(v reflect.Value) (any, error) {
	if !v.IsValid() {
		return nil, nil
	}
	if v.Type().Implements(marshalerType) {
		raw, err := json.Marshal(v.Interface())
		if err != nil {
			return nil, err
		}
		var tree any
		err = json.Unmarshal(raw, &tree)
		return tree, err
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return nil, nil
		}
		return e.encode(v.Elem())
	case reflect.Struct:
		obj := map[string]any{}
		if err := e.encodeFields(v, obj); err != nil {
			return nil, err
		}
		if ext, ok := v.Interface().(WireExtender); ok {
			ext.ExtendWire(obj)
		}
		return obj, nil
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		elems := make([]any, v.Len())
		for i := range elems {
			elem, err := e.encode(v.Index(i))
			if err != nil {
				return nil, err
			}
			elems[i] = elem
		}
		return elems, nil
	case reflect.Map:
		if v.IsNil() {
			return nil, nil
		}
		obj := make(map[string]any, v.Len())
		for iter := v.MapRange(); iter.Next(); {
			elem, err := e.encode(iter.Value())
			if err != nil {
				return nil, err
			}
			obj[iter.Key().String()] = elem
		}
		return obj, nil
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Float64:
		return v.Float(), nil
	default:
		return nil, fmt.Errorf("cannot encode %s to the API", v.Type())
	}
}

func (e encoder) encodeFields(v reflect.Value, obj map[string]any) error {
	for _, f := range fieldsOf(v.Type()) {
		fv := v.Field(f.index)
		if f.embedded {
			if err := e.encodeFields(fv, obj); err != nil {
				return err
			}
			continue
		}
		if f.wire == "" || (f.optional && isEmpty(fv)) || (e.update && f.createOnly) {
			continue
		}
		value, err := e.encode(fv)
		if err != nil {
			return fmt.Errorf("%s: %w", f.wire, err)
		}
		obj[f.wire] = value
	}
	return nil
}

func decode(tree any, v reflect.Value) error {
	if tree == nil {
		v.SetZero()
		return nil
	}
	if reflect.PointerTo(v.Type()).Implements(unmarshalerType) {
		raw, err := json.Marshal(tree)
		if err != nil {
			return err
		}
		return v.Addr().Interface().(json.Unmarshaler).UnmarshalJSON(raw)
	}
	switch v.Kind() {
	case reflect.Pointer:
		elem := reflect.New(v.Type().Elem())
		if err := decode(tree, elem.Elem()); err != nil {
			return err
		}
		v.Set(elem)
		return nil
	case reflect.Interface:
		v.Set(reflect.ValueOf(tree))
		return nil
	case reflect.Struct:
		obj, ok := tree.(map[string]any)
		if !ok {
			return fmt.Errorf("expected an object for %s, got %T", v.Type(), tree)
		}
		return decodeFields(obj, v)
	case reflect.Slice:
		elems, ok := tree.([]any)
		if !ok {
			return fmt.Errorf("expected an array for %s, got %T", v.Type(), tree)
		}
		slice := reflect.MakeSlice(v.Type(), len(elems), len(elems))
		for i, elem := range elems {
			if err := decode(elem, slice.Index(i)); err != nil {
				return err
			}
		}
		v.Set(slice)
		return nil
	case reflect.Map:
		obj, ok := tree.(map[string]any)
		if !ok {
			return fmt.Errorf("expected an object for %s, got %T", v.Type(), tree)
		}
		m := reflect.MakeMapWithSize(v.Type(), len(obj))
		for k, elem := range obj {
			value := reflect.New(v.Type().Elem()).Elem()
			if err := decode(elem, value); err != nil {
				return err
			}
			m.SetMapIndex(reflect.ValueOf(k).Convert(v.Type().Key()), value)
		}
		v.Set(m)
		return nil
	}
	value := reflect.ValueOf(tree)
	if value.Kind() == reflect.Float64 && v.Kind() >= reflect.Int && v.Kind() <= reflect.Int64 {
		v.SetInt(int64(value.Float()))
		return nil
	}
	if !value.Type().ConvertibleTo(v.Type()) || value.Kind() != scalarKind(v.Kind()) {
		return fmt.Errorf("expected %s, got %T", v.Type(), tree)
	}
	v.Set(value.Convert(v.Type()))
	return nil
}

func decodeFields(obj map[string]any, v reflect.Value) error {
	for _, f := range fieldsOf(v.Type()) {
		fv := v.Field(f.index)
		if f.embedded {
			if err := decodeFields(obj, fv); err != nil {
				return err
			}
			continue
		}
		tree, present := obj[f.wire]
		if f.wire == "" || !present {
			continue
		}
		if err := decode(tree, fv); err != nil {
			return fmt.Errorf("%s: %w", f.wire, err)
		}
	}
	return nil
}

// scalarKind is the kind a JSON scalar decodes to for a Go kind.
func scalarKind(k reflect.Kind) reflect.Kind {
	if k == reflect.Float32 {
		return reflect.Float64
	}
	return k
}

// isEmpty reports whether an optional field is unset.
func isEmpty(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return v.IsNil()
	case reflect.Slice, reflect.Map, reflect.String:
		return v.Len() == 0
	default:
		return v.IsZero()
	}
}

// snakeCase converts a camelCase Pulumi property name to its snake_case API name.
func snakeCase(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
