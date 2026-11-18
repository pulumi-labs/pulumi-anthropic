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
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// FederationRule decides which federated tokens are accepted, and which service account
// they act as.
type FederationRule struct{}

// NewFederationRule returns the complete FederationRule resource.
func NewFederationRule() infer.InferredResource {
	return federationRuleResource{infer.Resource(&FederationRule{})}
}

func (f *FederationRule) Annotate(a infer.Annotator) {
	a.Describe(f, "A workload identity federation rule: tokens from an issuer that satisfy the "+
		"rule's `match` are exchanged for access tokens that act as the rule's target service "+
		"account, with the rule's OAuth scope.\n\n"+
		"A rule applies either to all workspaces (`appliesToAllWorkspaces`) or to the workspace "+
		"it is created in (`workspaceId`); bind it to further workspaces with "+
		"`FederationRuleWorkspace`.\n\n"+
		"Managing federation requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`); Admin API keys are not accepted. OAuth callers can configure only `workspace:developer` or `workspace:inference` scopes; higher-privilege rules require the Console. Creating a rule does not verify token exchange.\n\n"+
		"Deleting this resource archives the rule, preventing new exchanges but not revoking tokens already issued. "+
		"Changing the issuer or creation workspace replaces the rule by archiving it before creating its replacement; this interrupts new exchanges. "+
		"Optional settings omitted from the start remain unconfigured, preserving later external changes. "+
		"Import by rule ID recovers current settings but cannot recover the original creation workspace; configure workspace bindings separately with FederationRuleWorkspace.")
}

type FederationRuleArgs struct {
	IssuerID               string           `pulumi:"issuerId"                        provider:"replaceOnChanges"`
	Name                   string           `pulumi:"name"`
	Description            *string          `pulumi:"description,optional"`
	OauthScope             string           `pulumi:"oauthScope"`
	Target                 FederationTarget `pulumi:"target"`
	Match                  FederationMatch  `pulumi:"match"`
	AppliesToAllWorkspaces *bool            `pulumi:"appliesToAllWorkspaces,optional"`
	WorkspaceID            *string          `pulumi:"workspaceId,optional"            provider:"replaceOnChanges"`
	TokenLifetimeSeconds   *int             `pulumi:"tokenLifetimeSeconds,optional"`
}

func (f *FederationRuleArgs) Annotate(a infer.Annotator) {
	a.Describe(&f.IssuerID, "The ID of the issuer whose tokens the rule accepts. Changing it "+
		"replaces the rule.")
	a.Describe(&f.Name, "A slug of 1 to 255 lowercase letters, digits or hyphens that uniquely names the rule in the organization. Renaming updates the existing rule.")
	a.Describe(&f.Description, "A description of the rule, up to 2,000 characters. Removing a previously configured description clears it.")
	a.Describe(&f.OauthScope, "The space-separated OAuth scopes of the access tokens the rule "+
		"issues, such as `workspace:developer`.")
	a.Describe(&f.Target, "The service account the rule's access tokens act as.")
	a.Describe(&f.Match, "The conditions a token must satisfy. At least one of `subjectPrefix`, "+
		"`claims`, or `condition` is required.")
	a.Describe(&f.AppliesToAllWorkspaces, "Whether the rule applies to every workspace. "+
		"Otherwise `workspaceId` is required at creation. Removing a configured value restores false; this is rejected if no enabled workspace remains.")
	a.Describe(&f.WorkspaceID, "The workspace the rule is created in, required unless "+
		"`appliesToAllWorkspaces` is set. Changing it replaces the rule; to bind the rule to "+
		"further workspaces, use `FederationRuleWorkspace`.")
	a.Describe(&f.TokenLifetimeSeconds, "The lifetime, in seconds, of the access tokens the rule "+
		"issues, between 60 and 86,400. Defaults to 3,600; removing a configured value restores 3,600.")
}

type FederationRuleState struct {
	FederationRuleArgs
	IssuerName   string   `pulumi:"issuerName"`
	WorkspaceIDs []string `pulumi:"workspaceIds,optional"`
	CreatedAt    string   `pulumi:"createdAt"`
	UpdatedAt    string   `pulumi:"updatedAt"`
}

func (f *FederationRuleState) Annotate(a infer.Annotator) {
	a.Describe(&f.IssuerName, "The name of the rule's issuer.")
	a.Describe(&f.WorkspaceIDs, "Every workspace the rule is bound to.")
	a.Describe(&f.CreatedAt, "When the rule was created, in RFC 3339 format.")
	a.Describe(&f.UpdatedAt, "When the rule was last updated, in RFC 3339 format.")
}

