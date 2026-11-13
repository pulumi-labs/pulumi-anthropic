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
	"errors"
	"regexp"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// ServiceAccount is a non-human identity that workloads authenticate as through workload
// identity federation.
type ServiceAccount struct{}

// NewServiceAccount returns the complete ServiceAccount resource.
func NewServiceAccount() infer.InferredResource {
	return serviceAccountResource{infer.Resource(&ServiceAccount{})}
}

func (s *ServiceAccount) Annotate(a infer.Annotator) {
	a.Describe(s, "A service account: a non-human identity that workloads assume through "+
		"workload identity federation (see `FederationRule`), in place of long-lived API keys. "+
		"Grant it access to workspaces with `ServiceAccountWorkspace`.\n\n"+
		"Managing service accounts requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`); the API does not accept Admin API keys for them.\n\n"+
		"The API cannot delete service accounts, so deleting this resource archives it. "+
		"Archiving is refused while a live federation rule targets the account.")
}

type ServiceAccountArgs struct {
	Name             string              `pulumi:"name"                      provider:"replaceOnChanges"`
	Description      *string             `pulumi:"description,optional"`
	OrganizationRole *ServiceAccountRole `pulumi:"organizationRole,optional"`
}

func (s *ServiceAccountArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "A unique organization slug of 1–255 lowercase letters, digits or hyphens. "+
		"It cannot be changed; changing it replaces the service account.")
	a.Describe(&s.Description, "A description of the service account, up to 2,000 characters. Removing a previously configured description clears it.")
	a.Describe(&s.OrganizationRole, "The service account's organization role. Defaults to `developer` when created. Omitting or removing this property leaves the current role unchanged; explicitly set `developer` to demote an admin. Configuring `admin` requires an interactive credential on every update, including description-only edits.")
}

type ServiceAccountState struct {
	ServiceAccountArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *ServiceAccountState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the service account was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the service account was last updated, in RFC 3339 format.")
}

// ServiceAccountRole is a service account's role in the organization. Its values differ
// from the roles people hold (organization.OrganizationRole).
type ServiceAccountRole string

func (ServiceAccountRole) Values() []infer.EnumValue[ServiceAccountRole] {
	return []infer.EnumValue[ServiceAccountRole]{
		{Value: "developer", Description: "Can use the API in the workspaces it is a member of."},
		{Value: "admin", Description: "Can administer the organization. Creating an admin " +
			"service account requires an interactive credential."},
	}
}

func (controller *ServiceAccount) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[ServiceAccountArgs], error) {
	inputs, failures, err := infer.DefaultCheck[ServiceAccountArgs](ctx, req.NewInputs)
	if err != nil {
		return infer.CheckResponse[ServiceAccountArgs]{}, err
	}
	if v := req.NewInputs.Get("name"); v.IsString() && (!regexp.MustCompile(`^[a-z0-9-]{1,255}$`).MatchString(v.AsString())) {
		failures = append(failures, p.CheckFailure{Property: "name", Reason: "must contain 1–255 lowercase letters, digits or hyphens"})
	}
	if v := req.NewInputs.Get("description"); v.IsString() && utf8.RuneCountInString(v.AsString()) > 2000 {
		failures = append(failures, p.CheckFailure{Property: "description", Reason: "must not exceed 2,000 characters"})
	}
	if v := req.NewInputs.Get("organizationRole"); v.IsString() && v.AsString() != "admin" && v.AsString() != "developer" {
		failures = append(failures, p.CheckFailure{Property: "organizationRole", Reason: "must be admin or developer"})
	}
	return infer.CheckResponse[ServiceAccountArgs]{Inputs: inputs, Failures: failures}, nil
}

func (controller *ServiceAccount) Create(ctx context.Context, req infer.CreateRequest[ServiceAccountArgs]) (infer.CreateResponse[ServiceAccountState], error) {
	if req.DryRun {
		return infer.CreateResponse[ServiceAccountState]{Output: ServiceAccountState{ServiceAccountArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.CreateResponse[ServiceAccountState]{}, err
	}
	p := sdk.OrganizationServiceAccountNewParams{Name: req.Inputs.Name}
	if req.Inputs.Description != nil {
		p.Description = sdk.String(*req.Inputs.Description)
	}
	if req.Inputs.OrganizationRole != nil {
		p.OrganizationRole = sdk.OrganizationServiceAccountNewParamsOrganizationRole(*req.Inputs.OrganizationRole)
	}
	r, err := c.Organization.ServiceAccounts.New(ctx, p)
	if err != nil {
		return infer.CreateResponse[ServiceAccountState]{}, err
	}
	out, id, err := serviceAccountObserved(r, "", req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[ServiceAccountState]{}, err
	}
	return infer.CreateResponse[ServiceAccountState]{ID: id, Output: out}, err
}

func (controller *ServiceAccount) Read(ctx context.Context, req infer.ReadRequest[ServiceAccountArgs, ServiceAccountState]) (infer.ReadResponse[ServiceAccountArgs, ServiceAccountState], error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{}, err
	}
	r, err := c.Organization.ServiceAccounts.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{}, err
	}
	if r != nil && r.ID == req.ID && apiutil.JSONString(r.JSON.ID.Raw()) && (r.JSON.Type.Raw() == "" || r.JSON.Type.Raw() == `"service_account"`) && apiutil.JSONString(r.JSON.ArchivedAt.Raw()) && !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{}, nil
	}
	out, id, err := serviceAccountObserved(r, req.ID, req.Inputs, req.Inputs.Name == "")
	if err != nil {
		return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{}, err
	}
	return infer.ReadResponse[ServiceAccountArgs, ServiceAccountState]{ID: id, Inputs: out.ServiceAccountArgs, State: out}, err
}

