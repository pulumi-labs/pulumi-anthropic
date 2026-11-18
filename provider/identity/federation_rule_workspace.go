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
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// FederationRuleWorkspace manages an explicit workspace enablement.
type FederationRuleWorkspace struct{}

// NewFederationRuleWorkspace returns the complete FederationRuleWorkspace resource.
func NewFederationRuleWorkspace() infer.InferredResource {
	return federationRuleWorkspaceResource{infer.Resource(&FederationRuleWorkspace{})}
}

func (f *FederationRuleWorkspace) Annotate(a infer.Annotator) {
	a.Describe(f, "Binds a federation rule to an additional workspace. The resource ID is `<federation-rule-id>/<workspace-id>` and supports import. Changing either ID replaces the binding. Deleting the resource removes this explicit binding, not the rule or workspace. Other grants, including all-workspace access, are unaffected.\n\nDo not also manage the workspace the rule was created in with this resource. Manage each rule/workspace pair with only one resource: deleting either duplicate removes the shared binding.\n\nRequires an OAuth token with `org:admin` (`anthropic:authToken`). The rule and workspace must belong to your organization; the rule must grant `workspace:developer` or `workspace:inference`. Enabling a workspace does not enroll the target service account: token exchange requires its membership in that workspace.")
}

type FederationRuleWorkspaceArgs struct {
	FederationRuleID string `pulumi:"federationRuleId" provider:"replaceOnChanges"`
	WorkspaceID      string `pulumi:"workspaceId" provider:"replaceOnChanges"`
}

func (f *FederationRuleWorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.FederationRuleID, "The federation rule's nonempty ID. Changing it replaces the binding.")
	a.Describe(&f.WorkspaceID, "The workspace's nonempty ID. Changing it replaces the binding.")
}

type FederationRuleWorkspaceState struct {
	FederationRuleWorkspaceArgs
	WorkspaceName *string `pulumi:"workspaceName,optional"`
	CreatedAt     *string `pulumi:"createdAt,optional"`
}

func (f *FederationRuleWorkspaceState) Annotate(a infer.Annotator) {
	a.Describe(&f.WorkspaceName, "The workspace name reported when the binding is read. Not available immediately after enabling the binding.")
	a.Describe(&f.CreatedAt, "When the binding was created, in RFC 3339 format, when available.")
}

func (*FederationRuleWorkspace) Create(ctx context.Context, req infer.CreateRequest[FederationRuleWorkspaceArgs]) (infer.CreateResponse[FederationRuleWorkspaceState], error) {
	if req.DryRun {
		// Non-nil observation placeholders let infer mark optional fields unknown.
		return infer.CreateResponse[FederationRuleWorkspaceState]{Output: FederationRuleWorkspaceState{FederationRuleWorkspaceArgs: req.Inputs, WorkspaceName: new(string), CreatedAt: new(string)}}, nil
	}
	out, err := addBinding(ctx, req.Inputs)
	if err != nil {
		return infer.CreateResponse[FederationRuleWorkspaceState]{}, err
	}
	return infer.CreateResponse[FederationRuleWorkspaceState]{ID: bindingID(req.Inputs), Output: out}, nil
}

func (*FederationRuleWorkspace) Read(ctx context.Context, req infer.ReadRequest[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]) (infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState], error) {
	a, err := bindingIDs(req.ID)
	if err != nil {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, err
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, err
	}
	page, err := c.Organization.Federation.Rules.Workspaces.List(ctx, a.FederationRuleID, sdk.OrganizationFederationRuleWorkspaceListParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, err
	}
	// A malformed list must not masquerade as confirmed absence. Validate the
	// envelope and every row before deciding the requested pair is missing.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if page == nil {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, errors.New("missing binding list")
	}
	if err := json.Unmarshal([]byte(page.RawJSON()), &envelope); err != nil || len(envelope.Data) == 0 || envelope.Data[0] != '[' {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, errors.New("invalid binding list")
	}
	var found *FederationRuleWorkspaceState
	for i := range page.Data {
		row := &page.Data[i]
		if row.WorkspaceID == "" || row.FederationRuleID != a.FederationRuleID {
			return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, errors.New("invalid binding list identity")
		}
		out, err := bindingObservation(row, FederationRuleWorkspaceArgs{FederationRuleID: a.FederationRuleID, WorkspaceID: row.WorkspaceID})
		if err != nil {
			return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, err
		}
		if row.WorkspaceID == a.WorkspaceID {
			found = &out
		}
	}
	if found == nil {
		return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{}, nil
	}
	return infer.ReadResponse[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]{ID: req.ID, Inputs: a, State: *found}, nil
}

// There are no mutable fields. Refresh belongs to the engine.
func (*FederationRuleWorkspace) Update(ctx context.Context, req infer.UpdateRequest[FederationRuleWorkspaceArgs, FederationRuleWorkspaceState]) (infer.UpdateResponse[FederationRuleWorkspaceState], error) {
	if req.DryRun {
		out := req.State
		out.FederationRuleWorkspaceArgs = req.Inputs
		return infer.UpdateResponse[FederationRuleWorkspaceState]{Output: out}, nil
	}
	a, err := bindingIDs(req.ID)
	if err != nil {
		return infer.UpdateResponse[FederationRuleWorkspaceState]{}, err
	}
	if a != req.Inputs {
		return infer.UpdateResponse[FederationRuleWorkspaceState]{}, errors.New("changing binding identity requires replacement")
	}
	return infer.UpdateResponse[FederationRuleWorkspaceState]{Output: req.State}, nil
}

