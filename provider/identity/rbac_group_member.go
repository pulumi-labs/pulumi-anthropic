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

// RbacGroupMember adds a user to an RBAC group.
type RbacGroupMember struct {
	rest.Child[RbacGroupMemberArgs, RbacGroupMemberState, *RbacGroupMemberState]
}

// NewRbacGroupMember returns the RbacGroupMember resource controller.
func NewRbacGroupMember() *RbacGroupMember {
	return &RbacGroupMember{rest.Child[RbacGroupMemberArgs, RbacGroupMemberState, *RbacGroupMemberState]{
		Resource: rest.Resource[RbacGroupMemberArgs, RbacGroupMemberState, *RbacGroupMemberState]{
			Collection: rbacGroupCollection,
			EndOfLife:  rest.Delete,
		},
		Name:     "members",
		ParentID: func(a *RbacGroupMemberArgs) *string { return &a.RbacGroupID },
		ChildID:  func(a *RbacGroupMemberArgs) *string { return &a.UserID },
		Listed:   true,
	}}
}

func (m *RbacGroupMember) Annotate(a infer.Annotator) {
	a.Describe(m, "Adds a user to an RBAC group. The resource's ID is `<rbac-group-id>/<user-id>`.\n\n"+
		"RBAC groups are available to Claude Enterprise organizations.")
}

type RbacGroupMemberArgs struct {
	// The group ID is part of the endpoint's path rather than the request body.
	RbacGroupID string `pulumi:"rbacGroupId" provider:"replaceOnChanges" wire:"-"`
	UserID      string `pulumi:"userId"      provider:"replaceOnChanges"`
}

func (m *RbacGroupMemberArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.RbacGroupID, "The group's ID.")
	a.Describe(&m.UserID, "The user's ID.")
}

type RbacGroupMemberState struct {
	RbacGroupMemberArgs
	Email     string `pulumi:"email"`
	CreatedAt string `pulumi:"createdAt"`
}

func (m *RbacGroupMemberState) Inputs() *RbacGroupMemberArgs { return &m.RbacGroupMemberArgs }

func (m *RbacGroupMemberState) Annotate(a infer.Annotator) {
	a.Describe(&m.Email, "The user's email address.")
	a.Describe(&m.CreatedAt, "When the user was added to the group, in RFC 3339 format.")
}
