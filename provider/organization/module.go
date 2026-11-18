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

// Package organization is the anthropic:organization module: an organization's
// workspaces, people, API keys, spend limits, and compliance settings, managed through
// the Admin API.
package organization

import (
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Resources returns the module's resources.
func Resources() []infer.InferredResource {
	return []infer.InferredResource{
		NewWorkspace(),
		NewWorkspaceMember(),
		NewInvite(),
		NewUser(),
		NewApiKey(),
		NewSpendLimit(),
		NewComplianceSettings(),
	}
}
