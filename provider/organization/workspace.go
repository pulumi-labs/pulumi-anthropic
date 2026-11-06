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

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// Workspace is an Anthropic workspace.
type Workspace struct {
	rest.Resource[WorkspaceArgs, WorkspaceState, *WorkspaceState]
}

// NewWorkspace returns the Workspace resource controller.
func NewWorkspace() *Workspace {
	// Whether the API merges or replaces tags on update is undocumented; they are
	// treated as replaced, so no tombstones are sent. Unverified against the live API.
	return &Workspace{rest.Resource[WorkspaceArgs, WorkspaceState, *WorkspaceState]{
		Collection: workspaceCollection,
		EndOfLife:  rest.Archive,
	}}
}

func (w *Workspace) Annotate(a infer.Annotator) {
	a.Describe(w, "A workspace: a unit of isolation for API keys, usage, limits, and "+
		"workspace-scoped resources such as agents and skills.\n\n"+
		"The API cannot delete workspaces, so deleting this resource archives the workspace, "+
		"which cannot be undone.")
}

type WorkspaceArgs struct {
	Name          string            `pulumi:"name"`
	DisplayColor  *string           `pulumi:"displayColor,optional"`
	Tags          map[string]string `pulumi:"tags,optional"`
	DataResidency *DataResidency    `pulumi:"dataResidency,optional"`
	ExternalKeyID *string           `pulumi:"externalKeyId,optional"`
}

func (w *WorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&w.Name, "The workspace's name, 1 to 40 characters.")
	a.Describe(&w.DisplayColor, "The color the Console shows the workspace in, as `#RRGGBB`.")
	a.Describe(&w.Tags, "Tags for the workspace. Keys may not start with `anthropic`.")
	a.Describe(&w.DataResidency, "Where the workspace's data is stored and where inference may run.")
	a.Describe(&w.ExternalKeyID, "The ID of a customer-managed encryption key (an "+
		"`anthropic.security.ExternalKey`) that encrypts the workspace's data. It can be set on "+
		"a workspace that has none, but once set it can never be changed or removed, so any "+
		"change to it replaces the workspace.")
}

// DataResidency controls where a workspace's data lives and where inference runs.
type DataResidency struct {
	WorkspaceGeo         *WorkspaceGeo  `pulumi:"workspaceGeo,optional"         provider:"replaceOnChanges" wire:"createOnly"`
	AllowedInferenceGeos []InferenceGeo `pulumi:"allowedInferenceGeos,optional"`
	DefaultInferenceGeo  *InferenceGeo  `pulumi:"defaultInferenceGeo,optional"`
}

func (d *DataResidency) Annotate(a infer.Annotator) {
	a.Describe(&d.WorkspaceGeo, "Where the workspace's data is stored. It cannot change, "+
		"so changing it replaces the workspace.")
	a.Describe(&d.AllowedInferenceGeos, "The geographies inference may run in. "+
		"Omit to allow any geography.")
	a.Describe(&d.DefaultInferenceGeo, "The geography inference runs in when a request doesn't choose one.")
}

// unrestricted is how the API spells "any geography" for allowed_inference_geos.
const unrestricted = "unrestricted"

// dataResidencyWire is DataResidency without its custom encoding.
type dataResidencyWire DataResidency

// ExtendWire sends an unset AllowedInferenceGeos as "unrestricted", explicitly: the API
// keeps the stored value of an omitted field, so omitting it could never lift a
// restriction.
func (d DataResidency) ExtendWire(obj map[string]any) {
	if d.AllowedInferenceGeos == nil {
		obj["allowed_inference_geos"] = unrestricted
	}
}

// UnmarshalJSON decodes "unrestricted" as an unset AllowedInferenceGeos.
func (d *DataResidency) UnmarshalJSON(raw []byte) error {
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields["allowed_inference_geos"] == unrestricted {
		delete(fields, "allowed_inference_geos")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return rest.Decode(raw, (*dataResidencyWire)(d))
}

// WorkspaceGeo is where a workspace's data is stored.
type WorkspaceGeo string

func (WorkspaceGeo) Values() []infer.EnumValue[WorkspaceGeo] {
	return []infer.EnumValue[WorkspaceGeo]{{Value: "us", Description: "The United States."}}
}

// InferenceGeo is a geography inference can run in.
type InferenceGeo string

func (InferenceGeo) Values() []infer.EnumValue[InferenceGeo] {
	return []infer.EnumValue[InferenceGeo]{
		{Value: "global", Description: "Any geography."},
		{Value: "us", Description: "The United States."},
	}
}

type WorkspaceState struct {
	WorkspaceArgs
	CreatedAt     string `pulumi:"createdAt"`
	CompartmentID string `pulumi:"compartmentId"`
}

func (s *WorkspaceState) Inputs() *WorkspaceArgs { return &s.WorkspaceArgs }

func (s *WorkspaceState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the workspace was created, in RFC 3339 format.")
	a.Describe(&s.CompartmentID, "The ID of the storage compartment that holds the workspace's data.")
}

var (
	_ infer.CustomDiff[WorkspaceArgs, WorkspaceState]   = (*Workspace)(nil)
	_ infer.CustomUpdate[WorkspaceArgs, WorkspaceState] = (*Workspace)(nil)
	_ infer.CustomRead[WorkspaceArgs, WorkspaceState]   = (*Workspace)(nil)
	_ infer.CustomDelete[WorkspaceState]                = (*Workspace)(nil)
)

// Diff adds one rule to the standard diff: externalKeyId is write-once, so setting it on
// a workspace that has none is an update, but any change once it is set is a replacement.
func (w *Workspace) Diff(
	_ context.Context, req infer.DiffRequest[WorkspaceArgs, WorkspaceState],
) (infer.DiffResponse, error) {
	diff := rest.Diff(req.State.WorkspaceArgs, req.Inputs)
	if d, changed := diff.DetailedDiff["externalKeyId"]; changed && req.State.ExternalKeyID != nil {
		d.Kind = rest.AsReplace(d.Kind)
		diff.DetailedDiff["externalKeyId"] = d
	}
	return diff, nil
}
