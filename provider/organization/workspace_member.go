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
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/roles"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// WorkspaceMember grants an organization member a role in a workspace.
type WorkspaceMember struct{}

// NewWorkspaceMember returns the complete WorkspaceMember resource.
func NewWorkspaceMember() infer.InferredResource {
	return workspaceMemberResource{infer.Resource(&WorkspaceMember{})}
}

func (m *WorkspaceMember) Annotate(a infer.Annotator) {
	a.Describe(m, "A user's membership of a workspace, with a workspace role. The user must "+
		"already be a member of the organization. The resource's ID is `<workspaceId>/<userId>`. Deleting this resource removes the workspace membership, not the user or workspace.")
}

type WorkspaceMemberArgs struct {
	WorkspaceID   string        `pulumi:"workspaceId"   provider:"replaceOnChanges"`
	UserID        string        `pulumi:"userId"        provider:"replaceOnChanges"`
	WorkspaceRole WorkspaceRole `pulumi:"workspaceRole"`
}

func (m *WorkspaceMemberArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.WorkspaceID, "The workspace's ID. Changing this value replaces the membership.")
	a.Describe(&m.UserID, "The user's ID. Changing this value replaces the membership.")
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

func (m *WorkspaceMember) Create(ctx context.Context, req infer.CreateRequest[WorkspaceMemberArgs]) (infer.CreateResponse[WorkspaceMemberState], error) {
	out := WorkspaceMemberState{WorkspaceMemberArgs: req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[WorkspaceMemberState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[WorkspaceMemberState]{}, err
	}
	if out.WorkspaceRole == workspaceBilling {
		out.WorkspaceRole = workspaceUser
	}
	r, err := c.Organization.Workspaces.Members.Add(ctx, out.WorkspaceID, sdk.OrganizationWorkspaceMemberAddParams{UserID: out.UserID, WorkspaceRole: sdk.NoBillingWorkspaceRole(out.WorkspaceRole)}, option.WithMaxRetries(0))
	if err != nil {
		return infer.CreateResponse[WorkspaceMemberState]{}, err
	}
	id := out.WorkspaceID + "/" + out.UserID
	out, err = workspaceMemberObserved(r, out.WorkspaceMemberArgs)
	if err != nil {
		return infer.CreateResponse[WorkspaceMemberState]{}, err
	}
	if req.Inputs.WorkspaceRole == workspaceBilling {
		updated, err := m.Update(ctx, infer.UpdateRequest[WorkspaceMemberArgs, WorkspaceMemberState]{ID: id, State: out, Inputs: req.Inputs})
		if err != nil {
			return infer.CreateResponse[WorkspaceMemberState]{}, err
		}
		out = updated.Output
	}
	return infer.CreateResponse[WorkspaceMemberState]{ID: id, Output: out}, nil
}

func (*WorkspaceMember) Read(ctx context.Context, req infer.ReadRequest[WorkspaceMemberArgs, WorkspaceMemberState]) (infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState], error) {
	pair, err := workspaceMemberPair(req.ID)
	if err != nil {
		return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{}, err
	}
	r, err := c.Organization.Workspaces.Members.Get(ctx, pair.UserID, sdk.OrganizationWorkspaceMemberGetParams{WorkspaceID: pair.WorkspaceID})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{}, err
	}
	out, err := workspaceMemberObserved(r, pair)
	if err != nil {
		return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{}, err
	}
	return infer.ReadResponse[WorkspaceMemberArgs, WorkspaceMemberState]{ID: req.ID, Inputs: out.WorkspaceMemberArgs, State: out}, nil
}

