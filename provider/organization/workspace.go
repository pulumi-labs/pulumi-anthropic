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
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Workspace is an Anthropic workspace.
type Workspace struct{ operation *workspaceOperationState }

// NewWorkspace returns the complete Workspace resource.
func NewWorkspace() infer.InferredResource {
	return workspaceResource{infer.Resource(&Workspace{})}
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
	UserProfileID *string           `pulumi:"userProfileId,optional"`
}

func (w *WorkspaceArgs) Annotate(a infer.Annotator) {
	a.Describe(&w.Name, "The workspace's nonempty name.")
	a.Describe(&w.DisplayColor, "The color the Console shows the workspace in, as `#RRGGBB`. Removing this setting leaves the last color in place; it cannot be cleared.")
	a.Describe(&w.Tags, "Tags for the workspace. Keys may not start with `anthropic`. Removing a previously configured entry deletes that entry. Tags added outside this resource are preserved.")
	a.Describe(&w.DataResidency, "Where the workspace's data is stored and where inference may run. Removing previously configured mutable inference settings allows any geography and resets the default to `global`; this does not change the storage geography.")
	a.Describe(&w.ExternalKeyID, "The ID of a customer-managed encryption key (an "+
		"`anthropic.security.ExternalKey`) that encrypts the workspace's data. It can be set on "+
		"a workspace that has none, but once set it can never be changed or removed, so any "+
		"change to it replaces the workspace.")
	a.Describe(&w.UserProfileID, "The user profile attached to this workspace. Every inference request must name that profile. It can be attached to a workspace that has none, except the default workspace. Once attached, changing or removing this setting replaces the workspace. Deleting the profile itself clears the attachment.")
}

// DataResidency controls where a workspace's data lives and where inference runs.
type DataResidency struct {
	WorkspaceGeo         *WorkspaceGeo  `pulumi:"workspaceGeo,optional"         provider:"replaceOnChanges"`
	AllowedInferenceGeos []InferenceGeo `pulumi:"allowedInferenceGeos,optional"`
	DefaultInferenceGeo  *InferenceGeo  `pulumi:"defaultInferenceGeo,optional"`
}

func (d *DataResidency) Annotate(a infer.Annotator) {
	a.Describe(&d.WorkspaceGeo, "Where the workspace's data is stored. It cannot change, "+
		"so changing it replaces the workspace. Adding or removing an explicit storage geography, including by removing dataResidency, also replaces the workspace.")
	a.Describe(&d.AllowedInferenceGeos, "The geographies inference may run in. "+
		"Omit to allow any geography. An explicit list must be nonempty and contain the default inference geography. Removing a configured list allows any geography.")
	a.Describe(&d.DefaultInferenceGeo, "The geography inference runs in when a request doesn't choose one. Defaults to `global`. Removing this setting resets it to `global`, which must be allowed by the inference geography list.")
}

// unrestricted is how the API spells "any geography" for allowed_inference_geos.
const unrestricted = "unrestricted"

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

func (s *WorkspaceState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the workspace was created, in RFC 3339 format.")
	a.Describe(&s.CompartmentID, "The ID of the storage compartment that holds the workspace's data.")
}

var workspaceColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (controller *Workspace) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[WorkspaceArgs], error) {
	a, failures, err := infer.DefaultCheck[WorkspaceArgs](ctx, req.NewInputs)
	fail := func(k, reason string) { failures = append(failures, p.CheckFailure{Property: k, Reason: reason}) }
	if v := req.NewInputs.Get("name"); v.IsString() && v.AsString() == "" {
		fail("name", "must not be empty")
	}
	if v := req.NewInputs.Get("displayColor"); v.IsString() && !workspaceColor.MatchString(v.AsString()) {
		fail("displayColor", "must have the form #RRGGBB")
	}
	for _, k := range []string{"externalKeyId", "userProfileId"} {
		if v := req.NewInputs.Get(k); v.IsString() && v.AsString() == "" {
			fail(k, "must not be empty")
		}
	}
	if v := req.NewInputs.Get("tags"); v.IsMap() {
		for k := range v.AsMap().All {
			if strings.HasPrefix(k, "anthropic") {
				fail("tags", "keys must not start with anthropic")
			}
		}
	}
	if v := req.NewInputs.Get("dataResidency"); v.IsMap() {
		m := v.AsMap()
		if g := m.Get("workspaceGeo"); g.IsString() && g.AsString() != "us" {
			fail("dataResidency.workspaceGeo", "must be us")
		}
		def := m.Get("defaultInferenceGeo")
		if def.IsString() && !workspaceInference(def.AsString()) {
			fail("dataResidency.defaultInferenceGeo", "must be global or us")
		}
		list := m.Get("allowedInferenceGeos")
		if list.IsArray() {
			if list.AsArray().Len() == 0 {
				fail("dataResidency.allowedInferenceGeos", "must be nonempty; omit to allow any geography")
			}
			unknown, found := false, false
			expected := "global"
			if def.IsString() {
				expected = def.AsString()
			}
			if def.IsComputed() {
				unknown = true
			}
			for _, g := range list.AsArray().All {
				if g.IsComputed() {
					unknown = true
				} else if g.IsString() {
					if !workspaceInference(g.AsString()) {
						fail("dataResidency.allowedInferenceGeos", "values must be global or us")
					}
					if g.AsString() == expected {
						found = true
					}
				}
			}
			if !unknown && !found && list.AsArray().Len() > 0 {
				fail("dataResidency.defaultInferenceGeo", "the default geography (global when omitted) must be in allowedInferenceGeos")
			}
		}
	}
	return infer.CheckResponse[WorkspaceArgs]{Inputs: a, Failures: failures}, err
}

