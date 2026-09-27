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

// Package roles describes the workspace roles, which people and service accounts both
// hold, so that each module's enum of them documents them identically.
package roles

// Role is a workspace role and its description.
type Role struct {
	Value       string
	Description string
}

// Billing is the workspace role that only people, not service accounts, can hold.
const Billing = "workspace_billing"

// Workspace lists the workspace roles, from least to most privileged.
var Workspace = []Role{
	{Value: "workspace_user", Description: "Can use the workspace's resources."},
	{Value: "workspace_restricted_developer", Description: "Can use the API, with restricted " +
		"access to the workspace's configuration."},
	{Value: "workspace_developer", Description: "Can use the API and manage the workspace's resources."},
	{Value: "workspace_admin", Description: "Can administer the workspace and its members."},
	{Value: Billing, Description: "Can manage the workspace's billing."},
}
