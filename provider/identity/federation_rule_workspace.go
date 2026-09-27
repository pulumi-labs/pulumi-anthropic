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

package identity

import (
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// FederationRuleWorkspace binds a federation rule to a workspace.
type FederationRuleWorkspace struct {
	rest.Child[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState, *FederationRuleWorkspaceState]
}

// NewFederationRuleWorkspace returns the FederationRuleWorkspace resource controller.
func NewFederationRuleWorkspace() *FederationRuleWorkspace {
	return &FederationRuleWorkspace{
		rest.Child[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState, *FederationRuleWorkspaceState]{
			Resource: rest.Resource[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState, *FederationRuleWorkspaceState]{
				Collection: federationRuleCollection,
				EndOfLife:  rest.Delete,
			},
			Name:     "workspaces",
			ParentID: func(a *FederationRuleWorkspaceArgs) *string { return &a.FederationRuleID },
			ChildID:  func(a *FederationRuleWorkspaceArgs) *string { return &a.WorkspaceID },
			Listed:   true,
		},
	}
}

func (f *FederationRuleWorkspace) Annotate(a infer.Annotator) {
	a.Describe(f, "Binds a federation rule to a workspace, so that the rule's access tokens can "+
		"act in it. The resource's ID is `<federation-rule-id>/<workspace-id>`.\n\n"+
		"Do not also bind the workspace the rule was created in (its `workspaceId`): that "+
		"binding belongs to the rule.\n\n"+
		"Managing federation requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`).")
}

type FederationRuleWorkspaceArgs struct {
	// The rule ID is part of the endpoint's path rather than the request body.
	FederationRuleID string `pulumi:"federationRuleId" provider:"replaceOnChanges" wire:"-"`
	WorkspaceID      string `pulumi:"workspaceId"      provider:"replaceOnChanges"`
}

func (f *FederationRuleWorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.FederationRuleID, "The federation rule's ID.")
	a.Describe(&f.WorkspaceID, "The workspace's ID.")
}

type FederationRuleWorkspaceState struct {
	FederationRuleWorkspaceArgs
	WorkspaceName string `pulumi:"workspaceName"`
	CreatedAt     string `pulumi:"createdAt"`
}

func (f *FederationRuleWorkspaceState) Inputs() *FederationRuleWorkspaceArgs {
	return &f.FederationRuleWorkspaceArgs
}

func (f *FederationRuleWorkspaceState) Annotate(a infer.Annotator) {
	a.Describe(&f.WorkspaceName, "The workspace's name when the binding was created.")
	a.Describe(&f.CreatedAt, "When the binding was created, in RFC 3339 format.")
}
