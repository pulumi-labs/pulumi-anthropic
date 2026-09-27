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
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// ComplianceSettings is the organization's compliance settings, a singleton.
type ComplianceSettings struct{}

// NewComplianceSettings returns the ComplianceSettings resource controller.
func NewComplianceSettings() *ComplianceSettings { return &ComplianceSettings{} }

// complianceSettingsID is the ID of the organization's one set of compliance settings.
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

type ComplianceSettingsState struct {
	ComplianceSettingsArgs
}

var (
	_ infer.CustomUpdate[ComplianceSettingsArgs, ComplianceSettingsState] = (*ComplianceSettings)(nil)
	_ infer.CustomRead[ComplianceSettingsArgs, ComplianceSettingsState]   = (*ComplianceSettings)(nil)
	_ infer.CustomDelete[ComplianceSettingsState]                         = (*ComplianceSettings)(nil)
)

func (c *ComplianceSettings) Create(
	ctx context.Context, req infer.CreateRequest[ComplianceSettingsArgs],
) (infer.CreateResponse[ComplianceSettingsState], error) {
	out := ComplianceSettingsState{req.Inputs}
	if req.DryRun {
		return infer.CreateResponse[ComplianceSettingsState]{ID: complianceSettingsID, Output: out}, nil
	}
	return infer.CreateResponse[ComplianceSettingsState]{ID: complianceSettingsID, Output: out},
		setComplianceSettings(ctx, req.Inputs)
}

func (c *ComplianceSettings) Read(
	ctx context.Context, req infer.ReadRequest[ComplianceSettingsArgs, ComplianceSettingsState],
) (infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState], error) {
	var raw []byte
	if err := complianceSettingsCollection.Do(ctx, http.MethodGet, complianceSettingsCollection.Path, nil, &raw); err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	var settings struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	state, err := rest.UnmarshalTypeTag(settings.State)
	if err != nil {
		return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{}, err
	}
	in := ComplianceSettingsArgs{Enabled: state == "enabled"}
	return infer.ReadResponse[ComplianceSettingsArgs, ComplianceSettingsState]{
		ID: complianceSettingsID, Inputs: in, State: ComplianceSettingsState{in},
	}, nil
}

func (c *ComplianceSettings) Update(
	ctx context.Context, req infer.UpdateRequest[ComplianceSettingsArgs, ComplianceSettingsState],
) (infer.UpdateResponse[ComplianceSettingsState], error) {
	out := ComplianceSettingsState{req.Inputs}
	if req.DryRun {
		return infer.UpdateResponse[ComplianceSettingsState]{Output: out}, nil
	}
	return infer.UpdateResponse[ComplianceSettingsState]{Output: out}, setComplianceSettings(ctx, req.Inputs)
}

func (c *ComplianceSettings) Delete(
	ctx context.Context, req infer.DeleteRequest[ComplianceSettingsState],
) (infer.DeleteResponse, error) {
	return infer.DeleteResponse{}, rest.End(ctx, complianceSettingsCollection, req.ID, rest.Forget)
}

// setComplianceSettings sets the settings, which the API encodes as
// {"state": {"type": "enabled"}}.
func setComplianceSettings(ctx context.Context, in ComplianceSettingsArgs) error {
	state := "disabled"
	if in.Enabled {
		state = "enabled"
	}
	body := map[string]any{"state": map[string]any{"type": state}}
	return complianceSettingsCollection.Do(ctx, http.MethodPost, complianceSettingsCollection.Path, body, nil)
}
