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

package agents

import (
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// MemoryStore is persistent memory that agents read and write across sessions.
type MemoryStore struct {
	rest.Resource[MemoryStoreArgs, MemoryStoreState, *MemoryStoreState]
}

// NewMemoryStore returns the MemoryStore resource controller.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rest.Resource[MemoryStoreArgs, MemoryStoreState, *MemoryStoreState]{
		Collection: memoryStoreCollection,
		EndOfLife:  rest.Delete,
		Tombstones: map[string]any{"metadata": nil},
	}}
}

func (m *MemoryStore) Annotate(a infer.Annotator) {
	a.Describe(m, "A Claude Managed Agents memory store: persistent memory that agents read and "+
		"write across sessions, attached to sessions or deployments as a resource. The memories "+
		"themselves are data that agents write at runtime, so they are not managed here.\n\n"+
		"Deleting this resource deletes the store along with all of its memories and their versions.")
}

type MemoryStoreArgs struct {
	Name        string            `pulumi:"name"`
	Description *string           `pulumi:"description,optional"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
}

func (m *MemoryStoreArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.Name, "The store's name, 1 to 255 characters. It determines the path the store "+
		"is mounted at in sessions, so renaming it moves the mount.")
	a.Describe(&m.Description, "What the store holds, up to 1,024 characters. It is shown to the agent.")
	a.Describe(&m.Metadata, "Arbitrary key-value pairs.")
}

type MemoryStoreState struct {
	MemoryStoreArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *MemoryStoreState) Inputs() *MemoryStoreArgs { return &s.MemoryStoreArgs }

func (s *MemoryStoreState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the store was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the store was last updated, in RFC 3339 format.")
}
