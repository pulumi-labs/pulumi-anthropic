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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// RbacGroupMember adds a user to an RBAC group.
type RbacGroupMember struct{}

// NewRbacGroupMember returns the complete RbacGroupMember resource.
func NewRbacGroupMember() infer.InferredResource {
	return memberResource{infer.Resource(&RbacGroupMember{})}
}

func (m *RbacGroupMember) Annotate(a infer.Annotator) {
	a.Describe(m, "Adds a user to an RBAC group. The resource's ID is `<rbac-group-id>/<user-id>`.\n\n"+
		"RBAC groups are available to Claude Enterprise organizations. Deleting this resource removes only the membership, not the user or group. Membership of SCIM-provisioned groups cannot be changed while SCIM provisioning is active in the tenant. Owner-access protections can prevent adding or removing members.")
}

type RbacGroupMemberArgs struct {
	// The group ID is part of the endpoint's path rather than the request body.
	RbacGroupID string `pulumi:"rbacGroupId" provider:"replaceOnChanges"`
	UserID      string `pulumi:"userId"      provider:"replaceOnChanges"`
}

func (m *RbacGroupMemberArgs) Annotate(a infer.Annotator) {
	a.Describe(&m.RbacGroupID, "The group's ID. Changing this value replaces the membership.")
	a.Describe(&m.UserID, "The user's ID. Changing this value replaces the membership.")
}

type RbacGroupMemberState struct {
	RbacGroupMemberArgs
	Email     string `pulumi:"email"`
	CreatedAt string `pulumi:"createdAt"`
}

func (m *RbacGroupMemberState) Annotate(a infer.Annotator) {
	a.Describe(&m.Email, "The user's email address.")
	a.Describe(&m.CreatedAt, "When the user was added to the group, in RFC 3339 format.")
}

func (controller *RbacGroupMember) Create(ctx context.Context, req infer.CreateRequest[RbacGroupMemberArgs]) (infer.CreateResponse[RbacGroupMemberState], error) {
	out := RbacGroupMemberState{RbacGroupMemberArgs: req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[RbacGroupMemberState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[RbacGroupMemberState]{}, err
	}
	r, err := c.Organization.RBACGroups.Members.Add(ctx, req.Inputs.RbacGroupID, sdk.OrganizationRBACGroupMemberAddParams{UserID: req.Inputs.UserID}, option.WithMaxRetries(0))
	if err != nil {
		return infer.CreateResponse[RbacGroupMemberState]{}, err
	}
	id := req.Inputs.RbacGroupID + "/" + req.Inputs.UserID
	out, err = memberObserved(r, req.Inputs)
	if err != nil {
		return infer.CreateResponse[RbacGroupMemberState]{}, err
	}
	return infer.CreateResponse[RbacGroupMemberState]{ID: id, Output: out}, err
}

func (controller *RbacGroupMember) Read(ctx context.Context, req infer.ReadRequest[RbacGroupMemberArgs, RbacGroupMemberState]) (infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState], error) {
	pair, err := memberPair(req.ID)
	if err != nil {
		return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, err
	}
	// Traverse to completion: errors on later pages must not erase membership.
	page, err := c.Organization.RBACGroups.Members.List(ctx, pair.RbacGroupID, sdk.OrganizationRBACGroupMemberListParams{})
	var found *sdk.RBACGroupMember
	for err == nil && page != nil {
		var data []json.RawMessage
		raw := page.JSON.Data.Raw()
		if len(raw) == 0 || raw[0] != '[' || json.Unmarshal([]byte(raw), &data) != nil || (page.JSON.NextPage.Raw() != "null" && !apiutil.JSONString(page.JSON.NextPage.Raw())) {
			return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, errors.New("invalid membership list page")
		}
		for _, m := range page.Data {
			if !apiutil.JSONString(m.JSON.UserID.Raw()) || m.UserID == "" || !apiutil.JSONString(m.JSON.RBACGroupID.Raw()) || m.RBACGroupID != pair.RbacGroupID || m.JSON.Type.Raw() != `"rbac_group_member"` {
				return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, errors.New("invalid membership list identity")
			}
			if m.UserID == pair.UserID {
				copy := m
				found = &copy
			}
		}
		page, err = page.GetNextPage()
	}
	if err != nil {
		if apiutil.IsNotFound(err) {
			return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, nil
		}
		return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, err
	}
	if found == nil {
		return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, nil
	}
	out, err := memberObserved(found, pair)
	if err != nil {
		return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{}, err
	}
	return infer.ReadResponse[RbacGroupMemberArgs, RbacGroupMemberState]{ID: req.ID, Inputs: pair, State: out}, err
}