func (controller *ServiceAccount) Update(ctx context.Context, req infer.UpdateRequest[ServiceAccountArgs, ServiceAccountState]) (infer.UpdateResponse[ServiceAccountState], error) {
	if req.State.OrganizationRole != nil && req.Inputs.OrganizationRole == nil {
		p.GetLogger(ctx).Warning("Removing organizationRole leaves the current role unchanged. Explicitly set organizationRole to developer to demote an admin.")
	}
	if req.DryRun {
		out := req.State
		out.ServiceAccountArgs = req.Inputs
		return infer.UpdateResponse[ServiceAccountState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.UpdateResponse[ServiceAccountState]{}, err
	}
	p := sdk.OrganizationServiceAccountUpdateParams{}
	if req.Inputs.Description != nil {
		p.Description = sdk.String(*req.Inputs.Description)
	} else if req.State.Description != nil {
		p.Description = param.Null[string]()
	}
	if req.Inputs.OrganizationRole != nil {
		p.OrganizationRole = sdk.OrganizationServiceAccountUpdateParamsOrganizationRole(*req.Inputs.OrganizationRole)
	}
	r, err := c.Organization.ServiceAccounts.Update(ctx, req.ID, p)
	if err != nil {
		return infer.UpdateResponse[ServiceAccountState]{}, err
	}
	out, _, err := serviceAccountObserved(r, req.ID, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[ServiceAccountState]{}, err
	}
	return infer.UpdateResponse[ServiceAccountState]{Output: out}, err
}

func (controller *ServiceAccount) Delete(ctx context.Context, req infer.DeleteRequest[ServiceAccountState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.ServiceAccounts.Archive(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve input metadata that the typed representation cannot express.
type serviceAccountResource struct{ infer.InferredResource }

func (r serviceAccountResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&ServiceAccount{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r serviceAccountResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&ServiceAccount{}).Diff(ctx, req)
}

func (r serviceAccountResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&ServiceAccount{}).Create(ctx, req)
}

func (r serviceAccountResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&ServiceAccount{}).Read(ctx, req)
}

func (r serviceAccountResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&ServiceAccount{}).Update(ctx, req)
}

func (r serviceAccountResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&ServiceAccount{}).Delete(ctx, req)
}

// Validate wire shapes before returning server facts.
func serviceAccountObserved(r *sdk.ServiceAccount, expected string, managed ServiceAccountArgs, importing bool) (ServiceAccountState, string, error) {
	out := ServiceAccountState{}
	var invalid error
	if r == nil {
		return out, "", errors.New("missing service account response")
	}
	id := r.ID
	if !apiutil.JSONString(r.JSON.ID.Raw()) || id == "" || (expected != "" && id != expected) || (r.JSON.Type.Raw() != "" && r.JSON.Type.Raw() != `"service_account"`) {
		return out, "", errors.New("invalid service account response identity or type")
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "" && raw != "null" && (!apiutil.JSONString(raw) || r.ArchivedAt.IsZero()) {
		invalid = errors.New("invalid service account archive timestamp")
	}
	if apiutil.JSONString(r.JSON.Name.Raw()) && r.Name != "" {
		out.Name = r.Name
	}
	if importing || managed.Description != nil {
		// An omitted description may be absent or null, including on ID-only Read.
		if raw := r.JSON.Description.Raw(); raw == "" || raw == "null" {
			out.Description = nil
		} else if apiutil.JSONString(raw) {
			v := r.Description
			out.Description = &v
		} else {
			invalid = errors.New("invalid service account description")
		}
	}
	if importing || managed.OrganizationRole != nil {
		if apiutil.JSONString(r.JSON.OrganizationRole.Raw()) && (r.OrganizationRole == "admin" || r.OrganizationRole == "developer") {
			v := ServiceAccountRole(r.OrganizationRole)
			out.OrganizationRole = &v
		} else {
			invalid = errors.New("invalid service account organization role")
		}
	}
	if apiutil.JSONString(r.JSON.CreatedAt.Raw()) && !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if apiutil.JSONString(r.JSON.UpdatedAt.Raw()) && !r.UpdatedAt.IsZero() {
		out.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	if out.Name == "" || out.CreatedAt == "" || out.UpdatedAt == "" {
		return ServiceAccountState{}, "", errors.New("incomplete service account response")
	}
	return out, id, invalid
}