// FederationTarget is the service account a federation rule's access tokens act as.
type FederationTarget struct {
	ServiceAccountID string `pulumi:"serviceAccountId"`
}

func (f *FederationTarget) Annotate(a infer.Annotator) {
	a.Describe(f, "The service account that tokens issued by a federation rule act as.")
	a.Describe(&f.ServiceAccountID, "The service account's ID.")
}

// FederationMatch is the set of conditions a federated token must satisfy.
type FederationMatch struct {
	Audience      *string           `pulumi:"audience,optional"`
	Claims        map[string]string `pulumi:"claims,optional"`
	Condition     *string           `pulumi:"condition,optional"`
	SubjectPrefix *string           `pulumi:"subjectPrefix,optional"`
}

func (f *FederationMatch) Annotate(a infer.Annotator) {
	a.Describe(f, "Conditions on verified identity tokens; every configured condition must pass. Removing a configured condition or claim stops requiring it, so review matcher edits carefully. At least one subject, claim or CEL condition is required; audience alone is insufficient.")
	a.Describe(&f.Audience, "The required `aud` claim.")
	a.Describe(&f.Claims, "Claims the token must carry, with exactly these values.")
	a.Describe(&f.Condition, "A CEL expression over the token's claims that must evaluate to "+
		"true, up to 4,096 characters.")
	a.Describe(&f.SubjectPrefix, "Matches the `sub` claim exactly unless it ends in `*`, which enables prefix matching. Up to 1,024 characters; a bare `*` is not accepted. No wildcard is added automatically.")
}

func (controller *FederationRule) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[FederationRuleArgs], error) {
	in, failures, err := infer.DefaultCheck[FederationRuleArgs](ctx, req.NewInputs)
	fail := func(k, s string) { failures = append(failures, p.CheckFailure{Property: k, Reason: s}) }
	m := req.NewInputs
	if v := m.Get("name"); v.IsString() && !regexp.MustCompile(`^[a-z0-9-]{1,255}$`).MatchString(v.AsString()) {
		fail("name", "must contain 1–255 lowercase letters, digits or hyphens")
	}
	for _, k := range []string{"issuerId", "oauthScope", "workspaceId"} {
		if v := m.Get(k); v.IsString() && v.AsString() == "" {
			fail(k, "must not be empty")
		}
	}
	if v := m.Get("description"); v.IsString() && utf8.RuneCountInString(v.AsString()) > 2000 {
		fail("description", "must not exceed 2000 characters")
	}
	if v := m.Get("tokenLifetimeSeconds"); v.IsNumber() && (v.AsNumber() < 60 || v.AsNumber() > 86400 || math.Trunc(v.AsNumber()) != v.AsNumber()) {
		fail("tokenLifetimeSeconds", "must be an integer between 60 and 86400")
	}
	if v := m.Get("target"); v.IsMap() {
		if x := v.AsMap().Get("serviceAccountId"); x.IsString() && x.AsString() == "" {
			fail("target.serviceAccountId", "must not be empty")
		}
	}
	if v := m.Get("match"); v.IsMap() {
		mm := v.AsMap()
		possible := false
		for _, k := range []string{"subjectPrefix", "condition", "claims"} {
			x := mm.Get(k)
			if x.IsComputed() || (x.IsString() && x.AsString() != "") || (x.IsMap() && x.AsMap().Len() > 0) {
				possible = true
			}
		}
		if !possible {
			fail("match", "at least one subjectPrefix, claims or condition is required; audience alone is insufficient")
		}
		for k, max := range map[string]int{"subjectPrefix": 1024, "condition": 4096} {
			if x := mm.Get(k); x.IsString() && (utf8.RuneCountInString(x.AsString()) > max || (k == "subjectPrefix" && x.AsString() == "*")) {
				fail("match."+k, "invalid matcher length or wildcard-only subject")
			}
		}
	}
	// Creation requires a workspace or all-workspaces. Read/import cannot recover
	// the original creation workspace from the GA membership list; enforce at Create.
	return infer.CheckResponse[FederationRuleArgs]{Inputs: in, Failures: failures}, err
}

