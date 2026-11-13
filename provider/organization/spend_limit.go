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
	"regexp"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// SpendLimit caps spending for a scope over a period.
type SpendLimit struct{}

// NewSpendLimit returns the complete SpendLimit resource.
func NewSpendLimit() infer.InferredResource {
	return spendLimitResource{infer.Resource(&SpendLimit{})}
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
		"with an amount. Deleting a user limit restores any inherited cap for that period; "+
		"deleting a limit may increase permitted spending. Import accepts a writable limit's spl_ ID.")
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
		"organization's billing currency: `\"50000\"` is $500.00. For user limits, omitting or removing "+
		"this value sets an explicit no-limit override for this period, not for other periods. "+
		"Organization and workspace limits require an amount.")
}

// SpendLimitScope is what a spend limit applies to. Which ID applies depends on the type.
type SpendLimitScope struct {
	Type        SpendLimitScopeType `pulumi:"type"`
	UserID      *string             `pulumi:"userId,optional"`
	WorkspaceID *string             `pulumi:"workspaceId,optional"`
}

func (s *SpendLimitScope) Annotate(a infer.Annotator) {
	a.Describe(s, "The user, workspace, or organization whose spending is capped. Specify only the ID matching the scope type.")
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
	IsEnabled bool   `pulumi:"isEnabled"`
}

func (s *SpendLimitState) Annotate(a infer.Annotator) {
	a.Describe(s, "A configured spend limit and its current enabled status.")
	a.Describe(&s.IsEnabled, "Whether the configured limit is enabled. Organization limits are disabled while extra usage is off; the amount is retained for when extra usage is enabled again. Other periods and inherited limits may still apply.")
	a.Describe(&s.Currency, "The ISO 4217 code of the organization's billing currency, "+
		"which `amount` is denominated in.")
	a.Describe(&s.CreatedAt, "When the limit was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the limit was last updated, in RFC 3339 format.")
}

var _ infer.CustomUpdate[SpendLimitArgs, SpendLimitState] = (*SpendLimit)(nil)

var spendLimitMoney = regexp.MustCompile(`^[0-9]+$`)

// Create sets the limit for the scope and period.
func (s *SpendLimit) Create(
	ctx context.Context, req infer.CreateRequest[SpendLimitArgs],
) (infer.CreateResponse[SpendLimitState], error) {
	if req.DryRun {
		return infer.CreateResponse[SpendLimitState]{Output: SpendLimitState{SpendLimitArgs: req.Inputs}}, nil
	}
	out, id, err := s.set(ctx, req.Inputs)
	if err != nil {
		return infer.CreateResponse[SpendLimitState]{}, err
	}
	return infer.CreateResponse[SpendLimitState]{ID: id, Output: out}, nil
}

func (s *SpendLimit) Read(ctx context.Context, req infer.ReadRequest[SpendLimitArgs, SpendLimitState]) (infer.ReadResponse[SpendLimitArgs, SpendLimitState], error) {
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{}, err
	}
	v, err := client.Beta.Organization.SpendLimits.Get(ctx, req.ID)
	if err != nil {
		if apiutil.IsNotFound(err) {
			return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{}, nil
		}
		return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{}, err
	}
	out, err := spendLimitState(v)
	if err != nil {
		return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{}, err
	}
	if v.ID != req.ID {
		return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{}, fmt.Errorf("spend limit response identity differs from requested ID")
	}
	return infer.ReadResponse[SpendLimitArgs, SpendLimitState]{ID: req.ID, Inputs: out.SpendLimitArgs, State: out}, nil
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
	out, id, err := s.set(ctx, req.Inputs)
	if id != "" && id != req.ID {
		// Set is keyed by scope and period, not ID. Never attach another object's
		// observations to the engine's identity after a concurrent recreation.
		return infer.UpdateResponse[SpendLimitState]{}, fmt.Errorf("spend limit identity changed from %s to %s; refresh before retrying", req.ID, id)
	}
	if err != nil {
		return infer.UpdateResponse[SpendLimitState]{}, err
	}
	return infer.UpdateResponse[SpendLimitState]{Output: out}, nil
}

