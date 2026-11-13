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

package functions

import (
	"context"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

// GetRateLimits reads the rate limits of the organization or of one workspace.
type GetRateLimits struct{}

func (f *GetRateLimits) Annotate(a infer.Annotator) {
	a.Describe(f, "Reads the rate limits that apply to the organization or, given a "+
		"`workspaceId`, to one workspace. Workspace results include inherited organization limits. "+
		"This lookup does not change limits; the platform also offers a workspace limit update API.")
}

type GetRateLimitsArgs struct {
	WorkspaceID *string `pulumi:"workspaceId,optional"`
}

func (r *GetRateLimitsArgs) Annotate(a infer.Annotator) {
	a.Describe(&r.WorkspaceID, "The workspace to read limits for. Omit for the organization's limits.")
}

type GetRateLimitsResult struct {
	RateLimits []RateLimit `pulumi:"rateLimits"`
}

func (r *GetRateLimitsResult) Annotate(a infer.Annotator) {
	a.Describe(&r.RateLimits, "The rate limits, one entry per group of models or API feature.")
}

// RateLimit is the set of limits that apply to one group of models or API feature.
type RateLimit struct {
	ID        string           `pulumi:"id"`
	GroupType string           `pulumi:"groupType"`
	Group     RateLimitGroup   `pulumi:"group"`
	Models    []string         `pulumi:"models"`
	Limits    []RateLimitValue `pulumi:"limits"`
}

func (r *RateLimit) Annotate(a infer.Annotator) {
	a.Describe(&r.ID, "The rate limit's ID.")
	a.Describe(&r.GroupType, "What the limits apply to, such as `model_group`, `batch`, or `web_search`.")
	a.Describe(&r.Group, "The group of models or API feature the limits apply to.")
	a.Describe(&r.Models, "The models the limits apply to.")
	a.Describe(&r.Limits, "The limits.")
}

// RateLimitGroup identifies what a rate limit applies to.
type RateLimitGroup struct {
	ID          string  `pulumi:"id"`
	Type        string  `pulumi:"type"`
	DisplayName *string `pulumi:"displayName,optional"`
}

func (g *RateLimitGroup) Annotate(a infer.Annotator) {
	a.Describe(&g.ID, "The group's ID.")
	a.Describe(&g.Type, "The group's type, matching the rate limit's `groupType`.")
	a.Describe(&g.DisplayName, "The group's human-readable name, for model groups.")
}

// RateLimitValue is one limit, such as requests or input tokens per minute.
type RateLimitValue struct {
	Type     string `pulumi:"type"`
	Value    int    `pulumi:"value"`
	OrgLimit *int   `pulumi:"orgLimit,optional"`
}

func (v *RateLimitValue) Annotate(a infer.Annotator) {
	a.Describe(&v.Type, "What is limited, such as `requests_per_minute`.")
	a.Describe(&v.Value, "The limit.")
	a.Describe(&v.OrgLimit, "For a workspace, the organization-wide limit that caps this one.")
}

func (*GetRateLimits) Invoke(
	ctx context.Context, req infer.FunctionRequest[GetRateLimitsArgs],
) (infer.FunctionResponse[GetRateLimitsResult], error) {
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.FunctionResponse[GetRateLimitsResult]{}, err
	}
	limits := []RateLimit{}
	if req.Input.WorkspaceID == nil {
		pages := client.Organization.RateLimits.ListAutoPaging(ctx, anthropic.OrganizationRateLimitListParams{})
		for pages.Next() {
			item := pages.Current()
			mapped := RateLimit{
				ID: item.ID, GroupType: item.Group.Type,
				Group:  RateLimitGroup{ID: item.Group.ID, Type: item.Group.Type},
				Models: append([]string{}, item.Models...), Limits: []RateLimitValue{},
			}
			if item.Group.JSON.DisplayName.Valid() {
				mapped.Group.DisplayName = &item.Group.DisplayName
			}
			for _, value := range item.Limits {
				mapped.Limits = append(mapped.Limits, RateLimitValue{Type: value.Type, Value: int(value.Value)})
			}
			limits = append(limits, mapped)
		}
		if err := pages.Err(); err != nil {
			return infer.FunctionResponse[GetRateLimitsResult]{}, err
		}
	} else {
		pages := client.Organization.Workspaces.RateLimits.ListAutoPaging(ctx, *req.Input.WorkspaceID,
			anthropic.OrganizationWorkspaceRateLimitListParams{IncludeInherited: param.NewOpt(true)})
		for pages.Next() {
			item := pages.Current()
			mapped := RateLimit{
				ID: item.RateLimitID, GroupType: item.Group.Type,
				Group:  RateLimitGroup{ID: item.Group.ID, Type: item.Group.Type},
				Models: append([]string{}, item.Models...), Limits: []RateLimitValue{},
			}
			if item.Group.JSON.DisplayName.Valid() {
				mapped.Group.DisplayName = &item.Group.DisplayName
			}
			for _, value := range item.Limits {
				entry := RateLimitValue{Type: value.Type, Value: int(value.Value)}
				if value.JSON.OrgLimit.Valid() {
					orgLimit := int(value.OrgLimit)
					entry.OrgLimit = &orgLimit
				}
				mapped.Limits = append(mapped.Limits, entry)
			}
			limits = append(limits, mapped)
		}
		if err := pages.Err(); err != nil {
			return infer.FunctionResponse[GetRateLimitsResult]{}, err
		}
	}
	return infer.FunctionResponse[GetRateLimitsResult]{Output: GetRateLimitsResult{RateLimits: limits}}, nil
}
