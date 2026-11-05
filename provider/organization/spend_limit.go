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
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// SpendLimit caps spending for a scope over a period.
type SpendLimit struct {
	rest.Resource[SpendLimitArgs, SpendLimitState, *SpendLimitState]
}

// NewSpendLimit returns the SpendLimit resource controller.
func NewSpendLimit() *SpendLimit {
	return &SpendLimit{rest.Resource[SpendLimitArgs, SpendLimitState, *SpendLimitState]{
		Collection: spendLimitCollection,
		EndOfLife:  rest.Delete,
		// A limit is keyed by its scope and period, and setting one overwrites any limit
		// with the same key, so a replacement created first would be the same limit the
		// deletion of the original then removes.
		DeleteBeforeReplace: true,
	}}
}

func (s *SpendLimit) Annotate(a infer.Annotator) {
	a.Describe(s, "A spend limit: the most a user, a workspace, or the whole organization may "+
		"spend in a period.\n\n"+
		"There is at most one limit per scope and period, and the API sets a limit by "+
		"overwriting whatever limit that scope and period already has, so creating this "+
		"resource takes over an existing limit rather than failing. Changing the scope or "+
		"period replaces the limit.\n\n"+
		"User limits apply to Claude Enterprise organizations. Organization and workspace "+
		"limits for Console organizations are in early-access preview, and must be monthly "+
		"with an amount.")
}

type SpendLimitArgs struct {
	Scope  SpendLimitScope   `pulumi:"scope"           provider:"replaceOnChanges"`
	Period *SpendLimitPeriod `pulumi:"period,optional" provider:"replaceOnChanges"`
	Amount *string           `pulumi:"amount,optional"`
}

func (s *SpendLimitArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Scope, "What the limit applies to.")
	a.Describe(&s.Period, "The period the limit applies over. If omitted, the API's default applies.")
	a.Describe(&s.Amount, "The limit, as a non-negative integer string in the minor unit of the "+
		"organization's billing currency: `\"50000\"` is $500.00. Omit for an explicit "+
		"\"no limit\" override of this scope and period.")
}

// SpendLimitScope is what a spend limit applies to. Which ID applies depends on the type.
type SpendLimitScope struct {
	Type        SpendLimitScopeType `pulumi:"type"`
	UserID      *string             `pulumi:"userId,optional"`
	WorkspaceID *string             `pulumi:"workspaceId,optional"`
}

func (s *SpendLimitScope) Annotate(a infer.Annotator) {
	a.Describe(&s.Type, "The kind of scope.")
	a.Describe(&s.UserID, "The user the limit applies to. Required for `user`.")
	a.Describe(&s.WorkspaceID, "The workspace the limit applies to. Required for `workspace`.")
}

// SpendLimitScopeType discriminates the kinds of spend limit scope.
type SpendLimitScopeType string

func (SpendLimitScopeType) Values() []infer.EnumValue[SpendLimitScopeType] {
	return []infer.EnumValue[SpendLimitScopeType]{
		{Value: "user", Description: "One user. Claude Enterprise only."},
		{Value: "workspace", Description: "One workspace."},
		{Value: "organization", Description: "The whole organization."},
	}
}

// SpendLimitPeriod is the period a spend limit applies over.
type SpendLimitPeriod string

func (SpendLimitPeriod) Values() []infer.EnumValue[SpendLimitPeriod] {
	return []infer.EnumValue[SpendLimitPeriod]{{Value: "daily"}, {Value: "weekly"}, {Value: "monthly"}}
}

type SpendLimitState struct {
	SpendLimitArgs
	Currency  string `pulumi:"currency"`
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *SpendLimitState) Inputs() *SpendLimitArgs { return &s.SpendLimitArgs }

func (s *SpendLimitState) Annotate(a infer.Annotator) {
	a.Describe(&s.Currency, "The ISO 4217 code of the organization's billing currency, "+
		"which `amount` is denominated in.")
	a.Describe(&s.CreatedAt, "When the limit was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the limit was last updated, in RFC 3339 format.")
}

var _ infer.CustomUpdate[SpendLimitArgs, SpendLimitState] = (*SpendLimit)(nil)

// Create sets the limit for the scope and period.
func (s *SpendLimit) Create(
	ctx context.Context, req infer.CreateRequest[SpendLimitArgs],
) (infer.CreateResponse[SpendLimitState], error) {
	if req.DryRun {
		return infer.CreateResponse[SpendLimitState]{Output: SpendLimitState{SpendLimitArgs: req.Inputs}}, nil
	}
	out, id, err := s.set(ctx, req.Inputs)
	return infer.CreateResponse[SpendLimitState]{ID: id, Output: out}, err
}

// Update sets the new amount. There is no endpoint to update a limit by ID; setting the
// limit for the same scope and period overwrites it in place.
func (s *SpendLimit) Update(
	ctx context.Context, req infer.UpdateRequest[SpendLimitArgs, SpendLimitState],
) (infer.UpdateResponse[SpendLimitState], error) {
	if req.DryRun {
		out := req.State
		out.SpendLimitArgs = req.Inputs
		return infer.UpdateResponse[SpendLimitState]{Output: out}, nil
	}
	out, _, err := s.set(ctx, req.Inputs)
	return infer.UpdateResponse[SpendLimitState]{Output: out}, err
}

// set upserts the limit for the inputs' scope and period. The amount is always sent,
// because null is itself a setting: an explicit "no limit" override.
func (s *SpendLimit) set(ctx context.Context, in SpendLimitArgs) (SpendLimitState, string, error) {
	tree, err := rest.Encode(in)
	if err != nil {
		return SpendLimitState{}, "", err
	}
	body := tree.(map[string]any)
	body["amount"] = in.Amount
	var raw []byte
	if err := s.Collection.Do(ctx, http.MethodPost, s.Collection.Path, body, &raw); err != nil {
		return SpendLimitState{}, "", err
	}
	return rest.DecodeState[SpendLimitArgs, SpendLimitState](raw, in)
}