func (controller *Workspace) Create(ctx context.Context, req infer.CreateRequest[WorkspaceArgs]) (infer.CreateResponse[WorkspaceState], error) {
	if req.DryRun {
		return infer.CreateResponse[WorkspaceState]{Output: WorkspaceState{WorkspaceArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[WorkspaceState]{}, err
	}
	r, err := c.Organization.Workspaces.New(ctx, workspaceNew(req.Inputs))
	if err != nil {
		return infer.CreateResponse[WorkspaceState]{}, err
	}
	s, err := workspaceState(r)
	id := workspaceIdentity(r)
	if err != nil {
		return infer.CreateResponse[WorkspaceState]{}, err
	}
	return infer.CreateResponse[WorkspaceState]{ID: id, Output: s}, nil
}

func (controller *Workspace) Read(ctx context.Context, req infer.ReadRequest[WorkspaceArgs, WorkspaceState]) (infer.ReadResponse[WorkspaceArgs, WorkspaceState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, err
	}
	r, err := c.Organization.Workspaces.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, err
	}
	if workspaceIdentity(r) != req.ID {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, fmt.Errorf("workspace response identity missing or mismatched")
	}
	s, err := workspaceState(r)
	if err != nil {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{}, nil
	}
	inputs := s.WorkspaceArgs
	managed := controller.operation != nil && controller.operation.managedRead
	if managed {
		inputs = workspaceReadInputs(s.WorkspaceArgs, req.Inputs)
	}
	return infer.ReadResponse[WorkspaceArgs, WorkspaceState]{ID: req.ID, Inputs: inputs, State: s}, nil
}

func (controller *Workspace) Update(ctx context.Context, req infer.UpdateRequest[WorkspaceArgs, WorkspaceState]) (infer.UpdateResponse[WorkspaceState], error) {
	if req.DryRun {
		return infer.UpdateResponse[WorkspaceState]{Output: WorkspaceState{WorkspaceArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[WorkspaceState]{}, err
	}
	owned := workspaceOwned(controller.operation, req.State.WorkspaceArgs)
	r, err := c.Organization.Workspaces.Update(ctx, req.ID, workspaceUpdate(req.State, owned, req.Inputs))
	if err != nil {
		return infer.UpdateResponse[WorkspaceState]{}, err
	}
	if workspaceIdentity(r) != req.ID {
		return infer.UpdateResponse[WorkspaceState]{}, fmt.Errorf("workspace response identity missing or mismatched")
	}
	s, err := workspaceState(r)
	if err != nil {
		return infer.UpdateResponse[WorkspaceState]{}, err
	}
	return infer.UpdateResponse[WorkspaceState]{Output: s}, nil
}

func (controller *Workspace) Delete(ctx context.Context, req infer.DeleteRequest[WorkspaceState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Workspaces.Archive(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// These adapters preserve validated Check inputs and distinguish an ID-only
// Read from managed refresh. Output secrecy and preview knownness use infer.
type workspaceResource struct{ infer.InferredResource }

// workspaceOperationState belongs to one lifecycle invocation, never to the registered resource.
type workspaceOperationState struct {
	oldInputs   property.Map
	managedRead bool
}

func (r workspaceResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&Workspace{operation: &workspaceOperationState{}}).Check(ctx, req)
	if err == nil && len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

// Compare managed values, not API defaults or tags owned by other clients.
// Actual observations remain present in outputs. Removal is still compared to
// old inputs, so it triggers the one update needed to apply the effective reset.
func (r workspaceResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	for _, k := range []string{"displayColor", "tags", "dataResidency"} {
		if req.OldInputs.Get(k).IsNull() && req.Inputs.Get(k).IsNull() {
			req.State = req.State.Delete(k)
		}
	}
	if v := req.State.Get("tags"); v.IsMap() {
		m := v.AsMap()
		for k := range m.All {
			owned := false
			for _, bag := range []property.Map{req.OldInputs, req.Inputs} {
				if tags := bag.Get("tags"); tags.IsMap() && !tags.AsMap().Get(k).IsNull() {
					owned = true
				}
			}
			if !owned {
				m = m.Delete(k)
			}
		}
		req.State = req.State.Set("tags", property.WithGoValue(v, m))
	}
	if v := req.State.Get("dataResidency"); v.IsMap() {
		m := v.AsMap()
		old, next := req.OldInputs.Get("dataResidency"), req.Inputs.Get("dataResidency")
		geo := func(v property.Value) property.Value {
			if v.IsComputed() {
				return v
			}
			if v.IsMap() {
				return v.AsMap().Get("workspaceGeo")
			}
			return property.Value{}
		}
		if geo(old).IsNull() && geo(next).IsNull() {
			m = m.Delete("workspaceGeo")
		}
		if next.IsMap() && next.AsMap().Get("defaultInferenceGeo").IsNull() && m.Get("defaultInferenceGeo").IsString() && m.Get("defaultInferenceGeo").AsString() == "global" {
			m = m.Delete("defaultInferenceGeo")
		}
		req.State = req.State.Set("dataResidency", property.WithGoValue(v, m))
	}
	out, err := infer.Resource(&Workspace{operation: &workspaceOperationState{}}).Diff(ctx, req)
	if err != nil {
		return out, err
	}
	replace := func(key string) {
		if out.DetailedDiff == nil {
			out.DetailedDiff = map[string]p.PropertyDiff{}
		}
		kind := p.UpdateReplace
		if req.Inputs.Get(key).IsNull() {
			kind = p.DeleteReplace
		} else if req.State.Get(key).IsNull() {
			kind = p.AddReplace
		}
		out.DetailedDiff[key] = p.PropertyDiff{Kind: kind, InputDiff: true}
		out.HasChanges = true
	}
	scalarEqual := func(a, b property.Value) bool {
		return a.WithSecret(false).WithDependencies(nil).Equals(b.WithSecret(false).WithDependencies(nil))
	}
	for _, k := range []string{"externalKeyId", "userProfileId"} {
		if !slices.Contains(req.IgnoreChanges, k) && !req.State.Get(k).IsNull() && !scalarEqual(req.State.Get(k), req.Inputs.Get(k)) {
			replace(k)
		}
	}
	geo := func(v property.Value) property.Value {
		if v.IsComputed() {
			return v
		}
		if v.IsMap() {
			return v.AsMap().Get("workspaceGeo")
		}
		return property.Value{}
	}
	if !slices.Contains(req.IgnoreChanges, "dataResidency") && !slices.Contains(req.IgnoreChanges, "dataResidency.workspaceGeo") && !scalarEqual(geo(req.OldInputs.Get("dataResidency")), geo(req.Inputs.Get("dataResidency"))) {
		replace("dataResidency")
		for k := range out.DetailedDiff {
			if strings.HasPrefix(k, "dataResidency.") {
				delete(out.DetailedDiff, k)
			}
		}
	}
	return out, nil
}

func (r workspaceResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&Workspace{operation: &workspaceOperationState{}}).Create(ctx, req)
}

func (r workspaceResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	operation := &workspaceOperationState{managedRead: req.Properties.Len() != 0 || req.Inputs.Len() != 0}
	resp, err := infer.Resource(&Workspace{operation: operation}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		// Read restores old secret paths but does not apply input secret wiring.
		// Protect corresponding properties without copying data or dependencies.
		for key, input := range req.Inputs.All {
			if !input.HasSecrets() {
				continue
			}
			if value, ok := resp.Inputs.GetOk(key); ok {
				resp.Inputs = resp.Inputs.Set(key, value.WithSecret(true))
			}
			if value, ok := resp.Properties.GetOk(key); ok {
				resp.Properties = resp.Properties.Set(key, value.WithSecret(true))
			}
		}
	}
	return resp, err
}

func (r workspaceResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	operation := &workspaceOperationState{oldInputs: req.OldInputs}
	return infer.Resource(&Workspace{operation: operation}).Update(ctx, req)
}

func (r workspaceResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Workspace{operation: &workspaceOperationState{}}).Delete(ctx, req)
}

func workspaceInference(s string) bool { return s == "global" || s == "us" }

func workspaceGeos(a []InferenceGeo) []anthropic.AllowedInferenceGeo {
	out := make([]anthropic.AllowedInferenceGeo, len(a))
	for i, g := range a {
		out[i] = anthropic.AllowedInferenceGeo(g)
	}
	return out
}

func workspaceNew(a WorkspaceArgs) anthropic.OrganizationWorkspaceNewParams {
	r := anthropic.OrganizationWorkspaceNewParams{Name: a.Name, Tags: maps.Clone(a.Tags)}
	if a.DisplayColor != nil {
		r.DisplayColor = anthropic.String(*a.DisplayColor)
	}
	if a.ExternalKeyID != nil {
		r.ExternalKeyID = anthropic.String(*a.ExternalKeyID)
	}
	if a.UserProfileID != nil {
		r.UserProfileID = anthropic.String(*a.UserProfileID)
	}
	if d := a.DataResidency; d != nil {
		r.DataResidency.AllowedInferenceGeos.OfUnrestricted = constant.ValueOf[constant.Unrestricted]()
		if d.AllowedInferenceGeos != nil {
			r.DataResidency.AllowedInferenceGeos = anthropic.DataResidencyCreateConfigAllowedInferenceGeosUnionParam{OfGeos: workspaceGeos(d.AllowedInferenceGeos)}
		}
		r.DataResidency.DefaultInferenceGeo = "global"
		if d.DefaultInferenceGeo != nil {
			r.DataResidency.DefaultInferenceGeo = anthropic.DataResidencyCreateConfigDefaultInferenceGeo(*d.DefaultInferenceGeo)
		}
		if d.WorkspaceGeo != nil {
			r.DataResidency.WorkspaceGeo = anthropic.DataResidencyCreateConfigWorkspaceGeo(*d.WorkspaceGeo)
		}
	}
	return r
}

func workspaceUpdate(old WorkspaceState, owned, desired WorkspaceArgs) anthropic.OrganizationWorkspaceUpdateParams {
	r := anthropic.OrganizationWorkspaceUpdateParams{Name: anthropic.String(desired.Name)}
	if desired.DisplayColor != nil {
		r.DisplayColor = anthropic.String(*desired.DisplayColor)
	}
	if old.ExternalKeyID == nil && desired.ExternalKeyID != nil {
		r.ExternalKeyID = anthropic.String(*desired.ExternalKeyID)
	}
	if old.UserProfileID == nil && desired.UserProfileID != nil {
		r.UserProfileID = anthropic.String(*desired.UserProfileID)
	}
	tags := apiutil.MergedStringMap(owned.Tags, desired.Tags)
	if desired.Tags != nil || len(tags) != 0 {
		r.SetExtraFields(map[string]any{"tags": tags})
	}
	if desired.DataResidency != nil || owned.DataResidency != nil {
		r.DataResidency.AllowedInferenceGeos.OfUnrestricted = constant.ValueOf[constant.Unrestricted]()
		r.DataResidency.DefaultInferenceGeo = "global"
		if d := desired.DataResidency; d != nil {
			if d.AllowedInferenceGeos != nil {
				r.DataResidency.AllowedInferenceGeos = anthropic.DataResidencyUpdateConfigAllowedInferenceGeosUnionParam{OfGeos: workspaceGeos(d.AllowedInferenceGeos)}
			}
			if d.DefaultInferenceGeo != nil {
				r.DataResidency.DefaultInferenceGeo = anthropic.DataResidencyUpdateConfigDefaultInferenceGeo(*d.DefaultInferenceGeo)
			}
		}
	}
	return r
}

func workspaceIdentity(r *anthropic.Workspace) string {
	if r == nil {
		return ""
	}
	if raw := r.JSON.Type.Raw(); raw != "" {
		if typ, ok := apiutil.JSONStringValue(raw); !ok || typ != "workspace" {
			return ""
		}
	}
	v, ok := apiutil.JSONStringValue(r.JSON.ID.Raw())
	if !ok {
		return ""
	}
	return v
}

func workspaceState(r *anthropic.Workspace) (WorkspaceState, error) {
	s := WorkspaceState{}
	if r == nil {
		return s, fmt.Errorf("missing workspace response")
	}
	var problems []string
	bad := func(k string) { problems = append(problems, "invalid or missing "+k) }
	if workspaceIdentity(r) == "" {
		bad("id")
	}
	if v, ok := apiutil.JSONStringValue(r.JSON.Type.Raw()); !ok || v != "workspace" {
		bad("type")
	}
	if v, ok := apiutil.JSONStringValue(r.JSON.Name.Raw()); ok && v != "" {
		s.Name = v
	} else {
		bad("name")
	}
	if v, ok := apiutil.JSONStringValue(r.JSON.CompartmentID.Raw()); ok && v != "" {
		s.CompartmentID = v
	} else {
		bad("compartment_id")
	}
	if v, ok := apiutil.JSONStringValue(r.JSON.CreatedAt.Raw()); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			s.CreatedAt = t.Format(time.RFC3339Nano)
		} else {
			bad("created_at")
		}
	} else {
		bad("created_at")
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "null" {
		if v, ok := apiutil.JSONStringValue(raw); !ok {
			bad("archived_at")
		} else if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
			bad("archived_at")
		}
	}
	if v, ok := apiutil.JSONStringValue(r.JSON.DisplayColor.Raw()); ok && workspaceColor.MatchString(v) {
		s.DisplayColor = &v
	} else {
		bad("display_color")
	}
	nullableID := func(raw, value, key string) *string {
		if raw == "null" {
			return nil
		}
		if raw == "" || value == "" {
			bad(key)
			return nil
		}
		var v string
		if json.Unmarshal([]byte(raw), &v) != nil {
			bad(key)
			return nil
		}
		return &v
	}
	s.ExternalKeyID = nullableID(r.JSON.ExternalKeyID.Raw(), r.ExternalKeyID, "external_key_id")
	s.UserProfileID = nullableID(r.JSON.UserProfileID.Raw(), r.UserProfileID, "user_profile_id")
	var tags map[string]json.RawMessage
	if json.Unmarshal([]byte(r.JSON.Tags.Raw()), &tags) != nil || tags == nil {
		bad("tags")
	} else {
		s.Tags = map[string]string{}
		for k, raw := range tags {
			var v string
			if string(raw) == "null" || json.Unmarshal(raw, &v) != nil {
				bad("tags." + k)
			} else {
				s.Tags[k] = v
			}
		}
	}
	if r.JSON.DataResidency.Raw() == "" || r.JSON.DataResidency.Raw() == "null" {
		bad("data_residency")
	} else {
		d := r.DataResidency
		s.DataResidency = &DataResidency{}
		if g, ok := apiutil.JSONStringValue(d.JSON.WorkspaceGeo.Raw()); ok && g == "us" {
			v := WorkspaceGeo(g)
			s.DataResidency.WorkspaceGeo = &v
		} else {
			bad("data_residency.workspace_geo")
		}
		if g, ok := apiutil.JSONStringValue(d.JSON.DefaultInferenceGeo.Raw()); ok && workspaceInference(g) {
			v := InferenceGeo(g)
			s.DataResidency.DefaultInferenceGeo = &v
		} else {
			bad("data_residency.default_inference_geo")
		}
		raw := d.AllowedInferenceGeos.RawJSON()
		if sentinel, ok := apiutil.JSONStringValue(raw); !ok || sentinel != unrestricted {
			var list []string
			if json.Unmarshal([]byte(raw), &list) != nil || len(list) == 0 {
				bad("data_residency.allowed_inference_geos")
			} else {
				valid := true
				for _, g := range list {
					if !workspaceInference(g) {
						valid = false
					}
				}
				if !valid {
					bad("data_residency.allowed_inference_geos")
				} else {
					s.DataResidency.AllowedInferenceGeos = make([]InferenceGeo, len(list))
					for i, g := range list {
						s.DataResidency.AllowedInferenceGeos[i] = InferenceGeo(g)
					}
				}
			}
		}
		if s.DataResidency.AllowedInferenceGeos != nil && s.DataResidency.DefaultInferenceGeo != nil && !slices.Contains(s.DataResidency.AllowedInferenceGeos, *s.DataResidency.DefaultInferenceGeo) {
			bad("data_residency.default_inference_geo membership")
		}
	}
	if len(problems) > 0 {
		return s, fmt.Errorf("workspace response: %s", strings.Join(problems, "; "))
	}
	return s, nil
}

func workspaceOwned(operation *workspaceOperationState, fallback WorkspaceArgs) WorkspaceArgs {
	if operation == nil {
		return fallback
	}
	bag := operation.oldInputs
	// Decode only the fields needed for removal ownership.
	a := WorkspaceArgs{}
	if v := bag.Get("tags"); v.IsMap() {
		a.Tags = map[string]string{}
		for k, v := range v.AsMap().All {
			if v.IsString() {
				a.Tags[k] = v.AsString()
			}
		}
	}
	if v := bag.Get("dataResidency"); v.IsMap() {
		a.DataResidency = &DataResidency{}
	}
	return a
}

func workspaceReadInputs(s, prior WorkspaceArgs) WorkspaceArgs {
	out := s
	if prior.DisplayColor == nil {
		out.DisplayColor = nil
	}
	if prior.Tags == nil {
		out.Tags = nil
	} else {
		out.Tags = map[string]string{}
		for k := range prior.Tags {
			if v, ok := s.Tags[k]; ok {
				out.Tags[k] = v
			}
		}
	}
	if prior.DataResidency == nil {
		out.DataResidency = nil
	} else if s.DataResidency != nil {
		d := *s.DataResidency
		if prior.DataResidency.WorkspaceGeo == nil {
			d.WorkspaceGeo = nil
		}
		if prior.DataResidency.DefaultInferenceGeo == nil && d.DefaultInferenceGeo != nil && *d.DefaultInferenceGeo == "global" {
			d.DefaultInferenceGeo = nil
		}
		out.DataResidency = &d
	}
	// Bindings are write-once facts, not clearable defaults; retain observed IDs.
	return out
}
