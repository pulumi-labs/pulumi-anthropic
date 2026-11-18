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
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// RbacGroup is a group of users that roles are granted to.
type RbacGroup struct{}

// NewRbacGroup returns the complete RbacGroup resource.
func NewRbacGroup() infer.InferredResource {
	return rbacGroupResource{infer.Resource(&RbacGroup{})}
}

func (g *RbacGroup) Annotate(a infer.Annotator) {
	a.Describe(g, "A role-based access control group of users. Add users with "+
		"`RbacGroupMember`. Roles are attached to groups in the organization's admin settings, "+
		"and are reported by `roleIds`.\n\n"+
		"RBAC groups are available to Claude Enterprise organizations. Groups provisioned by "+
		"SCIM cannot be changed or deleted while SCIM provisioning is active. Destroying this resource permanently deletes the group; owner-access protections may prevent deletion.")
}

type RbacGroupArgs struct {
	Name string `pulumi:"name"`
}

func (g *RbacGroupArgs) Annotate(a infer.Annotator) {
	a.Describe(&g.Name, "The group's name. Names need not be unique and can be changed without replacing the group.")
}

type RbacGroupState struct {
	RbacGroupArgs
	RoleIDs    []string `pulumi:"roleIds,optional"`
	SourceType string   `pulumi:"sourceType"`
	CreatedAt  string   `pulumi:"createdAt"`
	UpdatedAt  string   `pulumi:"updatedAt"`
}

func (g *RbacGroupState) Annotate(a infer.Annotator) {
	a.Describe(&g.RoleIDs, "The IDs of the roles attached to the group. An empty list means no roles are attached. If this value is absent, role information is temporarily unavailable; refresh again rather than treating it as an empty list.")
	a.Describe(&g.SourceType, "How the group was provisioned: `direct` or `scim`.")
	a.Describe(&g.CreatedAt, "When the group was created, in RFC 3339 format.")
	a.Describe(&g.UpdatedAt, "When the group was last updated, in RFC 3339 format.")
}

func (controller *RbacGroup) Create(ctx context.Context, req infer.CreateRequest[RbacGroupArgs]) (infer.CreateResponse[RbacGroupState], error) {
	if req.DryRun {
		// A non-nil observation placeholder lets infer mark this optional list unknown.
		return infer.CreateResponse[RbacGroupState]{Output: RbacGroupState{RbacGroupArgs: req.Inputs, RoleIDs: []string{}}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[RbacGroupState]{}, err
	}
	// Generated New uses conservative retries: names are not unique and cannot be used to recover ambiguous allocations.
	r, err := c.Organization.RBACGroups.New(ctx, sdk.OrganizationRBACGroupNewParams{Name: req.Inputs.Name})
	if err != nil {
		return infer.CreateResponse[RbacGroupState]{}, err
	}
	out, id, err := rbacGroupObserved(r, "")
	if err != nil {
		return infer.CreateResponse[RbacGroupState]{}, err
	}
	return infer.CreateResponse[RbacGroupState]{ID: id, Output: out}, err
}

func (controller *RbacGroup) Read(ctx context.Context, req infer.ReadRequest[RbacGroupArgs, RbacGroupState]) (infer.ReadResponse[RbacGroupArgs, RbacGroupState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[RbacGroupArgs, RbacGroupState]{}, err
	}
	r, err := c.Organization.RBACGroups.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[RbacGroupArgs, RbacGroupState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[RbacGroupArgs, RbacGroupState]{}, err
	}
	out, id, err := rbacGroupObserved(r, req.ID)
	if err != nil {
		return infer.ReadResponse[RbacGroupArgs, RbacGroupState]{}, err
	}
	return infer.ReadResponse[RbacGroupArgs, RbacGroupState]{ID: id, Inputs: out.RbacGroupArgs, State: out}, err
}

func (controller *RbacGroup) Update(ctx context.Context, req infer.UpdateRequest[RbacGroupArgs, RbacGroupState]) (infer.UpdateResponse[RbacGroupState], error) {
	if req.DryRun {
		out := req.State
		out.RbacGroupArgs = req.Inputs
		return infer.UpdateResponse[RbacGroupState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[RbacGroupState]{}, err
	}
	// The engine owns refresh. Send the full mutable configuration.
	r, err := c.Organization.RBACGroups.Update(ctx, req.ID, sdk.OrganizationRBACGroupUpdateParams{Name: sdk.String(req.Inputs.Name)})
	if err != nil {
		return infer.UpdateResponse[RbacGroupState]{}, err
	}
	out, _, err := rbacGroupObserved(r, req.ID)
	if err != nil {
		return infer.UpdateResponse[RbacGroupState]{}, err
	}
	return infer.UpdateResponse[RbacGroupState]{Output: out}, err
}

func (controller *RbacGroup) Delete(ctx context.Context, req infer.DeleteRequest[RbacGroupState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.RBACGroups.Delete(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve metadata and the distinction between unavailable and empty role lists.
type rbacGroupResource struct{ infer.InferredResource }

func (r rbacGroupResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&RbacGroup{}).Check(ctx, req)
	// No normalization: preserve the original logical value and all metadata,
	// including infer v1.5's lossy secret-wrapped unknown case.
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r rbacGroupResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&RbacGroup{}).Diff(ctx, req)
}

func (r rbacGroupResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&RbacGroup{}).Create(ctx, req)
}

func (r rbacGroupResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&RbacGroup{}).Read(ctx, req)
}

func (r rbacGroupResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&RbacGroup{}).Update(ctx, req)
}

func (r rbacGroupResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&RbacGroup{}).Delete(ctx, req)
}

func rbacGroupObserved(r *sdk.RBACGroup, expected string) (RbacGroupState, string, error) {
	out := RbacGroupState{}
	if r == nil {
		return out, "", errors.New("missing RBAC group response")
	}
	id := ""
	if apiutil.JSONString(r.JSON.ID.Raw()) {
		id = r.ID
	}
	// Foreign identities or object types establish no observations of the managed object.
	if id == "" || (expected != "" && id != expected) || (r.JSON.Type.Raw() != "" && r.JSON.Type.Raw() != `"rbac_group"`) {
		return out, "", errors.New("invalid RBAC group response identity or type")
	}
	if apiutil.JSONString(r.JSON.Name.Raw()) {
		out.Name = r.Name
	} else {
		return out, id, errors.New("invalid RBAC group name")
	}
	if apiutil.JSONString(r.JSON.SourceType.Raw()) && (r.SourceType == sdk.RBACGroupSourceTypeDirect || r.SourceType == sdk.RBACGroupSourceTypeSCIM) {
		out.SourceType = string(r.SourceType)
	}
	if apiutil.JSONString(r.JSON.CreatedAt.Raw()) && !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if apiutil.JSONString(r.JSON.UpdatedAt.Raw()) && !r.UpdatedAt.IsZero() {
		out.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	roles := r.JSON.RoleIDs.Raw()
	if roles != "" && roles != "null" {
		var values []json.RawMessage
		valid := json.Unmarshal([]byte(roles), &values) == nil && len(roles) > 0 && roles[0] == '['
		for _, v := range values {
			valid = valid && apiutil.JSONString(string(v))
		}
		if valid {
			out.RoleIDs = append([]string{}, r.RoleIDs...)
		} else {
			return out, id, errors.New("invalid RBAC group role IDs")
		}
	}
	if out.SourceType == "" || out.CreatedAt == "" || out.UpdatedAt == "" {
		return RbacGroupState{}, "", errors.New("incomplete RBAC group response")
	}
	return out, id, nil
}
