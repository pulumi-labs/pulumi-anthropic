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

package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// UserProfile is an entity a platform serves through the API, for request attribution.
type UserProfile struct{ operation *profileOperationState }

// NewUserProfile returns the complete UserProfile resource.
func NewUserProfile() infer.InferredResource {
	return userProfileResource{infer.Resource(&UserProfile{})}
}

func (u *UserProfile) Annotate(a infer.Annotator) {
	a.Describe(u, "A user profile: an entity your platform serves through the Anthropic API, "+
		"such as an end user of your product or a company you resell Claude access to. Send "+
		"its ID in the `anthropic-user-profile-id` header of Messages, Message Batches, and "+
		"token counting requests to attribute them to the entity.\n\n"+
		"Deleting this resource deletes the profile. Anthropic keeps the trust and safety "+
		"records attached to the profile's ID.")
}

type UserProfileArgs struct {
	Name                    *string           `pulumi:"name,optional"`
	AccessType              *AccessType       `pulumi:"accessType,optional"`
	ExternalID              *string           `pulumi:"externalId,optional"`
	ExternalUserOnboardedAt *string           `pulumi:"externalUserOnboardedAt,optional"`
	Metadata                map[string]string `pulumi:"metadata,optional"`
}

func (u *UserProfileArgs) Annotate(a infer.Annotator) {
	a.Describe(&u.Name, "The entity's name, up to 255 characters. Removing a name you previously configured replaces the profile and changes its attribution ID.")
	a.Describe(&u.AccessType, "How your platform uses the API on the entity's behalf. Defaults "+
		"to `application`. Removing this setting resets it to `application`.")
	a.Describe(&u.ExternalID, "Your platform's own non-unique identifier for the entity, up to 255 characters. Removing an identifier you previously configured replaces the profile and changes its attribution ID.")
	a.Describe(&u.ExternalUserOnboardedAt, "When the entity joined your platform, in RFC 3339 "+
		"format, at most one minute in the future. Once set, it cannot be cleared in place: removing it replaces the profile and changes its attribution ID.")
	a.Describe(&u.Metadata, "Up to 16 key-value pairs, keys up to 64 characters and nonempty values up to 512 characters. Removing an entry from your configuration deletes that entry.")
}

type UserProfileState struct {
	UserProfileArgs
	TrustGrants map[string]TrustGrant `pulumi:"trustGrants,optional"`
	CreatedAt   string                `pulumi:"createdAt"`
	UpdatedAt   string                `pulumi:"updatedAt"`
}

func (s *UserProfileState) Annotate(a infer.Annotator) {
	a.Describe(&s.TrustGrants, "The profile's active or pending trust grants, by grant name.")
	a.Describe(&s.CreatedAt, "When the profile was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the profile was last updated, in RFC 3339 format.")
}

// AccessType is how a platform uses the API on a profile's behalf.
type AccessType string

func (AccessType) Values() []infer.EnumValue[AccessType] {
	return []infer.EnumValue[AccessType]{
		{Value: "application", Description: "Your product uses the API behind the scenes, and " +
			"the profile is one of its end users."},
		{Value: "passthrough", Description: "You resell raw inference, and the profile is the " +
			"company you resell to."},
	}
}

// TrustGrant is the status of a profile's trust grant.
type TrustGrant struct {
	Status string `pulumi:"status"`
}

func (g *TrustGrant) Annotate(a infer.Annotator) {
	a.Describe(&g.Status, "The grant's status: `active`, `pending`, or `rejected`.")
}

func (controller *UserProfile) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[UserProfileArgs], error) {
	a, failures, err := infer.DefaultCheck[UserProfileArgs](ctx, req.NewInputs)
	fail := func(k, s string) { failures = append(failures, p.CheckFailure{Property: k, Reason: s}) }
	for _, k := range []string{"name", "externalId"} {
		v := req.NewInputs.Get(k)
		if v.IsString() && utf8.RuneCountInString(v.AsString()) > 255 {
			fail(k, "must contain at most 255 characters")
		}
	}
	if v := req.NewInputs.Get("accessType"); v.IsString() && v.AsString() != "application" && v.AsString() != "passthrough" {
		fail("accessType", "must be application or passthrough")
	}
	if v := req.NewInputs.Get("externalUserOnboardedAt"); v.IsString() {
		t, e := time.Parse(time.RFC3339Nano, v.AsString())
		if e != nil {
			fail("externalUserOnboardedAt", "must be a complete RFC3339 timestamp")
		} else if t.After(time.Now().Add(time.Minute)) {
			fail("externalUserOnboardedAt", "must not be more than one minute in the future")
		}
	}
	if v := req.NewInputs.Get("metadata"); v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 pairs")
		}
		for k, v := range v.AsMap().All {
			if utf8.RuneCountInString(k) > 64 {
				fail("metadata", "keys must contain at most 64 characters")
			}
			if v.IsString() && (v.AsString() == "" || utf8.RuneCountInString(v.AsString()) > 512) {
				fail("metadata", "values must contain 1–512 characters; remove a key to delete it")
			}
		}
	}
	return infer.CheckResponse[UserProfileArgs]{Inputs: a, Failures: failures}, err
}

