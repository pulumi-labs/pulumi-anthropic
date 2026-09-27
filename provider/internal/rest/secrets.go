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

	"github.com/pulumi/pulumi-go-provider/infer"
)

// MarkSecretOutputs marks every output-only field tagged provider:"secret" as always
// secret. The framework applies the tag to inputs, and to outputs that echo them, but
// for an output-only field the tag reaches only the schema: at runtime, the value would
// be stored in plain text.
func MarkSecretOutputs[O any](f infer.FieldSelector, state *O) {
	v := reflect.ValueOf(state).Elem()
	for _, field := range fieldsOf(v.Type()) {
		if !field.embedded && field.secret {
			f.OutputField(v.Field(field.index).Addr().Interface()).AlwaysSecret()
		}
	}
}

// WireDependencies marks the resource's secret outputs; see MarkSecretOutputs.
func (r Resource[I, O, PO]) WireDependencies(f infer.FieldSelector, _ *I, state *O) {
	MarkSecretOutputs(f, state)
}
