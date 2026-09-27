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
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Invite invites a person to join the organization.
type Invite struct {
	rest.Resource[InviteArgs, InviteState, *InviteState]
}

// NewInvite returns the Invite resource controller.
func NewInvite() *Invite {
	return &Invite{rest.Resource[InviteArgs, InviteState, *InviteState]{
		Collection: inviteCollection,
		EndOfLife:  rest.Delete,
	}}
}

func (i *Invite) Annotate(a infer.Annotator) {
	a.Describe(i, "An invitation to join the organization. Invitations cannot be changed, so "+
		"any change replaces the invitation, and they expire 21 days after they are sent. "+
		"Once accepted, the person is a user of the organization; manage their role with "+
		"`anthropic.organization.User`.")
}

type InviteArgs struct {
	Email        string           `pulumi:"email"                 provider:"replaceOnChanges"`
	Role         OrganizationRole `pulumi:"role"                  provider:"replaceOnChanges"`
	RbacGroupIDs []string         `pulumi:"rbacGroupIds,optional" provider:"replaceOnChanges"`
}

func (i *InviteArgs) Annotate(a infer.Annotator) {
	a.Describe(&i.Email, "The email address to invite.")
	a.Describe(&i.Role, "The organization role the person joins with.")
	a.Describe(&i.RbacGroupIDs, "The RBAC groups the person joins. Claude Enterprise only.")
}

// OrganizationRole is a user's role in the organization. The admin role cannot be
// granted or revoked through the API.
type OrganizationRole string

func (OrganizationRole) Values() []infer.EnumValue[OrganizationRole] {
	return []infer.EnumValue[OrganizationRole]{
		{Value: "user", Description: "Can use Claude."},
		{Value: "developer", Description: "Can also use the API and manage API keys."},
		{Value: "billing", Description: "Can also manage billing."},
		{Value: "claude_code_user", Description: "Can use Claude Code."},
		{Value: "managed", Description: "A managed user of a Claude Enterprise organization."},
	}
}

type InviteState struct {
	InviteArgs
	Status    string `pulumi:"status"`
	InvitedAt string `pulumi:"invitedAt"`
	ExpiresAt string `pulumi:"expiresAt"`
}

func (s *InviteState) Inputs() *InviteArgs { return &s.InviteArgs }

func (s *InviteState) Annotate(a infer.Annotator) {
	a.Describe(&s.Status, "The invitation's status: `pending`, `accepted`, `expired`, or `deleted`.")
	a.Describe(&s.InvitedAt, "When the invitation was sent, in RFC 3339 format.")
	a.Describe(&s.ExpiresAt, "When the invitation expires, in RFC 3339 format.")
}
