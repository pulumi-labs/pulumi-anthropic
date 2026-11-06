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

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// GetModels lists the models available to the workspace.
type GetModels struct{}

func (f *GetModels) Annotate(a infer.Annotator) {
	a.Describe(f, "Lists the Claude models available to the provider's workspace, newest first.")
}

type GetModelsArgs struct{}

type GetModelsResult struct {
	Models []ModelInfo `pulumi:"models"`
}

func (r *GetModelsResult) Annotate(a infer.Annotator) {
	a.Describe(&r.Models, "The available models, newest first.")
}

// ModelInfo describes an available model.
type ModelInfo struct {
	ID          string `pulumi:"id"`
	DisplayName string `pulumi:"displayName"`
	CreatedAt   string `pulumi:"createdAt"`
}

func (m *ModelInfo) Annotate(a infer.Annotator) {
	a.Describe(&m.ID, "The model ID, such as `claude-opus-5-5`, as used in an agent's `model.id`.")
	a.Describe(&m.DisplayName, "The model's human-readable name.")
	a.Describe(&m.CreatedAt, "When the model was released, in RFC 3339 format.")
}

func (*GetModels) Invoke(
	ctx context.Context, _ infer.FunctionRequest[GetModelsArgs],
) (infer.FunctionResponse[GetModelsResult], error) {
	models, err := rest.List[ModelInfo](ctx, modelCollection)
	return infer.FunctionResponse[GetModelsResult]{Output: GetModelsResult{Models: models}}, err
}