func (s *SpendLimit) Delete(ctx context.Context, req infer.DeleteRequest[SpendLimitState]) (infer.DeleteResponse, error) {
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = client.Beta.Organization.SpendLimits.Delete(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve Check inputs, structural replacement semantics and unknown preview observations.
type spendLimitResource struct{ infer.InferredResource }

func (r spendLimitResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&SpendLimit{}).Check(ctx, req)
	if err != nil {
		return out, err
	}
	fail := func(path, reason string) {
		out.Failures = append(out.Failures, p.CheckFailure{Property: path, Reason: reason})
	}
	if a := req.Inputs.Get("amount"); a.IsString() && !spendLimitMoney.MatchString(a.AsString()) {
		fail("amount", "must be a non-negative integer decimal string")
	}
	period := req.Inputs.Get("period")
	if period.IsString() && period.AsString() != "daily" && period.AsString() != "weekly" && period.AsString() != "monthly" {
		fail("period", "must be daily, weekly, or monthly")
	}
	if scope := req.Inputs.Get("scope"); scope.IsMap() {
		m := scope.AsMap()
		typ := m.Get("type")
		if typ.IsString() {
			t := typ.AsString()
			if t != "user" && t != "workspace" && t != "organization" {
				fail("scope.type", "must be user, workspace, or organization")
			}
			for _, pair := range []struct{ key, kind string }{{"userId", "user"}, {"workspaceId", "workspace"}} {
				id := m.Get(pair.key)
				if t == pair.kind && (id.IsNull() || (id.IsString() && id.AsString() == "")) {
					fail("scope."+pair.key, "a non-empty ID is required for this scope")
				}
				if t != pair.kind && !id.IsNull() && !id.IsComputed() {
					fail("scope."+pair.key, "must be omitted for this scope")
				}
			}
			if t == "workspace" || t == "organization" {
				if req.Inputs.Get("amount").IsNull() {
					fail("amount", "is required for organization and workspace limits")
				}
				if period.IsString() && period.AsString() != "monthly" {
					fail("period", "organization and workspace limits must be monthly")
				}
			}
		}
	}
	if len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, nil
}

func (r spendLimitResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	if req.OldInputs.Get("period").IsNull() && req.Inputs.Get("period").IsNull() {
		req.State = req.State.Delete("period")
	}
	out, err := infer.Resource(&SpendLimit{}).Diff(ctx, req)
	for path, diff := range out.DetailedDiff {
		if path == "scope" || strings.HasPrefix(path, "scope.") || path == "period" {
			switch diff.Kind {
			case p.Add:
				diff.Kind = p.AddReplace
			case p.Delete:
				diff.Kind = p.DeleteReplace
			case p.Update:
				diff.Kind = p.UpdateReplace
			}
			out.DetailedDiff[path] = diff
			out.DeleteBeforeReplace = true // Set takes over an existing structural key.
		}
	}
	return out, err
}

func (r spendLimitResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	out, err := infer.Resource(&SpendLimit{}).Create(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = spendLimitPreview(out.Properties)
	}
	return out, err
}

func (r spendLimitResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	out, err := infer.Resource(&SpendLimit{}).Read(ctx, req)
	if err == nil && out.ID != "" {
		if req.Inputs.Len() != 0 && req.Inputs.Get("period").IsNull() {
			out.Inputs = out.Inputs.Delete("period")
		}
	}
	return out, err
}

func (r spendLimitResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	out, err := infer.Resource(&SpendLimit{}).Update(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = spendLimitPreview(out.Properties)
	}
	return out, err
}

func (r spendLimitResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&SpendLimit{}).Delete(ctx, req)
}

func spendLimitPreview(m property.Map) property.Map {
	// A set operation can change these observations, even when prior state is known.
	for _, k := range []string{"currency", "isEnabled", "createdAt", "updatedAt"} {
		m = m.Set(k, property.New(property.Computed))
	}
	return m
}

