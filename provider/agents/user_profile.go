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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// UserProfile is an entity a platform serves through the API, for request attribution.
type UserProfile struct {
	rest.Resource[UserProfileArgs, UserProfileState, *UserProfileState]
}

// NewUserProfile returns the UserProfile resource controller.
func NewUserProfile() *UserProfile {
	return &UserProfile{rest.Resource[UserProfileArgs, UserProfileState, *UserProfileState]{
		Collection: userProfileCollection,
		EndOfLife:  rest.Forget,
		Tombstones: map[string]any{"metadata": ""},
	}}
}

func (u *UserProfile) Annotate(a infer.Annotator) {
	a.Describe(u, "A user profile: an entity your platform serves through the Anthropic API, "+
		"such as an end user of your product or a company you resell Claude access to. Send "+
		"its ID in the `anthropic-user-profile-id` header of Messages, Message Batches, and "+
		"token counting requests to attribute them to the entity.\n\n"+
		"The API cannot delete user profiles, so deleting this resource only removes it from "+
		"the stack; the profile remains.")
}

type UserProfileArgs struct {
	Name                    *string           `pulumi:"name,optional"`
	AccessType              *AccessType       `pulumi:"accessType,optional"`
	ExternalID              *string           `pulumi:"externalId,optional"`
	ExternalUserOnboardedAt *string           `pulumi:"externalUserOnboardedAt,optional"`
	Metadata                map[string]string `pulumi:"metadata,optional"`
}

func (u *UserProfileArgs) Annotate(a infer.Annotator) {
	a.Describe(&u.Name, "The entity's name, 1 to 255 characters.")
	a.Describe(&u.AccessType, "How your platform uses the API on the entity's behalf. Defaults "+
		"to `application`.")
	a.Describe(&u.ExternalID, "Your platform's own identifier for the entity, up to 255 characters.")
	a.Describe(&u.ExternalUserOnboardedAt, "When the entity joined your platform, in RFC 3339 "+
		"format. Once set, it cannot be cleared.")
	a.Describe(&u.Metadata, "Arbitrary key-value pairs, up to 16.")
}

type UserProfileState struct {
	UserProfileArgs
	TrustGrants map[string]TrustGrant `pulumi:"trustGrants,optional"`
	CreatedAt   string                `pulumi:"createdAt"`
	UpdatedAt   string                `pulumi:"updatedAt"`
}

func (s *UserProfileState) Inputs() *UserProfileArgs { return &s.UserProfileArgs }

func (s *UserProfileState) Annotate(a infer.Annotator) {
	a.Describe(&s.TrustGrants, "The profile's active or pending trust grants, by grant name.")
	a.Describe(&s.CreatedAt, "When the profile was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the profile was last updated, in RFC 3339 format.")
}

// AccessType is how a platform uses the API on a profile's behalf.
type AccessType string

func (AccessType) Values() []infer.EnumValue[AccessType] {
	return []infer.EnumValue[AccessType]{
		{Value: "application", Description: "Your product uses the API behind the scenes, and " +
			"the profile is one of its end users."},
		{Value: "passthrough", Description: "You resell raw inference, and the profile is the " +
			"company you resell to."},
	}
}

// TrustGrant is the status of a profile's trust grant.
type TrustGrant struct {
	Status string `pulumi:"status"`
}

func (g *TrustGrant) Annotate(a infer.Annotator) {
	a.Describe(&g.Status, "The grant's status: `active`, `pending`, or `rejected`.")
}