func (controller *RbacGroupMember) Update(ctx context.Context, req infer.UpdateRequest[RbacGroupMemberArgs, RbacGroupMemberState]) (infer.UpdateResponse[RbacGroupMemberState], error) {
	// No mutable properties exist. Refresh belongs to the engine, not Update.
	if req.DryRun {
		out := req.State
		out.RbacGroupMemberArgs = req.Inputs
		return infer.UpdateResponse[RbacGroupMemberState]{Output: out}, nil
	}
	return infer.UpdateResponse[RbacGroupMemberState]{Output: req.State}, nil
}

func (controller *RbacGroupMember) Delete(ctx context.Context, req infer.DeleteRequest[RbacGroupMemberState]) (infer.DeleteResponse, error) {
	pair, err := memberPair(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.RBACGroups.Members.Remove(ctx, pair.UserID, sdk.OrganizationRBACGroupMemberRemoveParams{RBACGroupID: pair.RbacGroupID})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve logical input metadata.
type memberResource struct{ infer.InferredResource }

func (r memberResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&RbacGroupMember{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
		for _, k := range []string{"rbacGroupId", "userId"} {
			v := req.Inputs.Get(k)
			if v.IsString() && (v.AsString() == "" || strings.Contains(v.AsString(), "/")) {
				resp.Failures = append(resp.Failures, p.CheckFailure{Property: k, Reason: "must be a nonempty ID without '/'"})
			}
		}
	}
	return resp, err
}

func (r memberResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&RbacGroupMember{}).Diff(ctx, req)
}

func (r memberResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&RbacGroupMember{}).Create(ctx, req)
}

func (r memberResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&RbacGroupMember{}).Read(ctx, req)
}

func (r memberResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&RbacGroupMember{}).Update(ctx, req)
}

func (r memberResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&RbacGroupMember{}).Delete(ctx, req)
}

func memberPair(id string) (RbacGroupMemberArgs, error) {
	groupID, userID, ok := resourceutil.SplitID(id)
	if !ok {
		return RbacGroupMemberArgs{}, fmt.Errorf("membership ID must be <rbac-group-id>/<user-id>")
	}
	return RbacGroupMemberArgs{RbacGroupID: groupID, UserID: userID}, nil
}

func memberObserved(r *sdk.RBACGroupMember, pair RbacGroupMemberArgs) (RbacGroupMemberState, error) {
	out := RbacGroupMemberState{RbacGroupMemberArgs: pair}
	if r == nil || !apiutil.JSONString(r.JSON.RBACGroupID.Raw()) || !apiutil.JSONString(r.JSON.UserID.Raw()) || r.RBACGroupID != pair.RbacGroupID || r.UserID != pair.UserID || r.JSON.Type.Raw() != `"rbac_group_member"` {
		return out, errors.New("invalid membership response identity or type")
	}
	if apiutil.JSONString(r.JSON.Email.Raw()) {
		out.Email = r.Email
	} else {
		return RbacGroupMemberState{}, errors.New("invalid membership email")
	}
	if apiutil.JSONString(r.JSON.CreatedAt.Raw()) && !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if out.CreatedAt == "" {
		return out, errors.New("incomplete membership response")
	}
	return out, nil
}