// set upserts the limit for the inputs' scope and period. The amount is always sent,
// because null is itself a setting: an explicit "no limit" override.
func (s *SpendLimit) set(ctx context.Context, in SpendLimitArgs) (SpendLimitState, string, error) {
	p := sdk.BetaOrganizationSpendLimitSetParams{Amount: param.Null[string]()}
	if in.Amount != nil {
		p.Amount = param.NewOpt(*in.Amount)
	}
	if in.Period != nil {
		p.Period = sdk.BetaSpendLimitPeriod(*in.Period)
	}
	switch in.Scope.Type {
	case "user":
		if in.Scope.UserID == nil || in.Scope.WorkspaceID != nil {
			return SpendLimitState{}, "", fmt.Errorf("user scope requires only userId")
		}
		p.Scope.OfUser = &sdk.BetaSpendLimitUserScopeParam{UserID: *in.Scope.UserID}
	case "workspace":
		if in.Scope.WorkspaceID == nil || in.Scope.UserID != nil {
			return SpendLimitState{}, "", fmt.Errorf("workspace scope requires only workspaceId")
		}
		p.Scope.OfWorkspace = &sdk.BetaSpendLimitWorkspaceScopeParam{WorkspaceID: *in.Scope.WorkspaceID}
	case "organization":
		if in.Scope.UserID != nil || in.Scope.WorkspaceID != nil {
			return SpendLimitState{}, "", fmt.Errorf("organization scope accepts no ID")
		}
		v := sdk.NewBetaSpendLimitOrganizationScopeParam()
		p.Scope.OfOrganization = &v
	default:
		return SpendLimitState{}, "", fmt.Errorf("unsupported spend limit scope %q", in.Scope.Type)
	}
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return SpendLimitState{}, "", err
	}
	v, err := client.Beta.Organization.SpendLimits.Set(ctx, p)
	if err != nil {
		return SpendLimitState{}, "", err
	}
	out, err := spendLimitState(v)
	if err != nil {
		return SpendLimitState{}, "", err
	}
	id := ""
	if apiutil.StringField(v.JSON.ID) {
		id = v.ID
	}
	return out, id, err
}

// Validate all server facts before publishing state.
func spendLimitState(v *sdk.BetaSpendLimit) (SpendLimitState, error) {
	out := SpendLimitState{}
	if v == nil {
		return out, fmt.Errorf("missing spend limit response")
	}
	if !apiutil.StringField(v.JSON.ID) || v.ID == "" {
		return out, fmt.Errorf("invalid spend limit identity")
	}
	if v.JSON.Type.Raw() != "" && (!apiutil.StringField(v.JSON.Type) || v.Type != "spend_limit") {
		return out, fmt.Errorf("invalid spend limit type")
	}
	if !v.JSON.Scope.Valid() || !apiutil.StringField(v.Scope.JSON.Type) {
		return out, fmt.Errorf("invalid spend limit scope")
	}
	out.Scope.Type = SpendLimitScopeType(v.Scope.Type)
	switch v.Scope.Type {
	case "user":
		if !apiutil.StringField(v.Scope.JSON.UserID) || v.Scope.UserID == "" {
			return SpendLimitState{}, fmt.Errorf("invalid spend limit user scope")
		}
		out.Scope.UserID = &v.Scope.UserID
	case "workspace":
		if !apiutil.StringField(v.Scope.JSON.WorkspaceID) || v.Scope.WorkspaceID == "" {
			return SpendLimitState{}, fmt.Errorf("invalid spend limit workspace scope")
		}
		out.Scope.WorkspaceID = &v.Scope.WorkspaceID
	case "organization":
	default:
		return SpendLimitState{}, fmt.Errorf("unsupported spend limit scope")
	}
	if apiutil.StringField(v.JSON.Amount) && spendLimitMoney.MatchString(v.Amount) {
		out.Amount = &v.Amount
	} else if v.JSON.Amount.Raw() != "null" {
		return SpendLimitState{}, fmt.Errorf("invalid spend limit amount")
	}
	if !apiutil.StringField(v.JSON.Period) || (v.Period != "daily" && v.Period != "weekly" && v.Period != "monthly") {
		return SpendLimitState{}, fmt.Errorf("invalid spend limit period")
	}
	period := SpendLimitPeriod(v.Period)
	out.Period = &period
	if !apiutil.StringField(v.JSON.Currency) || v.Currency == "" {
		return SpendLimitState{}, fmt.Errorf("invalid spend limit currency")
	}
	out.Currency = v.Currency
	if raw := strings.TrimSpace(v.JSON.IsEnabled.Raw()); !v.JSON.IsEnabled.Valid() || (raw != "true" && raw != "false") {
		return SpendLimitState{}, fmt.Errorf("invalid spend limit enabled status")
	}
	out.IsEnabled = v.IsEnabled
	if !apiutil.StringField(v.JSON.CreatedAt) || v.CreatedAt.IsZero() || !apiutil.StringField(v.JSON.UpdatedAt) || v.UpdatedAt.IsZero() {
		return SpendLimitState{}, fmt.Errorf("invalid spend limit timestamps")
	}
	out.CreatedAt = v.CreatedAt.Format(time.RFC3339Nano)
	out.UpdatedAt = v.UpdatedAt.Format(time.RFC3339Nano)
	return out, nil
}
