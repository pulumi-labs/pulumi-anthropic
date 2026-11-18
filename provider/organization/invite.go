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
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Invite invites a person to join the organization.
type Invite struct{}

// NewInvite returns the complete Invite resource.
func NewInvite() infer.InferredResource {
	return inviteResource{infer.Resource(&Invite{})}
}

func (i *Invite) Annotate(a infer.Annotator) {
	a.Describe(i, "An invitation to join the organization. Invitations cannot be changed, so "+
		"any change deletes the old pending invitation before sending a new one. Expiry is reported by expiresAt. "+
		"Deleting this resource revokes pending invitations; accepted, expired and deleted invitations are only removed from Pulumi state. "+
		"Deleting an invitation never removes an organization user. "+
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

func (s *InviteState) Annotate(a infer.Annotator) {
	a.Describe(&s.Status, "The invitation's status: `pending`, `accepted`, `expired`, or `deleted`.")
	a.Describe(&s.InvitedAt, "When the invitation was sent, in RFC 3339 format.")
	a.Describe(&s.ExpiresAt, "When the invitation expires, in RFC 3339 format.")
}

func (*Invite) Create(ctx context.Context, req infer.CreateRequest[InviteArgs]) (infer.CreateResponse[InviteState], error) {
	out := InviteState{InviteArgs: req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[InviteState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[InviteState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.Invites.New(ctx, sdk.OrganizationInviteNewParams{Email: req.Inputs.Email, Role: sdk.OrganizationInviteNewParamsRole(req.Inputs.Role), RBACGroupIDs: req.Inputs.RbacGroupIDs}, option.WithMaxRetries(0), option.WithResponseBodyInto(&raw))
	if err != nil {
		return infer.CreateResponse[InviteState]{}, err
	}
	observed, id, err := inviteObservation(raw, "")
	if err != nil {
		return infer.CreateResponse[InviteState]{}, err
	}
	if req.Inputs.RbacGroupIDs == nil && len(observed.RbacGroupIDs) == 0 {
		observed.RbacGroupIDs = nil
	}
	return infer.CreateResponse[InviteState]{ID: id, Output: observed}, nil
}

func (*Invite) Read(ctx context.Context, req infer.ReadRequest[InviteArgs, InviteState]) (infer.ReadResponse[InviteArgs, InviteState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[InviteArgs, InviteState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.Invites.Get(ctx, req.ID, option.WithResponseBodyInto(&raw))
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[InviteArgs, InviteState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[InviteArgs, InviteState]{}, err
	}
	out, _, err := inviteObservation(raw, req.ID)
	if err != nil {
		return infer.ReadResponse[InviteArgs, InviteState]{}, err
	}
	if req.Inputs.RbacGroupIDs == nil && len(out.RbacGroupIDs) == 0 {
		out.RbacGroupIDs = nil
	}
	return infer.ReadResponse[InviteArgs, InviteState]{ID: req.ID, Inputs: out.InviteArgs, State: out}, nil
}

func (*Invite) Update(ctx context.Context, req infer.UpdateRequest[InviteArgs, InviteState]) (infer.UpdateResponse[InviteState], error) {
	if req.DryRun {
		return infer.UpdateResponse[InviteState]{Output: InviteState{InviteArgs: req.Inputs}}, nil
	}
	// Immutable invitation: never send another email or perform an internal refresh.
	return infer.UpdateResponse[InviteState]{Output: req.State}, nil
}

func (*Invite) Delete(ctx context.Context, req infer.DeleteRequest[InviteState]) (infer.DeleteResponse, error) {
	// Only pending invitations can be revoked. Refresh owns observation of acceptance.
	switch req.State.Status {
	case "accepted", "expired", "deleted":
		return infer.DeleteResponse{}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Invites.Delete(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve validated Check inputs and protect secret groups across Read reordering.
type inviteResource struct{ infer.InferredResource }

func (r inviteResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&Invite{}).Check(ctx, req)
	if err != nil {
		return out, err
	}
	if v := req.Inputs.Get("email"); v.IsString() {
		addr, e := mail.ParseAddress(v.AsString())
		if e != nil || addr.Address != v.AsString() {
			out.Failures = append(out.Failures, p.CheckFailure{Property: "email", Reason: "must be an email address without a display name"})
		}
	}
	if v := req.Inputs.Get("role"); v.IsString() {
		switch v.AsString() {
		case "user", "developer", "billing", "claude_code_user", "managed":
		default:
			out.Failures = append(out.Failures, p.CheckFailure{Property: "role", Reason: "must be a supported invitation role"})
		}
	}
	if v := req.Inputs.Get("rbacGroupIds"); v.IsArray() && v.AsArray().Len() > 100 {
		out.Failures = append(out.Failures, p.CheckFailure{Property: "rbacGroupIds", Reason: "at most 100 RBAC groups may be specified"})
	}
	if len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, nil
}

func (r inviteResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	// Empty and omitted groups both mean no assignments. Do not reorder groups.
	if v := req.Inputs.Get("rbacGroupIds"); v.IsArray() && v.AsArray().Len() == 0 {
		req.Inputs = req.Inputs.Delete("rbacGroupIds")
	}
	if v := req.State.Get("rbacGroupIds"); v.IsArray() && v.AsArray().Len() == 0 {
		req.State = req.State.Delete("rbacGroupIds")
	}
	out, err := infer.Resource(&Invite{}).Diff(ctx, req)
	if out.HasChanges {
		// infer does not propagate a collection's replacement annotation to element paths.
		for path, change := range out.DetailedDiff {
			switch change.Kind {
			case p.Add:
				change.Kind = p.AddReplace
			case p.Update:
				change.Kind = p.UpdateReplace
			case p.Delete:
				change.Kind = p.DeleteReplace
			}
			if strings.HasPrefix(path, "rbacGroupIds[") {
				delete(out.DetailedDiff, path)
				out.DetailedDiff["rbacGroupIds"] = p.PropertyDiff{Kind: p.UpdateReplace}
			} else {
				out.DetailedDiff[path] = change
			}
		}
		out.DeleteBeforeReplace = true
	}
	return out, err
}

func (r inviteResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	out, err := infer.Resource(&Invite{}).Create(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = invitePreviewGroups(out.Properties, req.Properties.Get("rbacGroupIds"))
	}
	return out, err
}

func (r inviteResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	out, err := infer.Resource(&Invite{}).Read(ctx, req)
	if err == nil && out.ID != "" {
		// infer restores secret paths by index, but the service can reorder groups.
		// Protect the entire collection rather than copying raw values or dependencies.
		if req.Properties.Get("rbacGroupIds").HasSecrets() || req.Inputs.Get("rbacGroupIds").HasSecrets() {
			out.Inputs = out.Inputs.Set("rbacGroupIds", out.Inputs.Get("rbacGroupIds").WithSecret(true))
			out.Properties = out.Properties.Set("rbacGroupIds", out.Properties.Get("rbacGroupIds").WithSecret(true))
		}
	}
	return out, err
}

func (r inviteResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	out, err := infer.Resource(&Invite{}).Update(ctx, req)
	if err == nil && req.DryRun {
		out.Properties = invitePreviewGroups(out.Properties, req.Inputs.Get("rbacGroupIds"))
	}
	return out, err
}

func (r inviteResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Invite{}).Delete(ctx, req)
}

// An unresolved optional list can disappear during typed encoding before infer
// applies preview wiring. Preserve its unknownness/secrecy, not its dependencies.
func invitePreviewGroups(outputs property.Map, groups property.Value) property.Map {
	if groups.IsComputed() {
		outputs = outputs.Set("rbacGroupIds", property.New(property.Computed).WithSecret(groups.Secret()))
	}
	return outputs
}

// Validate the raw response without the SDK's primitive-to-string coercions.
func inviteObservation(raw json.RawMessage, expected string) (InviteState, string, error) {
	var envelope struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &envelope)
	id := envelope.ID
	if id == "" || (expected != "" && id != expected) {
		return InviteState{}, "", errors.New("invalid invitation response identity")
	}
	var r sdk.OrganizationInvite
	decodeErr := json.Unmarshal(raw, &r)
	out := InviteState{InviteArgs: InviteArgs{Email: r.Email, Role: OrganizationRole(r.Role), RbacGroupIDs: r.RBACGroupIDs}}
	// The SDK intentionally coerces primitive JSON values into strings. Presence
	// metadata alone cannot establish a valid string or array-of-strings response.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	stringJSON := func(raw json.RawMessage) bool {
		var value string
		return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &value) == nil
	}
	validEmail := stringJSON(fields["email"]) && r.Email != ""
	if !validEmail {
		out.Email = ""
	}
	var groups []json.RawMessage
	validGroups := len(fields["rbac_group_ids"]) > 0 && fields["rbac_group_ids"][0] == '[' && json.Unmarshal(fields["rbac_group_ids"], &groups) == nil
	for _, group := range groups {
		if !stringJSON(group) {
			validGroups = false
		}
	}
	if !validGroups {
		out.RbacGroupIDs = nil
	}
	if !r.InvitedAt.IsZero() {
		out.InvitedAt = r.InvitedAt.Format(time.RFC3339Nano)
	}
	if !r.ExpiresAt.IsZero() {
		out.ExpiresAt = r.ExpiresAt.Format(time.RFC3339Nano)
	}
	switch r.Status {
	case "pending", "accepted", "expired", "deleted":
		out.Status = string(r.Status)
	}
	if decodeErr != nil {
		return out, id, decodeErr
	}
	validRole := false
	switch r.Role {
	case "admin", "billing", "claude_code_user", "developer", "managed", "membership_admin", "owner", "primary_owner", "user":
		validRole = true
	}
	if r.JSON.Type.Raw() != `"invite"` || !validEmail || !validRole || !validGroups || out.InvitedAt == "" || out.ExpiresAt == "" || out.Status == "" {
		return out, id, fmt.Errorf("incomplete or invalid invitation response")
	}
	return out, id, nil
}
