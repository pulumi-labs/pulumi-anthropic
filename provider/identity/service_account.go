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

// ServiceAccount is a non-human identity that workloads authenticate as through workload
// identity federation.
type ServiceAccount struct {
	rest.Resource[ServiceAccountArgs, ServiceAccountState, *ServiceAccountState]
}

// NewServiceAccount returns the ServiceAccount resource controller.
func NewServiceAccount() *ServiceAccount {
	return &ServiceAccount{rest.Resource[ServiceAccountArgs, ServiceAccountState, *ServiceAccountState]{
		Collection: serviceAccountCollection,
		EndOfLife:  rest.Archive,
	}}
}

func (s *ServiceAccount) Annotate(a infer.Annotator) {
	a.Describe(s, "A service account: a non-human identity that workloads assume through "+
		"workload identity federation (see `FederationRule`), in place of long-lived API keys. "+
		"Grant it access to workspaces with `ServiceAccountWorkspace`.\n\n"+
		"Managing service accounts requires an OAuth token with the `org:admin` scope "+
		"(`anthropic:authToken`); the API does not accept Admin API keys for them.\n\n"+
		"The API cannot delete service accounts, so deleting this resource archives it. "+
		"Archiving is refused while a live federation rule targets the account.")
}

type ServiceAccountArgs struct {
	Name             string              `pulumi:"name"                      provider:"replaceOnChanges"`
	Description      *string             `pulumi:"description,optional"`
	OrganizationRole *ServiceAccountRole `pulumi:"organizationRole,optional"`
}

func (s *ServiceAccountArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "A slug that uniquely names the service account in the organization. "+
		"It cannot be changed; changing it replaces the service account.")
	a.Describe(&s.Description, "A description of the service account, up to 2,000 characters.")
	a.Describe(&s.OrganizationRole, "The service account's organization role. Defaults to `developer`.")
}

type ServiceAccountState struct {
	ServiceAccountArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *ServiceAccountState) Inputs() *ServiceAccountArgs { return &s.ServiceAccountArgs }

func (s *ServiceAccountState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the service account was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the service account was last updated, in RFC 3339 format.")
}

// ServiceAccountRole is a service account's role in the organization. Its values differ
// from the roles people hold (organization.OrganizationRole).
type ServiceAccountRole string

func (ServiceAccountRole) Values() []infer.EnumValue[ServiceAccountRole] {
	return []infer.EnumValue[ServiceAccountRole]{
		{Value: "developer", Description: "Can use the API in the workspaces it is a member of."},
		{Value: "admin", Description: "Can administer the organization. Creating an admin " +
			"service account requires an interactive credential."},
	}
}