func (controller *UserProfile) Create(ctx context.Context, req infer.CreateRequest[UserProfileArgs]) (infer.CreateResponse[UserProfileState], error) {
	if req.DryRun {
		return infer.CreateResponse[UserProfileState]{Output: UserProfileState{UserProfileArgs: req.Inputs}}, nil
	}
	n, err := profileNew(req.Inputs)
	if err != nil {
		return infer.CreateResponse[UserProfileState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[UserProfileState]{}, err
	}
	r, err := c.Beta.UserProfiles.New(ctx, n)
	if err != nil {
		return infer.CreateResponse[UserProfileState]{}, err
	}
	s, err := profileState(r)
	if err != nil {
		return infer.CreateResponse[UserProfileState]{}, err
	}
	return infer.CreateResponse[UserProfileState]{ID: r.ID, Output: s}, nil
}

func (controller *UserProfile) Read(ctx context.Context, req infer.ReadRequest[UserProfileArgs, UserProfileState]) (infer.ReadResponse[UserProfileArgs, UserProfileState], error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[UserProfileArgs, UserProfileState]{}, err
	}
	r, err := c.Beta.UserProfiles.Get(ctx, req.ID, anthropic.BetaUserProfileGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[UserProfileArgs, UserProfileState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[UserProfileArgs, UserProfileState]{}, err
	}
	s, err := profileState(r)
	if r != nil && r.ID != req.ID {
		err = fmt.Errorf("profile response identity mismatch")
	}
	if err != nil {
		return infer.ReadResponse[UserProfileArgs, UserProfileState]{}, err
	}
	inputs := s.UserProfileArgs
	if controller.operation != nil && controller.operation.managedRead {
		inputs = profileOwned(s, req.Inputs).UserProfileArgs
	}
	return infer.ReadResponse[UserProfileArgs, UserProfileState]{ID: req.ID, Inputs: inputs, State: s}, nil
}

func (controller *UserProfile) Update(ctx context.Context, req infer.UpdateRequest[UserProfileArgs, UserProfileState]) (infer.UpdateResponse[UserProfileState], error) {
	if req.DryRun {
		return infer.UpdateResponse[UserProfileState]{Output: UserProfileState{UserProfileArgs: req.Inputs}}, nil
	}
	// The engine's checked old inputs establish ownership; output observations
	// alone cannot authorize deleting external metadata or resetting access type.
	managed := UserProfileArgs{}
	if operation := controller.operation; operation != nil {
		bag := operation.oldInputs
		if !bag.Get("accessType").IsNull() {
			v := AccessType("application")
			managed.AccessType = &v
		}
		if m := bag.Get("metadata"); m.IsMap() {
			managed.Metadata = map[string]string{}
			for k := range m.AsMap().All {
				managed.Metadata[k] = ""
			}
		}
	}
	n, err := profileUpdate(managed, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[UserProfileState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[UserProfileState]{}, err
	}
	r, err := c.Beta.UserProfiles.Update(ctx, req.ID, n)
	if err != nil {
		return infer.UpdateResponse[UserProfileState]{}, err
	}
	s, err := profileState(r)
	if r != nil && (!r.JSON.ID.Valid() || r.ID != req.ID) {
		return infer.UpdateResponse[UserProfileState]{}, fmt.Errorf("profile response identity mismatch")
	}
	if req.State.CreatedAt != "" && s.CreatedAt != "" && req.State.CreatedAt != s.CreatedAt {
		err = fmt.Errorf("profile response changed immutable creation time")
	}
	if err != nil {
		return infer.UpdateResponse[UserProfileState]{}, err
	}
	return infer.UpdateResponse[UserProfileState]{Output: s}, nil
}

func (controller *UserProfile) Delete(ctx context.Context, req infer.DeleteRequest[UserProfileState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.UserProfiles.Delete(ctx, req.ID, anthropic.BetaUserProfileDeleteParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// No normalization or defaults: retain the original validated metadata-bearing bag.
type userProfileResource struct{ infer.InferredResource }

// profileOperationState belongs to one lifecycle invocation, never to the registered resource.
type profileOperationState struct {
	oldInputs   property.Map
	managedRead bool
}

func (r userProfileResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&UserProfile{operation: &profileOperationState{}}).Check(ctx, req)
	if err == nil && len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

func (r userProfileResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	// Observed optional defaults are not removal permission. Compare only managed
	// fields, retaining actual observed values for those fields (including drift).
	for _, k := range []string{"name", "accessType", "externalId", "externalUserOnboardedAt", "metadata"} {
		if req.Inputs.Get(k).IsNull() && req.OldInputs.Get(k).IsNull() {
			req.State = req.State.Delete(k)
		}
	}
	// SDK responses canonicalize timestamp spellings; compare instants.
	canonical := func(v property.Value) property.Value {
		if v.IsString() {
			if t, e := time.Parse(time.RFC3339Nano, v.AsString()); e == nil {
				return property.New(t.UTC().Format(time.RFC3339Nano)).WithSecret(v.Secret()).WithDependencies(v.Dependencies())
			}
		}
		return v
	}
	req.Inputs = req.Inputs.Set("externalUserOnboardedAt", canonical(req.Inputs.Get("externalUserOnboardedAt")))
	req.State = req.State.Set("externalUserOnboardedAt", canonical(req.State.Get("externalUserOnboardedAt")))
	req.OldInputs = req.OldInputs.Set("externalUserOnboardedAt", canonical(req.OldInputs.Get("externalUserOnboardedAt")))
	out, err := infer.Resource(&UserProfile{operation: &profileOperationState{}}).Diff(ctx, req)
	if err != nil {
		return out, err
	}
	// These API fields have no documented clear operation. Removal deliberately
	// allocates a new attribution identity, rather than sending ineffective nulls.
	for _, key := range []string{"name", "externalId", "externalUserOnboardedAt"} {
		if !slices.Contains(req.IgnoreChanges, key) && !req.State.Get(key).IsNull() && req.Inputs.Get(key).IsNull() {
			out.HasChanges = true
			if out.DetailedDiff == nil {
				out.DetailedDiff = map[string]p.PropertyDiff{}
			}
			out.DetailedDiff[key] = p.PropertyDiff{Kind: p.DeleteReplace, InputDiff: true}
		}
	}
	return out, nil
}

func (r userProfileResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&UserProfile{operation: &profileOperationState{}}).Create(ctx, req)
}

func (r userProfileResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	// Only a true ID-only request adopts every readable input. Retain the old
	// output bag so Read encoding preserves user-applied secrecy on server outputs.
	operation := &profileOperationState{managedRead: req.Properties.Len() != 0 || req.Inputs.Len() != 0}
	resp, err := infer.Resource(&UserProfile{operation: operation}).Read(ctx, req)
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

func (r userProfileResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	operation := &profileOperationState{oldInputs: req.OldInputs}
	return infer.Resource(&UserProfile{operation: operation}).Update(ctx, req)
}

func (r userProfileResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&UserProfile{operation: &profileOperationState{}}).Delete(ctx, req)
}

func profileNew(a UserProfileArgs) (anthropic.BetaUserProfileNewParams, error) {
	r := anthropic.BetaUserProfileNewParams{Metadata: maps.Clone(a.Metadata)}
	if a.Name != nil {
		r.Name = anthropic.String(*a.Name)
	}
	if a.ExternalID != nil {
		r.ExternalID = anthropic.String(*a.ExternalID)
	}
	if a.AccessType != nil {
		r.AccessType = anthropic.BetaUserProfileNewParamsAccessType(*a.AccessType)
	}
	if a.ExternalUserOnboardedAt != nil {
		t, e := time.Parse(time.RFC3339Nano, *a.ExternalUserOnboardedAt)
		if e != nil {
			return r, fmt.Errorf("invalid onboarding timestamp")
		}
		r.ExternalUserOnboardedAt = anthropic.Time(t)
	}
	return r, nil
}

func profileUpdate(old, a UserProfileArgs) (anthropic.BetaUserProfileUpdateParams, error) {
	n, err := profileNew(a)
	r := anthropic.BetaUserProfileUpdateParams{Name: n.Name, ExternalID: n.ExternalID, ExternalUserOnboardedAt: n.ExternalUserOnboardedAt, AccessType: anthropic.BetaUserProfileUpdateParamsAccessType(n.AccessType)}
	if a.AccessType == nil && old.AccessType != nil {
		r.AccessType = anthropic.BetaUserProfileUpdateParamsAccessTypeApplication
	}
	if old.Metadata != nil || a.Metadata != nil {
		r.Metadata = maps.Clone(a.Metadata)
		if r.Metadata == nil {
			r.Metadata = map[string]string{}
		}
		for k := range old.Metadata {
			if _, ok := a.Metadata[k]; !ok {
				r.Metadata[k] = ""
			}
		}
	}
	return r, err
}

// Validate individual raw JSON leaves: SDK Valid on a map alone does not prove
// its children decoded successfully.
func profileState(r *anthropic.BetaUserProfile) (UserProfileState, error) {
	s := UserProfileState{}
	if r == nil {
		return s, fmt.Errorf("missing profile response")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(r.RawJSON()), &raw) != nil {
		return s, fmt.Errorf("invalid profile response")
	}
	bad := false
	stringField := func(k string, nullable bool) *string {
		b, ok := raw[k]
		if !ok {
			bad = true
			return nil
		}
		if string(b) == "null" && nullable {
			return nil
		}
		var v string
		if string(b) == "null" || json.Unmarshal(b, &v) != nil {
			bad = true
			return nil
		}
		return &v
	}
	id := stringField("id", false)
	if id == nil || *id == "" {
		bad = true
	}
	typ := stringField("type", false)
	if typ == nil || *typ != "user_profile" {
		bad = true
	}
	s.Name = stringField("name", true)
	s.ExternalID = stringField("external_id", true)
	if v := stringField("access_type", false); v != nil {
		if *v != "application" && *v != "passthrough" {
			bad = true
		} else {
			a := AccessType(*v)
			s.AccessType = &a
		}
	}
	date := func(k string, nullable bool) *string {
		v := stringField(k, nullable)
		if v == nil {
			return nil
		}
		t, e := time.Parse(time.RFC3339Nano, *v)
		if e != nil {
			bad = true
			return nil
		}
		v2 := t.Format(time.RFC3339Nano)
		return &v2
	}
	s.ExternalUserOnboardedAt = date("external_user_onboarded_at", true)
	if v := date("created_at", false); v != nil {
		s.CreatedAt = *v
	}
	if v := date("updated_at", false); v != nil {
		s.UpdatedAt = *v
	}
	var metadata map[string]json.RawMessage
	if b := raw["metadata"]; len(b) == 0 || string(b) == "null" || json.Unmarshal(b, &metadata) != nil {
		bad = true
	} else {
		s.Metadata = map[string]string{}
		for k, b := range metadata {
			var v string
			if string(b) == "null" || json.Unmarshal(b, &v) != nil {
				bad = true
				continue
			}
			s.Metadata[k] = v
		}
	}
	var grants map[string]json.RawMessage
	if b := raw["trust_grants"]; len(b) == 0 || string(b) == "null" || json.Unmarshal(b, &grants) != nil {
		bad = true
	} else {
		s.TrustGrants = map[string]TrustGrant{}
		for k, b := range grants {
			var obj map[string]json.RawMessage
			var v string
			if json.Unmarshal(b, &obj) != nil || json.Unmarshal(obj["status"], &v) != nil || (v != "active" && v != "pending" && v != "rejected") {
				bad = true
				continue
			}
			s.TrustGrants[k] = TrustGrant{Status: v}
		}
	}
	if bad {
		return s, fmt.Errorf("incomplete or malformed profile response")
	}
	return s, nil
}

// Keep omitted optional inputs unmanaged; import (no prior observation) adopts all.
// Read validates the full response before applying ownership, so malformed
// unowned fields can never be hidden by this projection.
func profileOwned(s UserProfileState, a UserProfileArgs) UserProfileState {
	if a.Name == nil {
		s.Name = nil
	}
	if a.ExternalID == nil {
		s.ExternalID = nil
	}
	if a.AccessType == nil {
		s.AccessType = nil
	}
	if a.ExternalUserOnboardedAt == nil {
		s.ExternalUserOnboardedAt = nil
	}
	if a.Metadata == nil {
		s.Metadata = nil
	} else {
		owned := map[string]string{}
		for k := range a.Metadata {
			if v, ok := s.Metadata[k]; ok {
				owned[k] = v
			}
		}
		s.Metadata = owned
	}
	return s
}
