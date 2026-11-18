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

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// ComplianceSettings manages the organization's existing singleton.
type ComplianceSettings struct{}

// NewComplianceSettings returns the complete ComplianceSettings resource.
func NewComplianceSettings() infer.InferredResource {
	return complianceSettingsResource{infer.Resource(&ComplianceSettings{})}
}

const complianceSettingsID = "compliance-settings"

func (c *ComplianceSettings) Annotate(a infer.Annotator) {
	a.Describe(c, "The organization's compliance settings, which control the Compliance API. "+
		"An organization has exactly one set, so declare at most one of these per organization.\n\n"+
		"An organization with a parent inherits its parent's settings, and provisioning Access "+
		"Transparency re-enables the Compliance API, which a refresh reports as drift.\n\n"+
		"Settings cannot be deleted, so deleting this resource leaves them as they are.")
}

type ComplianceSettingsArgs struct {
	Enabled bool `pulumi:"enabled"`
}

func (c *ComplianceSettingsArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.Enabled, "Whether the Compliance API is enabled.")
}

type ComplianceSettingsState struct{ ComplianceSettingsArgs }

func (c *ComplianceSettings) Create(ctx context.Context, req infer.CreateRequest[ComplianceSettingsArgs]) (infer.CreateResponse[ComplianceSettingsState], error) {
	if req.DryRun {
		return infer.CreateResponse[ComplianceSettingsState]{ID: complianceSettingsID, Output: ComplianceSettingsState{req.Inputs}}, nil
	}
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[ComplianceSettingsState]{}, err
	}
	v, err := client.Organization.ComplianceSettings.Update(ctx, complianceParams(req.Inputs))
	if err != nil {
		return infer.CreateResponse[ComplianceSettingsState]{}, err
	}
	out, err := complianceState(v)
	if err != nil {
		return infer.CreateResponse[ComplianceSettingsState]{}, err
	}
	return infer.CreateResponse[ComplianceSettingsState]{ID: complianceSettingsID, Output: out}, nil
}

func (c *ComplianceSettings) Read(ctx context.Context, req infer.ReadRequest[ComplianceSettingsArgs, ComplianceSettingsState]) (infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState], error) {
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	v, err := client.Organization.ComplianceSettings.Get(ctx)
	if err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	out, err := complianceState(v)
	if err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{ID: complianceSettingsID, Inputs: out.ComplianceSettingsArgs, State: out}, nil
}

func (c *ComplianceSettings) Update(ctx context.Context, req infer.UpdateRequest[ComplianceSettingsArgs, ComplianceSettingsState]) (infer.UpdateResponse[ComplianceSettingsState], error) {
	if req.DryRun {
		return infer.UpdateResponse[ComplianceSettingsState]{Output: ComplianceSettingsState{req.Inputs}}, nil
	}
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[ComplianceSettingsState]{}, err
	}
	v, err := client.Organization.ComplianceSettings.Update(ctx, complianceParams(req.Inputs))
	if err != nil {
		return infer.UpdateResponse[ComplianceSettingsState]{}, err
	}
	out, err := complianceState(v)
	if err != nil {
		return infer.UpdateResponse[ComplianceSettingsState]{}, err
	}
	return infer.UpdateResponse[ComplianceSettingsState]{Output: out}, nil
}

func (c *ComplianceSettings) Delete(ctx context.Context, req infer.DeleteRequest[ComplianceSettingsState]) (infer.DeleteResponse, error) {
	p.GetLogger(ctx).Warningf("Compliance settings cannot be deleted; leaving the organization's settings unchanged.")
	return infer.DeleteResponse{}, nil
}

// Preserve validated input metadata across infer's typed codec.
type complianceSettingsResource struct{ infer.InferredResource }

// The default typed Check drops output-value dependencies. No normalization is
// needed for this boolean; preserve the validated bag verbatim.
func (r complianceSettingsResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&ComplianceSettings{}).Check(ctx, req)
	if err == nil && len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

func (r complianceSettingsResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&ComplianceSettings{}).Diff(ctx, req)
}

func (r complianceSettingsResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&ComplianceSettings{}).Create(ctx, req)
}

func (r complianceSettingsResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&ComplianceSettings{}).Read(ctx, req)
}

func (r complianceSettingsResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&ComplianceSettings{}).Update(ctx, req)
}

func (r complianceSettingsResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&ComplianceSettings{}).Delete(ctx, req)
}

func complianceParams(in ComplianceSettingsArgs) sdk.OrganizationComplianceSettingUpdateParams {
	state := sdk.ComplianceSettingsStateParamUnion{}
	if in.Enabled {
		v := sdk.NewComplianceSettingsStateEnabledParam()
		state.OfEnabled = &v
	} else {
		v := sdk.NewComplianceSettingsStateDisabledParam()
		state.OfDisabled = &v
	}
	return sdk.OrganizationComplianceSettingUpdateParams{State: state}
}

func complianceState(v *sdk.OrganizationComplianceSettings) (ComplianceSettingsState, error) {
	if v != nil && v.JSON.State.Valid() && v.State.JSON.Type.Valid() {
		switch v.State.Type {
		case "enabled":
			return ComplianceSettingsState{ComplianceSettingsArgs{Enabled: true}}, nil
		case "disabled":
			return ComplianceSettingsState{ComplianceSettingsArgs{Enabled: false}}, nil
		}
	}
	return ComplianceSettingsState{}, fmt.Errorf("compliance settings response has missing or unsupported state")
}
