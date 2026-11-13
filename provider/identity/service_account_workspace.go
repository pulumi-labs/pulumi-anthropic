// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/roles"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// ServiceAccountWorkspace manages an explicit service account workspace grant.
type ServiceAccountWorkspace struct{}

// NewServiceAccountWorkspace returns the complete ServiceAccountWorkspace resource.
func NewServiceAccountWorkspace() infer.InferredResource {
	return serviceAccountWorkspaceResource{infer.Resource(&ServiceAccountWorkspace{})}
}

func (s *ServiceAccountWorkspace) Annotate(a infer.Annotator) {
	a.Describe(s, "Grants a service account a role in a workspace. The resource ID is `<workspace-id>/<service-account-id>` and supports import of explicit grants. Changing either ID replaces the grant. Manage each pair with only one resource: creating a grant for an existing pair replaces its role, and deleting either duplicate removes the shared explicit grant.\n\nEvery service account has implicit `workspace_user` access to the organization's default workspace. Creating this resource there makes an explicit grant; deleting it restores implicit access rather than revoking all access. Implicit memberships cannot be imported. If an explicit grant becomes implicit, refresh reports the explicit grant as absent. Deleting this resource does not delete the service account or workspace.\n\nRequires an OAuth token with `org:admin` (`anthropic:authToken`).")
}

type ServiceAccountWorkspaceArgs struct {
	WorkspaceID      string        `pulumi:"workspaceId" provider:"replaceOnChanges"`
	ServiceAccountID string        `pulumi:"serviceAccountId" provider:"replaceOnChanges"`
	WorkspaceRole    WorkspaceRole `pulumi:"workspaceRole"`
}

func (s *ServiceAccountWorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.WorkspaceID, "The workspace's nonempty ID. Changing it replaces the grant.")
	a.Describe(&s.ServiceAccountID, "The service account's nonempty ID. Changing it replaces the grant.")
	a.Describe(&s.WorkspaceRole, "The service account's role in the workspace. Required; service accounts cannot hold the workspace billing role. Changing this value changes the explicit grant's role.")
}

type ServiceAccountWorkspaceState struct{ ServiceAccountWorkspaceArgs }

// WorkspaceRole excludes the people-only billing role.
type WorkspaceRole string

func (WorkspaceRole) Values() []infer.EnumValue[WorkspaceRole] {
	var values []infer.EnumValue[WorkspaceRole]
	for _, r := range roles.Workspace {
		if r.Value != roles.Billing {
			values = append(values, infer.EnumValue[WorkspaceRole]{Value: WorkspaceRole(r.Value), Description: r.Description})
		}
	}
	return values
}

func (controller *ServiceAccountWorkspace) Create(ctx context.Context, req infer.CreateRequest[ServiceAccountWorkspaceArgs]) (infer.CreateResponse[ServiceAccountWorkspaceState], error) {
	if req.DryRun {
		return infer.CreateResponse[ServiceAccountWorkspaceState]{Output: ServiceAccountWorkspaceState{req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.CreateResponse[ServiceAccountWorkspaceState]{}, err
	}
	r, err := c.Organization.Workspaces.ServiceAccounts.Add(ctx, req.Inputs.WorkspaceID, sdk.OrganizationWorkspaceServiceAccountAddParams{ServiceAccountID: req.Inputs.ServiceAccountID, WorkspaceRole: sdk.NoBillingWorkspaceRole(req.Inputs.WorkspaceRole)}, option.WithMaxRetries(0))
	if err != nil {
		return infer.CreateResponse[ServiceAccountWorkspaceState]{}, err
	}
	a := req.Inputs
	a.WorkspaceRole = ""
	out, implicit, err := sawObservation(r, a, true)
	if err == nil && implicit {
		err = errors.New("add did not establish an explicit workspace grant")
	}
	if err != nil {
		return infer.CreateResponse[ServiceAccountWorkspaceState]{}, err
	}
	return infer.CreateResponse[ServiceAccountWorkspaceState]{ID: a.WorkspaceID + "/" + a.ServiceAccountID, Output: out}, nil
}

func (controller *ServiceAccountWorkspace) Read(ctx context.Context, req infer.ReadRequest[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]) (infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState], error) {
	a, err := sawPair(req.ID)
	if err != nil {
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, err
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, err
	}
	r, err := c.Organization.Workspaces.ServiceAccounts.Get(ctx, a.ServiceAccountID, sdk.OrganizationWorkspaceServiceAccountGetParams{WorkspaceID: a.WorkspaceID})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, err
	}
	out, implicit, err := sawObservation(r, a, false)
	if err != nil {
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, err
	}
	if implicit {
		if req.Inputs.WorkspaceID == "" && req.State.WorkspaceID == "" {
			return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, errors.New("implicit workspace membership cannot be imported; create an explicit ServiceAccountWorkspace grant instead")
		}
		return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{}, nil
	}
	return infer.ReadResponse[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]{ID: req.ID, Inputs: out.ServiceAccountWorkspaceArgs, State: out}, nil
}

func (controller *ServiceAccountWorkspace) Update(ctx context.Context, req infer.UpdateRequest[ServiceAccountWorkspaceArgs, ServiceAccountWorkspaceState]) (infer.UpdateResponse[ServiceAccountWorkspaceState], error) {
	if req.DryRun {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{Output: ServiceAccountWorkspaceState{req.Inputs}}, nil
	}
	a, err := sawPair(req.ID)
	if err != nil {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{}, err
	}
	if a.WorkspaceID != req.Inputs.WorkspaceID || a.ServiceAccountID != req.Inputs.ServiceAccountID {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{}, errors.New("changing grant identity requires replacement")
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{}, err
	}
	r, err := c.Organization.Workspaces.ServiceAccounts.Update(ctx, a.ServiceAccountID, sdk.OrganizationWorkspaceServiceAccountUpdateParams{WorkspaceID: a.WorkspaceID, WorkspaceRole: sdk.NoBillingWorkspaceRole(req.Inputs.WorkspaceRole)}, option.WithMaxRetries(0))
	if err != nil {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{}, err
	}
	out, implicit, err := sawObservation(r, a, true)
	if err == nil && implicit {
		err = errors.New("Update did not establish an explicit workspace grant")
	}
	if err != nil {
		return infer.UpdateResponse[ServiceAccountWorkspaceState]{}, err
	}
	return infer.UpdateResponse[ServiceAccountWorkspaceState]{Output: out}, nil
}

