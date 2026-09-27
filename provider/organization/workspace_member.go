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

package organization

import (
	"context"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
	"github.com/pulumi/pulumi-anthropic/provider/internal/roles"
)

// WorkspaceMember grants an organization member a role in a workspace.
type WorkspaceMember struct {
	rest.Child[WorkspaceMemberArgs, WorkspaceMemberState, *WorkspaceMemberState]
}

// NewWorkspaceMember returns the WorkspaceMember resource controller.
func NewWorkspaceMember() *WorkspaceMember {
	return &WorkspaceMember{rest.Child[WorkspaceMemberArgs, WorkspaceMemberState, *WorkspaceMemberState]{
		Resource: rest.Resource[WorkspaceMemberArgs, WorkspaceMemberState, *WorkspaceMemberState]{
			Collection: workspaceCollection,
			EndOfLife:  rest.Delete,
		},
		Name:     "members",
		ParentID: func(a *WorkspaceMemberArgs) *string { return &a.WorkspaceID },
		ChildID:  func(a *WorkspaceMemberArgs) *string { return &a.UserID },
	}}
}

func (m *WorkspaceMember) Annotate(a infer.Annotator) {
	a.Describe(m, "A user's membership of a workspace, with a workspace role. The user must "+
		"already be a member of the organization. The resource's ID is `<workspaceId>/<userId>`.")
}

type WorkspaceMemberArgs struct {
	WorkspaceID   string        `pulumi:"workspaceId"   provider:"replaceOnChanges" wire:"-"`
	UserID        string        `pulumi:"userId"        provider:"replaceOnChanges"`
	WorkspaceRole WorkspaceRole `pulumi:"workspaceRole"`
}

func (m *WorkspaceMemberArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.WorkspaceID, "The workspace's ID.")
	a.Describe(&m.UserID, "The user's ID.")
	a.Describe(&m.WorkspaceRole, "The user's role in the workspace.")
}

// WorkspaceRole is a member's role in a workspace.
type WorkspaceRole string

// The API accepts workspace_billing only when updating a membership, so a billing
// member is first added as a workspace_user.
const (
	workspaceUser    WorkspaceRole = "workspace_user"
	workspaceBilling WorkspaceRole = roles.Billing
)

func (WorkspaceRole) Values() []infer.EnumValue[WorkspaceRole] {
	var values []infer.EnumValue[WorkspaceRole]
	for _, r := range roles.Workspace {
		description := r.Description
		if r.Value == roles.Billing {
			description += " The API only grants this role to an existing member, so the " +
				"provider adds the member as `workspace_user` first, then grants it."
		}
		values = append(values, infer.EnumValue[WorkspaceRole]{Value: WorkspaceRole(r.Value), Description: description})
	}
	return values
}

type WorkspaceMemberState struct {
	WorkspaceMemberArgs
}

func (s *WorkspaceMemberState) Inputs() *WorkspaceMemberArgs { return &s.WorkspaceMemberArgs }

var _ infer.CustomCreate[WorkspaceMemberArgs, WorkspaceMemberState] = (*WorkspaceMember)(nil)

// Create adds the member. The API grants workspace_billing only to an existing member,
// so a billing member is added as a workspace_user, then granted the role.
func (m *WorkspaceMember) Create(
	ctx context.Context, req infer.CreateRequest[WorkspaceMemberArgs],
) (infer.CreateResponse[WorkspaceMemberState], error) {
	if req.Inputs.WorkspaceRole != workspaceBilling {
		return m.Child.Create(ctx, req)
	}
	asUser := req
	asUser.Inputs.WorkspaceRole = workspaceUser
	resp, err := m.Child.Create(ctx, asUser)
	if err != nil || req.DryRun {
		resp.Output.WorkspaceMemberArgs = req.Inputs
		return resp, err
	}
	updated, err := m.Child.Update(ctx, infer.UpdateRequest[WorkspaceMemberArgs, WorkspaceMemberState]{
		ID: resp.ID, State: resp.Output, Inputs: req.Inputs,
	})
	if err != nil {
		// The member exists as a workspace_user; the next update grants the role.
		return resp, rest.InitFailed(err)
	}
	return infer.CreateResponse[WorkspaceMemberState]{ID: resp.ID, Output: updated.Output}, nil
}
