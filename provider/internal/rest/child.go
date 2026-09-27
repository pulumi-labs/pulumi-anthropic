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

	"github.com/pulumi/pulumi-go-provider/infer"
)

// Child implements the lifecycle of a resource that lives in a sub-collection of a parent
// object, such as a vault's credentials or a workspace's members. The sub-collection's
// path embeds the parent's ID, which is an input, so the collection is resolved per
// request; the resource's ID is JoinID(parentID, childID), so that refresh and import
// can recover both.
//
// The embedded Resource configures the lifecycle, except that its Collection is the
// parents' collection, e.g. v1/vaults, beneath which each parent's sub-collection lives.
type Child[I, O any, PO State[I, O]] struct {
	Resource[I, O, PO]
	// Name is the sub-collection's name, e.g. "credentials".
	Name string
	// ParentID selects the input that holds the parent's ID.
	ParentID func(*I) *string
	// ChildID, if set, selects the input that identifies the child, for sub-collections
	// keyed by a natural key (such as a user ID) rather than by a generated ID.
	ChildID func(*I) *string
	// Listed marks a sub-collection with no endpoint to read a single child: a child is
	// read by finding it, by ChildID, among its parent's children. It requires ChildID.
	Listed bool
}

// Of returns the conventional lifecycle over one parent's sub-collection.
func (c Child[I, O, PO]) Of(parentID string) Resource[I, O, PO] {
	r := c.Resource
	r.Collection = c.Collection.Sub(parentID, c.Name)
	return r
}

// ID returns the resource ID of the child with the given parent-relative ID.
func (c Child[I, O, PO]) ID(inputs *I, childID string) string {
	if c.ChildID != nil {
		childID = *c.ChildID(inputs)
	}
	return JoinID(*c.ParentID(inputs), childID)
}

func (c Child[I, O, PO]) Create(ctx context.Context, req infer.CreateRequest[I]) (infer.CreateResponse[O], error) {
	resp, err := c.Of(*c.ParentID(&req.Inputs)).Create(ctx, req)
	// A child that was created, even if a later step failed, is identified by its parent.
	if !req.DryRun && (err == nil || resp.ID != "") {
		resp.ID = c.ID(&req.Inputs, resp.ID)
	}
	return resp, err
}

func (c Child[I, O, PO]) Read(ctx context.Context, req infer.ReadRequest[I, O]) (infer.ReadResponse[I, O], error) {
	id := req.ID
	parentID, childID, err := SplitID(id)
	if err != nil {
		return infer.ReadResponse[I, O]{}, err
	}
	raw, err := c.fetch(ctx, parentID, childID)
	if err != nil || raw == nil {
		return infer.ReadResponse[I, O]{}, err
	}
	resp, err := Refresh[I, O, PO](raw, req)
	if err != nil || resp.ID == "" {
		return resp, err
	}
	c.Restore(&resp.Inputs, parentID, childID)
	*PO(&resp.State).Inputs() = resp.Inputs
	return resp, nil
}

// fetch returns the API object of a child, or nil if it does not exist.
func (c Child[I, O, PO]) fetch(ctx context.Context, parentID, childID string) ([]byte, error) {
	children := c.Of(parentID).Collection
	if !c.Listed {
		var raw []byte
		err := children.Get(ctx, childID, &raw)
		if IsNotFound(err) {
			return nil, nil
		}
		return raw, err
	}
	all, err := List[map[string]any](ctx, children)
	if IsNotFound(err) {
		return nil, nil
	}
	for _, obj := range all {
		raw, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		var inputs I
		if err := Decode(raw, &inputs); err != nil {
			return nil, err
		}
		if *c.ChildID(&inputs) == childID {
			return raw, nil
		}
	}
	return nil, nil
}

// Restore sets the inputs that are in the resource's ID rather than in the API object,
// which the ID is the authority on, including on import.
func (c Child[I, O, PO]) Restore(inputs *I, parentID, childID string) {
	*c.ParentID(inputs) = parentID
	if c.ChildID != nil {
		*c.ChildID(inputs) = childID
	}
}

func (c Child[I, O, PO]) Update(ctx context.Context, req infer.UpdateRequest[I, O]) (infer.UpdateResponse[O], error) {
	return c.UpdateWith(ctx, req, nil)
}

// UpdateWith is Resource.UpdateWith for a child.
func (c Child[I, O, PO]) UpdateWith(
	ctx context.Context, req infer.UpdateRequest[I, O], extra map[string]any,
) (infer.UpdateResponse[O], error) {
	parentID, childID, err := SplitID(req.ID)
	if err != nil {
		return infer.UpdateResponse[O]{}, err
	}
	req.ID = childID
	return c.Of(parentID).UpdateWith(ctx, req, extra)
}

func (c Child[I, O, PO]) Delete(ctx context.Context, req infer.DeleteRequest[O]) (infer.DeleteResponse, error) {
	parentID, childID, err := SplitID(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	return infer.DeleteResponse{}, End(ctx, c.Of(parentID).Collection, childID, c.EndOfLife)
}