func (*WorkspaceMember) Update(ctx context.Context, req infer.UpdateRequest[WorkspaceMemberArgs, WorkspaceMemberState]) (infer.UpdateResponse[WorkspaceMemberState], error) {
	if req.DryRun {
		return infer.UpdateResponse[WorkspaceMemberState]{Output: WorkspaceMemberState{WorkspaceMemberArgs: req.Inputs}}, nil
	}
	pair, err := workspaceMemberPair(req.ID)
	if err != nil {
		return infer.UpdateResponse[WorkspaceMemberState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[WorkspaceMemberState]{}, err
	}
	r, err := c.Organization.Workspaces.Members.Update(ctx, pair.UserID, sdk.OrganizationWorkspaceMemberUpdateParams{WorkspaceID: pair.WorkspaceID, WorkspaceRole: sdk.WorkspaceRole(req.Inputs.WorkspaceRole)}, option.WithMaxRetries(0))
	if err != nil {
		return infer.UpdateResponse[WorkspaceMemberState]{}, err
	}
	out, err := workspaceMemberObserved(r, pair)
	if err != nil {
		return infer.UpdateResponse[WorkspaceMemberState]{}, err
	}
	return infer.UpdateResponse[WorkspaceMemberState]{Output: out}, nil
}

func (*WorkspaceMember) Delete(ctx context.Context, req infer.DeleteRequest[WorkspaceMemberState]) (infer.DeleteResponse, error) {
	pair, err := workspaceMemberPair(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Workspaces.Members.Remove(ctx, pair.UserID, sdk.OrganizationWorkspaceMemberRemoveParams{WorkspaceID: pair.WorkspaceID})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve validated Check inputs; infer handles output secrecy and knownness.
type workspaceMemberResource struct{ infer.InferredResource }

func (r workspaceMemberResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&WorkspaceMember{}).Check(ctx, req)
	if err != nil {
		return out, err
	}
	for _, k := range []string{"workspaceId", "userId"} {
		v := req.Inputs.Get(k)
		if v.IsString() && (v.AsString() == "" || strings.Contains(v.AsString(), "/")) {
			out.Failures = append(out.Failures, p.CheckFailure{Property: k, Reason: "must be nonempty and must not contain '/'"})
		}
	}
	if v := req.Inputs.Get("workspaceRole"); v.IsString() && !validWorkspaceRole(v.AsString()) {
		out.Failures = append(out.Failures, p.CheckFailure{Property: "workspaceRole", Reason: "must be a supported workspace role"})
	}
	if len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, nil
}

func (r workspaceMemberResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&WorkspaceMember{}).Diff(ctx, req)
}

func (r workspaceMemberResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&WorkspaceMember{}).Create(ctx, req)
}

func (r workspaceMemberResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&WorkspaceMember{}).Read(ctx, req)
}

func (r workspaceMemberResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&WorkspaceMember{}).Update(ctx, req)
}

func (r workspaceMemberResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&WorkspaceMember{}).Delete(ctx, req)
}

func workspaceMemberPair(id string) (WorkspaceMemberArgs, error) {
	workspaceID, userID, ok := resourceutil.SplitID(id)
	if !ok {
		return WorkspaceMemberArgs{}, fmt.Errorf("membership ID must be <workspaceId>/<userId>")
	}
	return WorkspaceMemberArgs{WorkspaceID: workspaceID, UserID: userID}, nil
}

func workspaceMemberObserved(r *sdk.WorkspaceMember, pair WorkspaceMemberArgs) (WorkspaceMemberState, error) {
	out := WorkspaceMemberState{WorkspaceMemberArgs: pair}
	if r == nil || r.WorkspaceID != pair.WorkspaceID || r.UserID != pair.UserID || r.JSON.Type.Raw() != `"workspace_member"` {
		return out, fmt.Errorf("invalid workspace membership response identity or type")
	}
	role := WorkspaceRole(r.WorkspaceRole)
	if !validWorkspaceRole(string(role)) {
		return out, fmt.Errorf("invalid workspace membership response role")
	}
	out.WorkspaceRole = role
	return out, nil
}

func validWorkspaceRole(s string) bool {
	for _, r := range roles.Workspace {
		if r.Value == s {
			return true
		}
	}
	return false
}
