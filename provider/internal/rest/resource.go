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
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// EndOfLife is what deleting a resource does in the Anthropic API. A Pulumi delete
// performs the strongest end-of-life operation the API offers for the resource.
type EndOfLife int

const (
	// Delete hard-deletes the resource. The zero EndOfLife is deliberately invalid, so a
	// resource that forgets to declare one fails rather than defaulting to deletion.
	Delete EndOfLife = iota + 1
	// Archive archives the resource: it becomes read-only and cannot be restored.
	Archive
	// Forget leaves the resource in place, because the API cannot remove it.
	Forget
)

// State is the constraint on a resource's output type O: a pointer to it exposes the
// embedded inputs I, which always hold exactly what the program declared.
type State[I, O any] interface {
	*O
	Inputs() *I
}

// Resource implements Create, Read, Update, and Delete for a resource that follows the
// API's REST conventions. Its input type I and output type O are wire-shaped: I encodes
// to the create request body, and a response decodes into O. Optional fields of I must
// be pointers, slices, or maps, so that "unset" is distinguishable from a zero value.
//
// A resource embeds Resource to inherit its lifecycle, and overrides methods where the
// API departs from the conventions.
type Resource[I, O any, PO State[I, O]] struct {
	Collection Collection
	EndOfLife  EndOfLife
	// Tombstones names the map-valued fields with patch semantics on update, such as
	// metadata, mapped to the value that removes a key.
	Tombstones map[string]any
	// DeleteBeforeReplace is set when a replacement would collide with the original,
	// because the API requires a field that forces replacement to be unique.
	DeleteBeforeReplace bool
}

// Diff compares the declared inputs; see [Diff].
func (r Resource[I, O, PO]) Diff(
	_ context.Context, req infer.DiffRequest[I, O],
) (infer.DiffResponse, error) {
	diff := Diff(*PO(&req.State).Inputs(), req.Inputs)
	diff.DeleteBeforeReplace = r.DeleteBeforeReplace && IsReplacement(diff)
	return diff, nil
}

// Create creates the resource.
func (r Resource[I, O, PO]) Create(
	ctx context.Context, req infer.CreateRequest[I],
) (infer.CreateResponse[O], error) {
	var out O
	*PO(&out).Inputs() = req.Inputs
	if req.DryRun {
		return infer.CreateResponse[O]{Output: out}, nil
	}
	body, err := Encode(req.Inputs)
	if err != nil {
		return infer.CreateResponse[O]{}, err
	}
	var raw []byte
	if err := r.Collection.Do(ctx, http.MethodPost, r.Collection.Path, body, &raw); err != nil {
		return infer.CreateResponse[O]{}, err
	}
	out, id, err := DecodeState[I, O, PO](raw, req.Inputs)
	return infer.CreateResponse[O]{ID: id, Output: out}, InitFailed(err)
}

// InitFailed reports an error from a Create whose object the API has already created:
// the resource is recorded with the state it reached rather than forgotten, so the next
// update finishes it instead of orphaning it and creating another.
func InitFailed(err error) error {
	if err == nil {
		return nil
	}
	return infer.ResourceInitFailedError{Reasons: []string{err.Error()}}
}

// Read refreshes the resource. An archived resource reads as deleted.
func (r Resource[I, O, PO]) Read(
	ctx context.Context, req infer.ReadRequest[I, O],
) (infer.ReadResponse[I, O], error) {
	var raw []byte
	if err := r.Collection.Get(ctx, req.ID, &raw); err != nil {
		if IsNotFound(err) {
			return infer.ReadResponse[I, O]{}, nil
		}
		return infer.ReadResponse[I, O]{}, err
	}
	return Refresh[I, O, PO](raw, req)
}

// Update applies the changed inputs.
func (r Resource[I, O, PO]) Update(
	ctx context.Context, req infer.UpdateRequest[I, O],
) (infer.UpdateResponse[O], error) {
	return r.UpdateWith(ctx, req, nil)
}

