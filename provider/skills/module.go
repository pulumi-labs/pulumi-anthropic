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

// Package skills is the anthropic:skills module: custom Agent Skills.
package skills

import (
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// Resources returns the module's resources.
func Resources() []infer.InferredResource {
	return []infer.InferredResource{
		infer.Resource(NewSkill()),
	}
}

// The module's collections. The Skills API is generally available, so it takes no beta
// parameter.
var skillCollection = rest.Collection{Plane: config.Workspace, Path: "v1/skills", GA: true}
