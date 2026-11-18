// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package agents

import (
	"context"
	"fmt"
	"maps"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// MemoryStore manages container configuration, never runtime memory contents.
type MemoryStore struct{}

// NewMemoryStore returns the complete MemoryStore resource.
func NewMemoryStore() infer.InferredResource {
	return memoryStoreResource{infer.Resource(&MemoryStore{})}
}

func (m *MemoryStore) Annotate(a infer.Annotator) {
	a.Describe(m, "A workspace-scoped persistent memory store. Runtime memories and versions are not managed here. Deleting this resource permanently deletes the store and all its memories and versions. Externally archived stores are treated as absent.")
}

type MemoryStoreArgs struct {
	Name        string            `pulumi:"name"`
	Description *string           `pulumi:"description,optional"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
}

func (m *MemoryStoreArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.Name, "A non-unique name, 1–255 characters without control characters. New sessions derive the /mnt/memory/ slug by lowercasing and collapsing runs other than ASCII letters and digits to hyphens. Renaming affects new sessions, not existing mounts.")
	a.Describe(&m.Description, "Description shown to the agent, at most 1,024 characters. Removing a description you previously configured clears it.")
	a.Describe(&m.Metadata, "At most 16 key-value pairs, with keys 1–64 characters and values at most 512 characters. Removing an entry from your configuration deletes that entry.")
}

type MemoryStoreState struct {
	MemoryStoreArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *MemoryStoreState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the memory store was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the memory store's configuration was last changed, in RFC 3339 format.")
}

func (*MemoryStore) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[MemoryStoreArgs], error) {
	args, failures, err := infer.DefaultCheck[MemoryStoreArgs](ctx, req.NewInputs)
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	if v, known := memoryKnown(req.NewInputs.Get("name")); known && v.IsString() {
		n := utf8.RuneCountInString(v.AsString())
		if n < 1 || n > 255 {
			fail("name", "must contain 1–255 characters")
		}
		for _, r := range v.AsString() {
			if unicode.IsControl(r) {
				fail("name", "must not contain control characters")
				break
			}
		}
	}
	if v, known := memoryKnown(req.NewInputs.Get("description")); known && v.IsString() && utf8.RuneCountInString(v.AsString()) > 1024 {
		fail("description", "must contain at most 1,024 characters")
	}
	if v, known := memoryKnown(req.NewInputs.Get("metadata")); known && v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 pairs")
		}
		v.AsMap().All(func(k string, v property.Value) bool {
			if n := utf8.RuneCountInString(k); n < 1 || n > 64 {
				fail("metadata", "keys must contain 1–64 characters")
			}
			if !v.IsComputed() && v.IsString() && utf8.RuneCountInString(v.AsString()) > 512 {
				fail("metadata", "values must contain at most 512 characters")
			}
			return true
		})
	}
	return infer.CheckResponse[MemoryStoreArgs]{Inputs: args, Failures: failures}, err
}

func (*MemoryStore) Create(ctx context.Context, req infer.CreateRequest[MemoryStoreArgs]) (infer.CreateResponse[MemoryStoreState], error) {
	if req.DryRun {
		return infer.CreateResponse[MemoryStoreState]{Output: MemoryStoreState{MemoryStoreArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[MemoryStoreState]{}, err
	}
	r, err := c.Beta.MemoryStores.New(ctx, memoryNew(req.Inputs))
	if err != nil {
		return infer.CreateResponse[MemoryStoreState]{}, err
	}
	s, err := memoryState(r, req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[MemoryStoreState]{}, err
	}
	return infer.CreateResponse[MemoryStoreState]{ID: r.ID, Output: s}, nil
}

func (*MemoryStore) Read(ctx context.Context, req infer.ReadRequest[MemoryStoreArgs, MemoryStoreState]) (infer.ReadResponse[MemoryStoreArgs, MemoryStoreState], error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{}, err
	}
	r, err := c.Beta.MemoryStores.Get(ctx, req.ID, anthropic.BetaMemoryStoreGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{}, err
	}
	s, err := memoryState(r, req.Inputs, req.Inputs.Name == "")
	if err != nil {
		return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{}, nil
	}
	return infer.ReadResponse[MemoryStoreArgs, MemoryStoreState]{ID: req.ID, Inputs: s.MemoryStoreArgs, State: s}, nil
}

func (*MemoryStore) Update(ctx context.Context, req infer.UpdateRequest[MemoryStoreArgs, MemoryStoreState]) (infer.UpdateResponse[MemoryStoreState], error) {
	if req.DryRun {
		return infer.UpdateResponse[MemoryStoreState]{Output: MemoryStoreState{MemoryStoreArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[MemoryStoreState]{}, err
	}
	// The engine owns Diff and Read/refresh. Convert the desired mutable values
	// directly; old state is needed only for metadata's per-key deletion syntax.
	r, err := c.Beta.MemoryStores.Update(ctx, req.ID, memoryUpdateParams(req.State.MemoryStoreArgs, req.Inputs))
	if err != nil {
		return infer.UpdateResponse[MemoryStoreState]{}, err
	}
	s, err := memoryState(r, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[MemoryStoreState]{}, err
	}
	return infer.UpdateResponse[MemoryStoreState]{Output: s}, nil
}

func (*MemoryStore) Delete(ctx context.Context, req infer.DeleteRequest[MemoryStoreState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.MemoryStores.Delete(ctx, req.ID, anthropic.BetaMemoryStoreDeleteParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// infer v1.5's Check encoder drops output dependencies. This resource has no
// defaults or path-changing normalization: after typed validation succeeds its
// exact original bag is canonical. Keep this adapter local to this resource.
type memoryStoreResource struct{ infer.InferredResource }

func (r memoryStoreResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&MemoryStore{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r memoryStoreResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&MemoryStore{}).Diff(ctx, req)
}

func (r memoryStoreResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&MemoryStore{}).Create(ctx, req)
}

func (r memoryStoreResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&MemoryStore{}).Read(ctx, req)
}

func (r memoryStoreResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&MemoryStore{}).Update(ctx, req)
}

func (r memoryStoreResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&MemoryStore{}).Delete(ctx, req)
}

func memoryKnown(v property.Value) (property.Value, bool) {
	return v, !v.IsComputed()
}

func memoryNew(a MemoryStoreArgs) anthropic.BetaMemoryStoreNewParams {
	r := anthropic.BetaMemoryStoreNewParams{Name: a.Name, Metadata: maps.Clone(a.Metadata)}
	if a.Description != nil {
		r.Description = anthropic.String(*a.Description)
	}
	return r
}

func memoryUpdateParams(old, desired MemoryStoreArgs) anthropic.BetaMemoryStoreUpdateParams {
	description := ""
	if desired.Description != nil {
		description = *desired.Description
	}
	r := anthropic.BetaMemoryStoreUpdateParams{
		Name:        anthropic.String(desired.Name),
		Description: anthropic.String(description),
	}
	// The API merges metadata. Send every desired entry and delete only old
	// managed keys missing from the desired map; this is not change suppression.
	metadata := apiutil.MergedStringMap(old.Metadata, desired.Metadata)
	r.SetExtraFields(map[string]any{"metadata": metadata})
	return r
}

// Empty API defaults remain omitted only when they were not declared. Nonempty
// values are always observed so refresh does not conceal external changes.
func memoryState(r *anthropic.BetaManagedAgentsMemoryStore, prior MemoryStoreArgs, importing bool) (MemoryStoreState, error) {
	s := MemoryStoreState{}
	if r == nil {
		return s, fmt.Errorf("missing memory store response")
	}
	if r.JSON.Name.Valid() {
		s.Name = r.Name
	}
	if r.JSON.Description.Valid() && (importing || prior.Description != nil || r.Description != "") {
		s.Description = &r.Description
	}
	if r.JSON.Metadata.Valid() && (importing || prior.Metadata != nil || len(r.Metadata) > 0) {
		s.Metadata = maps.Clone(r.Metadata)
	}
	if r.JSON.CreatedAt.Valid() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if r.JSON.UpdatedAt.Valid() {
		s.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "" && raw != "null" && !r.JSON.ArchivedAt.Valid() {
		return s, fmt.Errorf("invalid memory store archive timestamp")
	}
	if r.ID == "" || !r.JSON.Name.Valid() || !r.JSON.Description.Valid() || !r.JSON.Metadata.Valid() || !r.JSON.CreatedAt.Valid() || !r.JSON.UpdatedAt.Valid() {
		return s, fmt.Errorf("incomplete memory store response")
	}
	return s, nil
}