func (controller *FederationRule) Create(ctx context.Context, req infer.CreateRequest[FederationRuleArgs]) (infer.CreateResponse[FederationRuleState], error) {
	if req.DryRun {
		// A non-nil observation placeholder lets infer mark this optional list unknown.
		return infer.CreateResponse[FederationRuleState]{Output: FederationRuleState{FederationRuleArgs: req.Inputs, WorkspaceIDs: []string{}}}, nil
	}
	in := req.Inputs
	if in.WorkspaceID == nil && (in.AppliesToAllWorkspaces == nil || !*in.AppliesToAllWorkspaces) {
		return infer.CreateResponse[FederationRuleState]{}, errors.New("workspaceId is required at creation unless appliesToAllWorkspaces is true")
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.CreateResponse[FederationRuleState]{}, err
	}
	u := ruleUpdate(in, FederationRuleState{})
	params := sdk.OrganizationFederationRuleNewParams{IssuerID: in.IssuerID, Name: in.Name, OAuthScope: in.OauthScope, Match: u.Match, Target: u.Target, Description: u.Description, AppliesToAllWorkspaces: u.AppliesToAllWorkspaces, TokenLifetimeSeconds: u.TokenLifetimeSeconds}
	if in.WorkspaceID != nil {
		params.WorkspaceID = sdk.String(*in.WorkspaceID)
	}
	r, err := c.Organization.Federation.Rules.New(ctx, params)
	if err != nil {
		return infer.CreateResponse[FederationRuleState]{}, err
	}
	out, id, err := ruleObserved(r, "", in, false)
	if err != nil {
		// The API allocated this rule, but no usable state can be returned.
		// Compensate only a trustworthy identity returned by this create.
		if ruleIdentity(r, "") {
			_, cleanupErr := c.Organization.Federation.Rules.Archive(context.WithoutCancel(ctx), r.ID)
			if cleanupErr != nil && !apiutil.IsNotFound(cleanupErr) {
				err = errors.Join(err, fmt.Errorf("could not archive newly created federation rule %s: %w", r.ID, cleanupErr))
			}
		}
		return infer.CreateResponse[FederationRuleState]{}, err
	}
	return infer.CreateResponse[FederationRuleState]{ID: id, Output: out}, err
}

func (controller *FederationRule) Read(ctx context.Context, req infer.ReadRequest[FederationRuleArgs, FederationRuleState]) (infer.ReadResponse[FederationRuleArgs, FederationRuleState], error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{}, err
	}
	r, err := c.Organization.Federation.Rules.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{}, err
	}
	if ruleIdentity(r, req.ID) && r.JSON.ArchivedAt.Raw() != "" && r.JSON.ArchivedAt.Raw() != "null" && !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{}, nil
	}
	out, id, err := ruleObserved(r, req.ID, req.Inputs, req.Inputs.Name == "")
	if err != nil {
		return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{}, err
	}
	return infer.ReadResponse[FederationRuleArgs, FederationRuleState]{ID: id, Inputs: out.FederationRuleArgs, State: out}, err
}

func (controller *FederationRule) Update(ctx context.Context, req infer.UpdateRequest[FederationRuleArgs, FederationRuleState]) (infer.UpdateResponse[FederationRuleState], error) {
	if req.DryRun {
		out := req.State
		out.FederationRuleArgs = req.Inputs
		return infer.UpdateResponse[FederationRuleState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.UpdateResponse[FederationRuleState]{}, err
	}
	r, err := c.Organization.Federation.Rules.Update(ctx, req.ID, ruleUpdate(req.Inputs, req.State))
	if err != nil {
		return infer.UpdateResponse[FederationRuleState]{}, err
	}
	out, _, err := ruleObserved(r, req.ID, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[FederationRuleState]{}, err
	}
	return infer.UpdateResponse[FederationRuleState]{Output: out}, err
}

func (controller *FederationRule) Delete(ctx context.Context, req infer.DeleteRequest[FederationRuleState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.OrgAdminOAuth)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Federation.Rules.Archive(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve claim normalization and logical input metadata.
type federationRuleResource struct{ infer.InferredResource }

func (r federationRuleResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	req.Inputs = ruleNormalizeClaims(req.Inputs)
	out, err := infer.Resource(&FederationRule{}).Check(ctx, req)
	if err == nil && len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

func (r federationRuleResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	req.State = ruleClaimsComparison(req.State)
	req.OldInputs = ruleClaimsComparison(req.OldInputs)
	req.Inputs = ruleClaimsComparison(req.Inputs)
	out, err := infer.Resource(&FederationRule{}).Diff(ctx, req)
	if out.HasChanges {
		for _, v := range out.DetailedDiff {
			if v.Kind == p.UpdateReplace || v.Kind == p.AddReplace || v.Kind == p.DeleteReplace {
				out.DeleteBeforeReplace = true
			}
		}
	}
	return out, err
}

func (r federationRuleResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&FederationRule{}).Create(ctx, req)
}

func (r federationRuleResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&FederationRule{}).Read(ctx, req)
}

func (r federationRuleResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&FederationRule{}).Update(ctx, req)
}

func (r federationRuleResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&FederationRule{}).Delete(ctx, req)
}

