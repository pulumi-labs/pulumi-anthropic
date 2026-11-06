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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// User manages the organization role of an existing user.
type User struct {
	rest.Adopted[UserArgs, UserState, *UserState]
}

// NewUser returns the User resource controller.
func NewUser() *User {
	return &User{rest.Adopted[UserArgs, UserState, *UserState]{
		Resource: rest.Resource[UserArgs, UserState, *UserState]{
			Collection: userCollection,
			EndOfLife:  rest.Delete,
		},
		ID:     func(a *UserArgs) *string { return &a.UserID },
		Noun:   "user",
		Origin: "users join the organization by accepting an anthropic.organization.Invite",
	}}
}

func (u *User) Annotate(a infer.Annotator) {
	a.Describe(u, "A user of the organization, and their organization role.\n\n"+
		"Users cannot be created through the API: people join by accepting an "+
		"`anthropic.organization.Invite`. Creating this resource adopts an existing user by ID "+
		"and sets their role.\n\n"+
		"Deleting this resource removes the user from the organization. To stop managing a "+
		"user without removing them, set the `retainOnDelete` resource option.")
}

type UserArgs struct {
	UserID string           `pulumi:"userId" provider:"replaceOnChanges" wire:"-"`
	Role   OrganizationRole `pulumi:"role"`
}

func (u *UserArgs) Annotate(a infer.Annotator) {
	a.Describe(&u.UserID, "The ID of the existing user to manage.")
	a.Describe(&u.Role, "The user's organization role. The `admin` role cannot be granted "+
		"through the API.")
}

type UserState struct {
	UserArgs
	Email   string `pulumi:"email"`
	Name    string `pulumi:"name"`
	AddedAt string `pulumi:"addedAt"`
}

func (s *UserState) Inputs() *UserArgs { return &s.UserArgs }

func (s *UserState) Annotate(a infer.Annotator) {
	a.Describe(&s.Email, "The user's email address.")
	a.Describe(&s.Name, "The user's name.")
	a.Describe(&s.AddedAt, "When the user joined the organization, in RFC 3339 format.")
}
