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
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// FederationRule decides which federated tokens are accepted, and which service account
// they act as.
type FederationRule struct {
	rest.Resource[FederationRuleArgs, FederationRuleState, *FederationRuleState]
}

// NewFederationRule returns the FederationRule resource controller.
func NewFederationRule() *FederationRule {
	return &FederationRule{rest.Resource[FederationRuleArgs, FederationRuleState, *FederationRuleState]{
		Collection: federationRuleCollection,
		EndOfLife:  rest.Archive,
	}}
}

func (f *FederationRule) Annotate(a infer.Annotator) {
	a.Describe(f, "A workload identity federation rule: tokens from an issuer that satisfy the "+
		"rule's `match` are exchanged for access tokens that act as the rule's target service "+
		"account, with the rule's OAuth scope.\n\n"+
		"A rule applies either to all workspaces (`appliesToAllWorkspaces`) or to the workspace "+
		"it is created in (`workspaceId`); bind it to further workspaces with "+
		"`FederationRuleWorkspace`.\n\n"+
		"Managing federation requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`).\n\n"+
		"The API cannot delete rules, so deleting this resource archives the rule.")
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
	a.Describe(&f.Name, "A lowercase slug that uniquely names the rule in the organization.")
	a.Describe(&f.Description, "A description of the rule, up to 2,000 characters.")
	a.Describe(&f.OauthScope, "The space-separated OAuth scopes of the access tokens the rule "+
		"issues, such as `workspace:developer`.")
	a.Describe(&f.Target, "The service account the rule's access tokens act as.")
	a.Describe(&f.Match, "The conditions a token must satisfy. At least one of `subjectPrefix`, "+
		"`claims`, or `condition` is required.")
	a.Describe(&f.AppliesToAllWorkspaces, "Whether the rule applies to every workspace. "+
		"Otherwise `workspaceId` is required.")
	a.Describe(&f.WorkspaceID, "The workspace the rule is created in, required unless "+
		"`appliesToAllWorkspaces` is set. Changing it replaces the rule; to bind the rule to "+
		"further workspaces, use `FederationRuleWorkspace`.")
	a.Describe(&f.TokenLifetimeSeconds, "The lifetime, in seconds, of the access tokens the rule "+
		"issues, between 60 and 86,400. Defaults to 3,600.")
}

type FederationRuleState struct {
	FederationRuleArgs
	IssuerName   string   `pulumi:"issuerName"`
	WorkspaceIDs []string `pulumi:"workspaceIds,optional"`
	CreatedAt    string   `pulumi:"createdAt"`
	UpdatedAt    string   `pulumi:"updatedAt"`
}

func (f *FederationRuleState) Inputs() *FederationRuleArgs { return &f.FederationRuleArgs }

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

func (FederationTarget) ExtendWire(obj map[string]any) { obj["type"] = "service_account" }

func (f *FederationTarget) Annotate(a infer.Annotator) {
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
	a.Describe(&f.Audience, "The required `aud` claim.")
	a.Describe(&f.Claims, "Claims the token must carry, with exactly these values.")
	a.Describe(&f.Condition, "A CEL expression over the token's claims that must evaluate to "+
		"true, up to 4,096 characters.")
	a.Describe(&f.SubjectPrefix, "A prefix the `sub` claim must start with, up to 1,024 "+
		"characters. A bare `*` is not accepted.")
}