// The SDK omits both nil and empty claims. Canonicalize omission to an empty
// map, rather than deleting empty maps: metadata remains on the same logical
// value/path, including secret and dependency-bearing empty maps. Unknown and
// nonempty maps are untouched, as are metadata on the enclosing match object.
func ruleNormalizeClaims(m property.Map) property.Map {
	match := m.Get("match")
	if !match.IsMap() {
		return m
	}
	claims := match.AsMap().Get("claims")
	if !claims.IsNull() {
		return m
	}
	normalized := match.AsMap().Set("claims", property.WithGoValue(claims, property.Map{}))
	return m.Set("match", property.WithGoValue(match, normalized))
}

// Diff has no value output. Compare canonical empty claims without treating
// metadata on the empty map (or enclosing matcher) as an API change. This is a
// comparison-only copy: Check and caller bags retain all input metadata.
// Unknown and nonempty claims keep their original representation.
func ruleClaimsComparison(m property.Map) property.Map {
	m = ruleNormalizeClaims(m)
	match := m.Get("match")
	if !match.IsMap() {
		return m
	}
	claims := match.AsMap().Get("claims")
	if !claims.IsMap() || claims.AsMap().Len() != 0 {
		return m
	}
	comparison := match.AsMap().Set("claims", claims.WithSecret(false).WithDependencies(nil))
	return m.Set("match", property.New(comparison))
}

func ruleMatch(in FederationMatch) sdk.FederationRuleMatchParam {
	out := sdk.FederationRuleMatchParam{Claims: in.Claims}
	if in.Audience != nil {
		out.Audience = sdk.String(*in.Audience)
	}
	if in.Condition != nil {
		out.Condition = sdk.String(*in.Condition)
	}
	if in.SubjectPrefix != nil {
		out.SubjectPrefix = sdk.String(*in.SubjectPrefix)
	}
	return out
}

func ruleUpdate(in FederationRuleArgs, old FederationRuleState) sdk.OrganizationFederationRuleUpdateParams {
	out := sdk.OrganizationFederationRuleUpdateParams{Name: sdk.String(in.Name), OAuthScope: sdk.String(in.OauthScope), Match: ruleMatch(in.Match), Target: sdk.ServiceAccountTargetParam{ServiceAccountID: in.Target.ServiceAccountID}}
	if in.Description != nil {
		out.Description = sdk.String(*in.Description)
	} else if old.Description != nil {
		out.Description = param.Null[string]()
	}
	if in.AppliesToAllWorkspaces != nil {
		out.AppliesToAllWorkspaces = sdk.Bool(*in.AppliesToAllWorkspaces)
	} else if old.AppliesToAllWorkspaces != nil {
		out.AppliesToAllWorkspaces = sdk.Bool(false)
	}
	if in.TokenLifetimeSeconds != nil {
		out.TokenLifetimeSeconds = sdk.Int(int64(*in.TokenLifetimeSeconds))
	} else if old.TokenLifetimeSeconds != nil {
		out.TokenLifetimeSeconds = sdk.Int(3600)
	}
	return out
}

func ruleIdentity(r *sdk.FederationRule, expected string) bool {
	return r != nil && apiutil.JSONString(r.JSON.ID.Raw()) && r.ID != "" && (expected == "" || expected == r.ID) && (r.JSON.Type.Raw() == "" || r.JSON.Type.Raw() == `"federation_rule"`)
}

