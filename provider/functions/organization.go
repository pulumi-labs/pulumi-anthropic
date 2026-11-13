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

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
)

// GetOrganization reads the organization the Admin credential belongs to.
type GetOrganization struct{}

func (f *GetOrganization) Annotate(a infer.Annotator) {
	a.Describe(f, "Reads the organization that the provider's Admin API key or OAuth token belongs to.")
}

type GetOrganizationArgs struct{}

type GetOrganizationResult struct {
	OrganizationID string `pulumi:"organizationId"`
	Name           string `pulumi:"name"`
}

func (r *GetOrganizationResult) Annotate(a infer.Annotator) {
	a.Describe(&r.OrganizationID, "The organization's ID, a UUID.")
	a.Describe(&r.Name, "The organization's name.")
}

func (*GetOrganization) Invoke(
	ctx context.Context, _ infer.FunctionRequest[GetOrganizationArgs],
) (infer.FunctionResponse[GetOrganizationResult], error) {
	client, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.FunctionResponse[GetOrganizationResult]{}, err
	}
	org, err := client.Organization.Get(ctx)
	if err != nil {
		return infer.FunctionResponse[GetOrganizationResult]{}, err
	}
	return infer.FunctionResponse[GetOrganizationResult]{
		Output: GetOrganizationResult{OrganizationID: org.ID, Name: org.Name},
	}, nil
}
