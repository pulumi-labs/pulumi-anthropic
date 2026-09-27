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

// RbacGroup is a group of users that roles are granted to.
type RbacGroup struct {
	rest.Resource[RbacGroupArgs, RbacGroupState, *RbacGroupState]
}

// NewRbacGroup returns the RbacGroup resource controller.
func NewRbacGroup() *RbacGroup {
	return &RbacGroup{rest.Resource[RbacGroupArgs, RbacGroupState, *RbacGroupState]{
		Collection: rbacGroupCollection,
		EndOfLife:  rest.Delete,
	}}
}

func (g *RbacGroup) Annotate(a infer.Annotator) {
	a.Describe(g, "A role-based access control group of users. Add users with "+
		"`RbacGroupMember`. Roles are attached to groups in the organization's admin settings, "+
		"and are reported by `roleIds`.\n\n"+
		"RBAC groups are available to Claude Enterprise organizations. Groups provisioned by "+
		"SCIM cannot be changed or deleted while SCIM provisioning is active.")
}

type RbacGroupArgs struct {
	Name string `pulumi:"name"`
}

func (g *RbacGroupArgs) Annotate(a infer.Annotator) {
	a.Describe(&g.Name, "The group's name, 1 to 255 characters. Names need not be unique.")
}

type RbacGroupState struct {
	RbacGroupArgs
	RoleIDs    []string `pulumi:"roleIds,optional"`
	SourceType string   `pulumi:"sourceType"`
	CreatedAt  string   `pulumi:"createdAt"`
	UpdatedAt  string   `pulumi:"updatedAt"`
}

func (g *RbacGroupState) Inputs() *RbacGroupArgs { return &g.RbacGroupArgs }

func (g *RbacGroupState) Annotate(a infer.Annotator) {
	a.Describe(&g.RoleIDs, "The IDs of the roles attached to the group.")
	a.Describe(&g.SourceType, "How the group was provisioned: `direct` or `scim`.")
	a.Describe(&g.CreatedAt, "When the group was created, in RFC 3339 format.")
	a.Describe(&g.UpdatedAt, "When the group was last updated, in RFC 3339 format.")
}
