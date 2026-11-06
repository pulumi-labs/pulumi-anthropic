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

package organization

import (
	"context"
	"net/http"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// ApiKey manages the name and status of an existing API key.
type ApiKey struct {
	rest.Adopted[ApiKeyArgs, ApiKeyState, *ApiKeyState]
}

// NewApiKey returns the ApiKey resource controller.
func NewApiKey() *ApiKey {
	return &ApiKey{rest.Adopted[ApiKeyArgs, ApiKeyState, *ApiKeyState]{
		// Deleting a key archives it by status; see Delete.
		Resource: rest.Resource[ApiKeyArgs, ApiKeyState, *ApiKeyState]{Collection: apiKeyCollection},
		ID:       func(a *ApiKeyArgs) *string { return &a.ApiKeyID },
		Noun:     "API key",
		Origin:   "API keys are created in the Claude Console",
	}}
}

func (k *ApiKey) Annotate(a infer.Annotator) {
	a.Describe(k, "An API key's name and status.\n\n"+
		"API keys cannot be created through the API, only in the Claude Console, and their "+
		"secret values are never returned. Creating this resource adopts an existing key by "+
		"ID; fields left undeclared are left as they are.\n\n"+
		"Deleting this resource archives the key, which permanently revokes it. To stop "+
		"managing a key without revoking it, set the `retainOnDelete` resource option.")
}

type ApiKeyArgs struct {
	ApiKeyID string        `pulumi:"apiKeyId"        provider:"replaceOnChanges" wire:"-"`
	Name     *string       `pulumi:"name,optional"`
	Status   *ApiKeyStatus `pulumi:"status,optional"`
}

func (k *ApiKeyArgs) Annotate(a infer.Annotator) {
	a.Describe(&k.ApiKeyID, "The ID of the existing API key to manage.")
	a.Describe(&k.Name, "The key's name, 1 to 500 characters.")
	a.Describe(&k.Status, "The key's status. An archived key cannot be reactivated.")
}

// ApiKeyStatus is an API key's status.
type ApiKeyStatus string

// archived is the status that revokes a key for good.
const archived ApiKeyStatus = "archived"

func (ApiKeyStatus) Values() []infer.EnumValue[ApiKeyStatus] {
	return []infer.EnumValue[ApiKeyStatus]{
		{Value: "active", Description: "The key can be used."},
		{Value: "inactive", Description: "The key is disabled, and can be reactivated."},
		{Value: archived, Description: "The key is permanently revoked."},
	}
}

type ApiKeyState struct {
	ApiKeyArgs
	PartialKeyHint string `pulumi:"partialKeyHint"`
	WorkspaceID    string `pulumi:"workspaceId"`
	CreatedAt      string `pulumi:"createdAt"`
	ExpiresAt      string `pulumi:"expiresAt"`
}

func (s *ApiKeyState) Inputs() *ApiKeyArgs { return &s.ApiKeyArgs }

func (s *ApiKeyState) Annotate(a infer.Annotator) {
	a.Describe(&s.PartialKeyHint, "A redacted hint of the key's value, for recognizing it.")
	a.Describe(&s.WorkspaceID, "The ID of the workspace the key belongs to.")
	a.Describe(&s.CreatedAt, "When the key was created, in RFC 3339 format.")
	a.Describe(&s.ExpiresAt, "When the key expires, in RFC 3339 format, if it does.")
}

var _ infer.CustomDelete[ApiKeyState] = (*ApiKey)(nil)

// Delete archives the key.
func (k *ApiKey) Delete(ctx context.Context, req infer.DeleteRequest[ApiKeyState]) (infer.DeleteResponse, error) {
	body := map[string]any{"status": string(archived)}
	err := k.Collection.Do(ctx, http.MethodPost, k.Collection.Item(req.ID), body, nil)
	if rest.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}