func ruleObserved(r *sdk.FederationRule, expected string, accepted FederationRuleArgs, importing bool) (FederationRuleState, string, error) {
	// The creation workspace is write-only; membership is reported separately.
	out := FederationRuleState{FederationRuleArgs: FederationRuleArgs{WorkspaceID: accepted.WorkspaceID}}
	if !ruleIdentity(r, expected) {
		return out, "", errors.New("invalid federation rule identity or type")
	}
	var invalid error
	bad := func(k string) { invalid = errors.Join(invalid, fmt.Errorf("invalid or missing rule %s", k)) }
	for _, v := range []struct {
		k, raw, s string
		dest      *string
	}{{"issuerId", r.JSON.IssuerID.Raw(), r.IssuerID, &out.IssuerID}, {"name", r.JSON.Name.Raw(), r.Name, &out.Name}, {"oauthScope", r.JSON.OAuthScope.Raw(), r.OAuthScope, &out.OauthScope}} {
		if apiutil.JSONString(v.raw) && v.s != "" {
			*v.dest = v.s
		} else {
			bad(v.k)
		}
	}
	// The SDK declares a display string, not a nonempty identifier.
	// The API can return null for this display field; the SDK maps it to "".
	if raw := r.JSON.IssuerName.Raw(); apiutil.JSONString(raw) || raw == "null" {
		out.IssuerName = r.IssuerName
	} else {
		shape := "non-string"
		if raw == "" {
			shape = "absent"
		}
		invalid = errors.Join(invalid, fmt.Errorf("invalid rule issuerName: expected a string or null, got %s", shape))
	}
	optional := func(raw, s string, valid *bool) *string {
		if raw == "" || raw == "null" {
			return nil
		}
		if !apiutil.JSONString(raw) {
			*valid = false
			return nil
		}
		return &s
	}
	if importing || accepted.Description != nil {
		if r.JSON.Description.Raw() == "null" {
			out.Description = nil
		} else if apiutil.JSONString(r.JSON.Description.Raw()) {
			v := r.Description
			out.Description = &v
		} else {
			bad("description")
		}
	}
	if importing || accepted.AppliesToAllWorkspaces != nil {
		if apiutil.JSONBool(r.JSON.AppliesToAllWorkspaces.Raw()) {
			v := r.AppliesToAllWorkspaces
			out.AppliesToAllWorkspaces = &v
		} else {
			bad("appliesToAllWorkspaces")
		}
	}
	if importing || accepted.TokenLifetimeSeconds != nil {
		if apiutil.JSONInt64(r.JSON.TokenLifetimeSeconds.Raw()) && r.TokenLifetimeSeconds >= 60 && r.TokenLifetimeSeconds <= 86400 {
			v := int(r.TokenLifetimeSeconds)
			out.TokenLifetimeSeconds = &v
		} else {
			bad("tokenLifetimeSeconds")
		}
	}
	if r.Target.Type == "service_account" && apiutil.JSONString(r.Target.JSON.ServiceAccountID.Raw()) && r.Target.ServiceAccountID != "" {
		out.Target = FederationTarget{ServiceAccountID: r.Target.ServiceAccountID}
	} else {
		bad("target")
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(r.JSON.Match.Raw()), &obj) == nil && obj != nil {
		valid := true
		mm := FederationMatch{Audience: optional(r.Match.JSON.Audience.Raw(), r.Match.Audience, &valid), Condition: optional(r.Match.JSON.Condition.Raw(), r.Match.Condition, &valid), SubjectPrefix: optional(r.Match.JSON.SubjectPrefix.Raw(), r.Match.SubjectPrefix, &valid)}
		raw := r.Match.JSON.Claims.Raw()
		if raw != "" && raw != "null" {
			var cm map[string]json.RawMessage
			if json.Unmarshal([]byte(raw), &cm) != nil || cm == nil {
				valid = false
			} else {
				for _, v := range cm {
					if !apiutil.JSONString(string(v)) {
						valid = false
					}
				}
			}
			mm.Claims = r.Match.Claims
		}
		if valid {
			out.Match = mm
		} else {
			bad("match")
		}
	} else {
		bad("match")
	}
	var ids []string
	raw := r.JSON.WorkspaceIDs.Raw()
	if raw != "" && raw != "null" && json.Unmarshal([]byte(raw), &ids) == nil {
		valid := true
		var values []json.RawMessage
		_ = json.Unmarshal([]byte(raw), &values)
		for _, v := range values {
			if !apiutil.JSONString(string(v)) {
				valid = false
			}
		}
		if valid {
			out.WorkspaceIDs = ids
		} else {
			bad("workspaceIds")
		}
	} else {
		bad("workspaceIds")
	}
	for _, v := range []struct {
		k, raw string
		t      time.Time
		dest   *string
	}{{"createdAt", r.JSON.CreatedAt.Raw(), r.CreatedAt, &out.CreatedAt}, {"updatedAt", r.JSON.UpdatedAt.Raw(), r.UpdatedAt, &out.UpdatedAt}} {
		if apiutil.JSONString(v.raw) && !v.t.IsZero() {
			*v.dest = v.t.Format(time.RFC3339Nano)
		} else {
			bad(v.k)
		}
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "" && raw != "null" {
		bad("archivedAt")
	}
	return out, r.ID, invalid
}