// UpdateWith applies the changed inputs, adding extra fields to a non-empty request
// body. It exists for fields such as an optimistic-concurrency version, which the body
// must carry but which are not inputs.
func (r Resource[I, O, PO]) UpdateWith(
	ctx context.Context, req infer.UpdateRequest[I, O], extra map[string]any,
) (infer.UpdateResponse[O], error) {
	out := req.State
	*PO(&out).Inputs() = req.Inputs
	body, err := UpdateBody(*PO(&req.State).Inputs(), req.Inputs, r.Tombstones)
	if err != nil || req.DryRun || len(body) == 0 {
		return infer.UpdateResponse[O]{Output: out}, err
	}
	for k, v := range extra {
		body[k] = v
	}
	var raw []byte
	if err := r.Collection.Do(ctx, http.MethodPost, r.Collection.Item(req.ID), body, &raw); err != nil {
		return infer.UpdateResponse[O]{}, err
	}
	out, _, err = DecodeState[I, O, PO](raw, req.Inputs)
	return infer.UpdateResponse[O]{Output: out}, err
}

// Delete ends the resource's life according to r.EndOfLife. A resource that is already
// gone is not an error.
func (r Resource[I, O, PO]) Delete(ctx context.Context, req infer.DeleteRequest[O]) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, End(ctx, r.Collection, req.ID, r.EndOfLife)
}

// End performs an end-of-life operation on the item with the given ID.
func End(ctx context.Context, c Collection, id string, eol EndOfLife) error {
	var err error
	switch eol {
	case Delete:
		err = c.Do(ctx, http.MethodDelete, c.Item(id), nil, nil)
	case Archive:
		err = c.Action(ctx, id, "archive", nil)
	case Forget:
		p.GetLogger(ctx).Warningf("%s cannot be deleted through the Anthropic API; "+
			"it has been removed from the stack but still exists", id)
	default:
		return fmt.Errorf("provider bug: %s declares no end-of-life", c.Path)
	}
	if IsNotFound(err) {
		return nil
	}
	return err
}

// envelope holds the fields every API object shares that the lifecycle depends on.
type envelope struct {
	ID         string  `json:"id"`
	ArchivedAt *string `json:"archived_at"`
}

// DecodeState decodes an API object into the output type, keeping the declared inputs
// as they were written rather than as the server echoed them back.
func DecodeState[I, O any, PO State[I, O]](raw []byte, inputs I) (O, string, error) {
	var out O
	var env envelope
	if err := Decode(raw, &out); err != nil {
		return out, "", fmt.Errorf("decoding API response: %w", err)
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return out, "", fmt.Errorf("decoding API response: %w", err)
	}
	*PO(&out).Inputs() = inputs
	return out, env.ID, nil
}

// IsImport reports whether a read is an import, which has no prior state. A resource
// whose inputs are all optional and unset has prior state nonetheless: its outputs.
func IsImport[I, O any](req infer.ReadRequest[I, O]) bool {
	return reflect.ValueOf(req.State).IsZero()
}

// Refresh builds a Read response from an API object. An archived object reads as
// deleted. Inputs are projected onto what the program declared (see Project), and an
// import, which has no prior state at all, adopts the server's values wholesale.
func Refresh[I, O any, PO State[I, O]](raw []byte, req infer.ReadRequest[I, O]) (infer.ReadResponse[I, O], error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return infer.ReadResponse[I, O]{}, fmt.Errorf("decoding API response: %w", err)
	}
	if env.ArchivedAt != nil {
		return infer.ReadResponse[I, O]{}, nil
	}
	var server I
	if err := Decode(raw, &server); err != nil {
		return infer.ReadResponse[I, O]{}, fmt.Errorf("decoding API response: %w", err)
	}
	inputs := server
	if !IsImport(req) {
		inputs = Project(server, *PO(&req.State).Inputs())
	}
	out, _, err := DecodeState[I, O, PO](raw, inputs)
	return infer.ReadResponse[I, O]{ID: req.ID, Inputs: inputs, State: out}, err
}
