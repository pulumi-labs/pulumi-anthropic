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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/roles"
)

// ServiceAccountWorkspace grants a service account a role in a workspace.
type ServiceAccountWorkspace struct {
	rest.Child[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState, *ServiceAccountWorkspaceState]
}

// NewServiceAccountWorkspace returns the ServiceAccountWorkspace resource controller. The
// API manages memberships from either side; the workspace's side is used because it has
// an endpoint to read a single membership. Memberships are keyed by service account ID.
func NewServiceAccountWorkspace() *ServiceAccountWorkspace {
	return &ServiceAccountWorkspace{
		rest.Child[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState, *ServiceAccountWorkspaceState]{
			Resource: rest.Resource[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState, *ServiceAccountWorkspaceState]{
				Collection: workspaceCollection,
				EndOfLife:  rest.Delete,
			},
			Name:     "service_accounts",
			ParentID: func(a *ServiceAccountWorkspaceArgs) *string { return &a.WorkspaceID },
			ChildID:  func(a *ServiceAccountWorkspaceArgs) *string { return &a.ServiceAccountID },
		},
	}
}

func (s *ServiceAccountWorkspace) Annotate(a infer.Annotator) {
	a.Describe(s, "Grants a service account a role in a workspace. The resource's ID is "+
		"`<workspace-id>/<service-account-id>`.\n\n"+
		"Every service account is implicitly a `workspace_user` of the organization's default "+
		"workspace; that membership cannot be removed, so do not manage it with this resource.\n\n"+
		"Managing service accounts requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`).")
}

type ServiceAccountWorkspaceArgs struct {
	// The workspace ID is part of the endpoint's path rather than the request body.
	WorkspaceID      string        `pulumi:"workspaceId"      provider:"replaceOnChanges" wire:"-"`
	ServiceAccountID string        `pulumi:"serviceAccountId" provider:"replaceOnChanges"`
	WorkspaceRole    WorkspaceRole `pulumi:"workspaceRole"`
}

func (s *ServiceAccountWorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.WorkspaceID, "The workspace's ID.")
	a.Describe(&s.ServiceAccountID, "The service account's ID.")
	a.Describe(&s.WorkspaceRole, "The service account's role in the workspace.")
}

type ServiceAccountWorkspaceState struct {
	ServiceAccountWorkspaceArgs
}

func (s *ServiceAccountWorkspaceState) Inputs() *ServiceAccountWorkspaceArgs {
	return &s.ServiceAccountWorkspaceArgs
}

// WorkspaceRole is a service account's role in a workspace: any workspace role but
// workspace_billing, which only people can hold.
type WorkspaceRole string

func (WorkspaceRole) Values() []infer.EnumValue[WorkspaceRole] {
	var values []infer.EnumValue[WorkspaceRole]
	for _, r := range roles.Workspace {
		if r.Value != roles.Billing {
			values = append(values, infer.EnumValue[WorkspaceRole]{Value: WorkspaceRole(r.Value), Description: r.Description})
		}
	}
	return values
}