func (*FederationRuleWorkspace) Delete(ctx context.Context, req infer.DeleteRequest[FederationRuleWorkspaceState]) (infer.DeleteResponse, error) {
	a, err := bindingIDs(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Federation.Rules.Workspaces.Remove(ctx, a.WorkspaceID, sdk.OrganizationFederationRuleWorkspaceRemoveParams{FederationRuleID: a.FederationRuleID})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// This resource has no normalization, so preserve accepted input bags verbatim.
type federationRuleWorkspaceResource struct{ infer.InferredResource }

func (r federationRuleWorkspaceResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&FederationRuleWorkspace{}).Check(ctx, req)
	if err != nil {
		return resp, err
	}
	for _, field := range []string{"federationRuleId", "workspaceId"} {
		value := req.Inputs.Get(field)
		if value.IsString() && (value.AsString() == "" || strings.Contains(value.AsString(), "/")) {
			resp.Failures = append(resp.Failures, p.CheckFailure{Property: field, Reason: "must be a nonempty ID without '/'"})
		}
	}
	if len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, nil
}

func (r federationRuleWorkspaceResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&FederationRuleWorkspace{}).Diff(ctx, req)
}

func (r federationRuleWorkspaceResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&FederationRuleWorkspace{}).Create(ctx, req)
}

func (r federationRuleWorkspaceResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&FederationRuleWorkspace{}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		for _, k := range []string{"federationRuleId", "workspaceId"} {
			old := req.Inputs.Get(k)
			resp.Inputs = resp.Inputs.Set(k, resp.Inputs.Get(k).WithSecret(old.Secret()).WithDependencies(old.Dependencies()))
			// Read preserves state secret paths, but does not wire input secrecy.
			// Protect input-only reads without forwarding dependency annotations.
			value := resp.Properties.Get(k)
			resp.Properties = resp.Properties.Set(k, value.WithSecret(value.Secret() || old.Secret()))
		}
	}
	return resp, err
}

func (r federationRuleWorkspaceResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&FederationRuleWorkspace{}).Update(ctx, req)
}

func (r federationRuleWorkspaceResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&FederationRuleWorkspace{}).Delete(ctx, req)
}

func bindingIDs(id string) (FederationRuleWorkspaceArgs, error) {
	ruleID, workspaceID, ok := resourceutil.SplitID(id)
	if !ok {
		return FederationRuleWorkspaceArgs{}, fmt.Errorf("expected binding ID <federation-rule-id>/<workspace-id>")
	}
	return FederationRuleWorkspaceArgs{FederationRuleID: ruleID, WorkspaceID: workspaceID}, nil
}

func bindingID(a FederationRuleWorkspaceArgs) string { return a.FederationRuleID + "/" + a.WorkspaceID }

func addBinding(ctx context.Context, a FederationRuleWorkspaceArgs) (FederationRuleWorkspaceState, error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return FederationRuleWorkspaceState{}, err
	}
	r, err := c.Organization.Federation.Rules.Workspaces.Add(ctx, a.FederationRuleID, sdk.OrganizationFederationRuleWorkspaceAddParams{WorkspaceID: a.WorkspaceID})
	if err != nil {
		return FederationRuleWorkspaceState{}, err
	}
	return bindingObservation(r, a)
}

func bindingObservation(r *sdk.FederationRuleWorkspace, a FederationRuleWorkspaceArgs) (FederationRuleWorkspaceState, error) {
	out := FederationRuleWorkspaceState{FederationRuleWorkspaceArgs: a}
	if r == nil || !apiutil.JSONNonEmptyString(r.JSON.FederationRuleID.Raw()) || !apiutil.JSONNonEmptyString(r.JSON.WorkspaceID.Raw()) || r.FederationRuleID != a.FederationRuleID || r.WorkspaceID != a.WorkspaceID || r.JSON.Type.Raw() != `"federation_rule_workspace"` {
		return out, errors.New("invalid federation workspace binding response identity or type")
	}
	var problems []string
	raw := r.JSON.WorkspaceName.Raw()
	if raw != "" && raw != "null" {
		var name string
		if json.Unmarshal([]byte(raw), &name) != nil {
			problems = append(problems, "invalid workspace name")
		} else {
			out.WorkspaceName = &name
		}
	} else if raw == "" {
		problems = append(problems, "missing workspace name")
	}
	if r.CreatedAt.IsZero() {
		problems = append(problems, "missing or invalid creation time")
	} else {
		value := r.CreatedAt.Format(time.RFC3339Nano)
		out.CreatedAt = &value
	}
	if len(problems) > 0 {
		return out, errors.New(strings.Join(problems, "; "))
	}
	return out, nil
}