func (controller *ServiceAccountWorkspace) Delete(ctx context.Context, req infer.DeleteRequest[ServiceAccountWorkspaceState]) (infer.DeleteResponse, error) {
	a, err := sawPair(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Workspaces.ServiceAccounts.Remove(ctx, a.ServiceAccountID, sdk.OrganizationWorkspaceServiceAccountRemoveParams{WorkspaceID: a.WorkspaceID})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve logical input metadata at the property boundary.
type serviceAccountWorkspaceResource struct{ infer.InferredResource }

func (r serviceAccountWorkspaceResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&ServiceAccountWorkspace{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		for _, k := range []string{"workspaceId", "serviceAccountId"} {
			v := req.Inputs.Get(k)
			if v.IsString() && (v.AsString() == "" || strings.Contains(v.AsString(), "/")) {
				resp.Failures = append(resp.Failures, p.CheckFailure{Property: k, Reason: "must be a nonempty ID without '/'"})
			}
		}
		v := req.Inputs.Get("workspaceRole")
		if v.IsString() && !sawRole(WorkspaceRole(v.AsString())) {
			resp.Failures = append(resp.Failures, p.CheckFailure{Property: "workspaceRole", Reason: "must be a supported non-billing workspace role"})
		}
		if len(resp.Failures) == 0 {
			resp.Inputs = req.Inputs
		}
	}
	return resp, err
}

func (r serviceAccountWorkspaceResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&ServiceAccountWorkspace{}).Diff(ctx, req)
}

func (r serviceAccountWorkspaceResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&ServiceAccountWorkspace{}).Create(ctx, req)
}

func (r serviceAccountWorkspaceResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&ServiceAccountWorkspace{}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		for _, k := range []string{"workspaceId", "serviceAccountId", "workspaceRole"} {
			v := req.Inputs.Get(k)
			resp.Inputs = resp.Inputs.Set(k, resp.Inputs.Get(k).WithSecret(v.Secret()).WithDependencies(v.Dependencies()))
			// Read preserves state secret paths, but does not wire input secrecy.
			// Protect input-only reads without forwarding dependency annotations.
			value := resp.Properties.Get(k)
			resp.Properties = resp.Properties.Set(k, value.WithSecret(value.Secret() || v.Secret()))
		}
	}
	return resp, err
}

func (r serviceAccountWorkspaceResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&ServiceAccountWorkspace{}).Update(ctx, req)
}

func (r serviceAccountWorkspaceResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&ServiceAccountWorkspace{}).Delete(ctx, req)
}

func sawRole(role WorkspaceRole) bool {
	for _, v := range (WorkspaceRole("")).Values() {
		if v.Value == role {
			return true
		}
	}
	return false
}

func sawPair(id string) (ServiceAccountWorkspaceArgs, error) {
	workspaceID, serviceAccountID, ok := resourceutil.SplitID(id)
	if !ok {
		return ServiceAccountWorkspaceArgs{}, fmt.Errorf("grant ID must be <workspace-id>/<service-account-id>")
	}
	return ServiceAccountWorkspaceArgs{WorkspaceID: workspaceID, ServiceAccountID: serviceAccountID}, nil
}

func sawObservation(r *sdk.ServiceAccountWorkspaceMember, a ServiceAccountWorkspaceArgs, explicitMutation bool) (ServiceAccountWorkspaceState, bool, error) {
	a.WorkspaceRole = ""
	out := ServiceAccountWorkspaceState{a}
	if r == nil || !apiutil.JSONNonEmptyString(r.JSON.WorkspaceID.Raw()) || !apiutil.JSONNonEmptyString(r.JSON.ServiceAccountID.Raw()) || r.WorkspaceID != a.WorkspaceID || r.ServiceAccountID != a.ServiceAccountID || r.JSON.Type.Raw() != `"service_account_workspace_member"` {
		return out, false, errors.New("invalid workspace grant response identity or type")
	}
	// Validate raw types: the SDK can coerce JSON primitives into strings.
	if !apiutil.JSONNonEmptyString(r.JSON.WorkspaceRole.Raw()) || !sawRole(WorkspaceRole(r.WorkspaceRole)) {
		return out, false, errors.New("invalid workspace grant role")
	}
	out.WorkspaceRole = WorkspaceRole(r.WorkspaceRole)
	// The TypeScript contract declares implicit as boolean | null on all
	// operations. Only true identifies implicit access. Add/Update may also
	// omit the flag, but their successful operation establishes an explicit grant.
	if raw := r.JSON.Implicit.Raw(); raw == "null" || (raw == "" && explicitMutation) {
		return out, false, nil
	}
	if r.JSON.Implicit.Raw() != "true" && r.JSON.Implicit.Raw() != "false" {
		shape := "non-boolean"
		if r.JSON.Implicit.Raw() == "" {
			shape = "absent"
		} else if r.JSON.Implicit.Raw() == "null" {
			shape = "null"
		}
		return out, false, fmt.Errorf("invalid workspace grant implicit flag: expected a boolean, got %s", shape)
	}
	return out, r.Implicit, nil
}
