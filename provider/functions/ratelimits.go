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

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// GetRateLimits reads the rate limits of the organization or of one workspace.
type GetRateLimits struct{}

func (f *GetRateLimits) Annotate(a infer.Annotator) {
	a.Describe(f, "Reads the rate limits that apply to the organization or, given a "+
		"`workspaceId`, to one workspace. Rate limits are set by Anthropic and cannot be "+
		"managed through the API.")
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

// workspaceRateLimit is a workspace rate limit as the API returns it: it names the
// limit's ID rate_limit_id rather than id.
type workspaceRateLimit struct {
	RateLimit
	RateLimitID string `pulumi:"rateLimitId"`
}

func (*GetRateLimits) Invoke(
	ctx context.Context, req infer.FunctionRequest[GetRateLimitsArgs],
) (infer.FunctionResponse[GetRateLimitsResult], error) {
	limits := []RateLimit{}
	var err error
	if req.Input.WorkspaceID == nil {
		limits, err = rest.List[RateLimit](ctx, rateLimitCollection)
	} else {
		var wsLimits []workspaceRateLimit
		wsLimits, err = rest.List[workspaceRateLimit](ctx, workspaceCollection.Sub(*req.Input.WorkspaceID, "rate_limits"))
		for _, l := range wsLimits {
			l.ID = l.RateLimitID
			limits = append(limits, l.RateLimit)
		}
	}
	return infer.FunctionResponse[GetRateLimitsResult]{Output: GetRateLimitsResult{RateLimits: limits}}, err
}
