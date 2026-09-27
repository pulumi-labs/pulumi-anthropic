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
	"context"
	"fmt"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"
)

// Adopted implements the lifecycle of an object the API cannot create, such as a user or
// an API key. Creating the resource adopts an existing object, identified by an input,
// and applies the declared fields to it. A field the program leaves undeclared is left
// as it is, not cleared: it is unmanaged. The embedded Resource configures the rest of
// the lifecycle.
type Adopted[I, O any, PO State[I, O]] struct {
	Resource[I, O, PO]
	// ID selects the input that holds the adopted object's ID.
	ID func(*I) *string
	// Noun names the kind of object, for errors, e.g. "API key".
	Noun string
	// Origin explains how such objects come to exist, for the error when one does not.
	Origin string
}

func (a Adopted[I, O, PO]) Create(ctx context.Context, req infer.CreateRequest[I]) (infer.CreateResponse[O], error) {
	var out O
	*PO(&out).Inputs() = req.Inputs
	if req.DryRun {
		return infer.CreateResponse[O]{Output: out}, nil
	}
	id := *a.ID(&req.Inputs)
	var raw []byte
	if err := a.Collection.Get(ctx, id, &raw); err != nil {
		if IsNotFound(err) {
			err = fmt.Errorf("%s %s does not exist; %s", a.Noun, id, a.Origin)
		}
		return infer.CreateResponse[O]{}, err
	}
	var current I
	if err := Decode(raw, &current); err != nil {
		return infer.CreateResponse[O]{}, err
	}
	raw, err := a.apply(ctx, id, current, req.Inputs, raw)
	if err != nil {
		return infer.CreateResponse[O]{}, err
	}
	out, _, err = DecodeState[I, O, PO](raw, req.Inputs)
	return infer.CreateResponse[O]{ID: id, Output: out}, err
}

// Read refreshes the object. Its ID is an input the API object does not carry, so it is
// restored from the resource ID, which is how an import learns it.
func (a Adopted[I, O, PO]) Read(ctx context.Context, req infer.ReadRequest[I, O]) (infer.ReadResponse[I, O], error) {
	resp, err := a.Resource.Read(ctx, req)
	if err != nil || resp.ID == "" {
		return resp, err
	}
	*a.ID(&resp.Inputs) = resp.ID
	*PO(&resp.State).Inputs() = resp.Inputs
	return resp, nil
}

func (a Adopted[I, O, PO]) Update(ctx context.Context, req infer.UpdateRequest[I, O]) (infer.UpdateResponse[O], error) {
	out := req.State
	*PO(&out).Inputs() = req.Inputs
	if req.DryRun {
		return infer.UpdateResponse[O]{Output: out}, nil
	}
	raw, err := a.apply(ctx, req.ID, *PO(&req.State).Inputs(), req.Inputs, nil)
	if err != nil || raw == nil {
		return infer.UpdateResponse[O]{Output: out}, err
	}
	out, _, err = DecodeState[I, O, PO](raw, req.Inputs)
	return infer.UpdateResponse[O]{Output: out}, err
}

// apply updates the object from old to new, leaving the fields new does not declare
// untouched. It returns the updated object, or unchanged if there was nothing to update.
func (a Adopted[I, O, PO]) apply(ctx context.Context, id string, old, new I, unchanged []byte) ([]byte, error) {
	body, err := UpdateBody(old, new, a.Tombstones)
	if err != nil {
		return nil, err
	}
	for k, v := range body {
		if v == nil {
			delete(body, k)
		}
	}
	if len(body) == 0 {
		return unchanged, nil
	}
	var raw []byte
	err = a.Collection.Do(ctx, http.MethodPost, a.Collection.Item(id), body, &raw)
	return raw, err
}
