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

// Package identity is the anthropic:identity module: service accounts, workload identity
// federation, and role-based access control groups.
package identity

import (
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// Resources returns the module's resources.
func Resources() []infer.InferredResource {
	return []infer.InferredResource{
		infer.Resource(NewServiceAccount()),
		infer.Resource(NewServiceAccountWorkspace()),
		infer.Resource(NewFederationIssuer()),
		infer.Resource(NewFederationRule()),
		infer.Resource(NewFederationRuleWorkspace()),
		infer.Resource(NewRbacGroup()),
		infer.Resource(NewRbacGroupMember()),
	}
}

// The module's collections. Service accounts and workload identity federation accept only
// an OAuth token with the org:admin scope, including where they are managed beneath
// workspaces; RBAC groups accept an Admin API key.
var (
	federationIssuerCollection = rest.Collection{Plane: config.OrgAdminOAuth, Path: "v1/organizations/federation_issuers"}
	federationRuleCollection   = rest.Collection{Plane: config.OrgAdminOAuth, Path: "v1/organizations/federation_rules"}
	rbacGroupCollection        = rest.Collection{Plane: config.Admin, Path: "v1/organizations/rbac_groups"}
	serviceAccountCollection   = rest.Collection{Plane: config.OrgAdminOAuth, Path: "v1/organizations/service_accounts"}
	workspaceCollection        = rest.Collection{Plane: config.OrgAdminOAuth, Path: "v1/organizations/workspaces"}
)
