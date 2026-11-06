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

package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// ExternalKey is a customer-managed encryption key held in a cloud KMS.
type ExternalKey struct {
	rest.Resource[ExternalKeyArgs, ExternalKeyState, *ExternalKeyState]
}

// NewExternalKey returns the ExternalKey resource controller.
func NewExternalKey() *ExternalKey {
	return &ExternalKey{rest.Resource[ExternalKeyArgs, ExternalKeyState, *ExternalKeyState]{
		Collection: externalKeyCollection,
		EndOfLife:  rest.Delete,
	}}
}

func (k *ExternalKey) Annotate(a infer.Annotator) {
	a.Describe(k, "A customer-managed encryption key (CMEK) held in AWS KMS, Google Cloud KMS, "+
		"or Azure Key Vault. A workspace encrypts its data with the key once the workspace's "+
		"`externalKeyId` refers to it.\n\n"+
		"Creating the key validates it with a round trip to the KMS, and fails, leaving nothing "+
		"behind, if Anthropic cannot use it. The key's provider configuration and geography "+
		"cannot change once a workspace uses the key, so changing either replaces the key.\n\n"+
		"Deleting the key is refused while a workspace uses it.")
}

type ExternalKeyArgs struct {
	DisplayName    *string        `pulumi:"displayName,optional"`
	Geo            *string        `pulumi:"geo,optional"         provider:"replaceOnChanges"`
	ProviderConfig ProviderConfig `pulumi:"providerConfig"       provider:"replaceOnChanges"`
}

func (k *ExternalKeyArgs) Annotate(a infer.Annotator) {
	a.Describe(&k.DisplayName, "A display name for the key, 1 to 255 characters.")
	a.Describe(&k.Geo, "The geography the key serves. The only accepted value is `us`.")
	a.Describe(&k.ProviderConfig, "Where the key is held.")
}

type ExternalKeyState struct {
	ExternalKeyArgs
	Attachment Attachment `pulumi:"attachment"`
	CreatedAt  string     `pulumi:"createdAt"`
	UpdatedAt  string     `pulumi:"updatedAt"`
}

func (k *ExternalKeyState) Inputs() *ExternalKeyArgs { return &k.ExternalKeyArgs }

func (k *ExternalKeyState) Annotate(a infer.Annotator) {
	a.Describe(&k.Attachment, "Whether a workspace uses the key: `attached` or `unattached`.")
	a.Describe(&k.CreatedAt, "When the key was created, in RFC 3339 format.")
	a.Describe(&k.UpdatedAt, "When the key was last updated, in RFC 3339 format.")
}

// KeyProvider discriminates the clouds an external key can be held in.
type KeyProvider string

func (KeyProvider) Values() []infer.EnumValue[KeyProvider] {
	return []infer.EnumValue[KeyProvider]{
		{Value: "aws", Description: "AWS Key Management Service."},
		{Value: "gcp", Description: "Google Cloud Key Management Service."},
		{Value: "azure", Description: "Azure Key Vault."},
	}
}

// ProviderConfig locates an external key in its cloud. Which fields apply depends on the
// type.
type ProviderConfig struct {
	Type     KeyProvider `pulumi:"type"`
	KmsArn   *string     `pulumi:"kmsArn,optional"`
	Region   *string     `pulumi:"region,optional"`
	KeyName  *string     `pulumi:"keyName,optional"`
	TenantID *string     `pulumi:"tenantId,optional"`
	VaultURI *string     `pulumi:"vaultUri,optional"`
	ClientID *string     `pulumi:"clientId,optional"`
}

func (c *ProviderConfig) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "The cloud the key is held in.")
	a.Describe(&c.KmsArn, "The ARN of the AWS KMS key. Required for `aws`.")
	a.Describe(&c.Region, "The AWS region of the key. Derived from the ARN if omitted. For `aws`.")
	a.Describe(&c.KeyName, "The key's resource name. Required for `gcp` and `azure`.")
	a.Describe(&c.TenantID, "The Azure tenant ID. Required for `azure`.")
	a.Describe(&c.VaultURI, "The Azure Key Vault URI. Required for `azure`.")
	a.Describe(&c.ClientID, "The client ID of the Azure application that accesses the key. "+
		"Omit to use Anthropic's multitenant application. For `azure`.")
}

// Attachment reports whether a workspace uses an external key. The API encodes it as
// {"type": "attached"}.
type Attachment string

func (a *Attachment) UnmarshalJSON(raw []byte) error {
	s, err := rest.UnmarshalTypeTag(raw)
	*a = Attachment(s)
	return err
}

var _ infer.CustomCreate[ExternalKeyArgs, ExternalKeyState] = (*ExternalKey)(nil)

// Create creates the key, then validates it, so that a key Anthropic cannot use fails
// the deployment instead of the first workspace that relies on it. A key that fails
// validation is deleted again.
func (k *ExternalKey) Create(
	ctx context.Context, req infer.CreateRequest[ExternalKeyArgs],
) (infer.CreateResponse[ExternalKeyState], error) {
	resp, err := k.Resource.Create(ctx, req)
	if err != nil || req.DryRun {
		return resp, err
	}
	if err := k.validate(ctx, resp.ID); err != nil {
		if cleanup := rest.End(ctx, k.Collection, resp.ID, rest.Delete); cleanup != nil {
			err = errors.Join(err, fmt.Errorf("deleting the invalid key %s: %w", resp.ID, cleanup))
		}
		return infer.CreateResponse[ExternalKeyState]{}, err
	}
	return resp, nil
}

func (k *ExternalKey) validate(ctx context.Context, id string) error {
	var raw []byte
	if err := k.Collection.Action(ctx, id, "validate", &raw); err != nil {
		return fmt.Errorf("validating the key: %w", err)
	}
	var result struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("decoding the key's validation: %w", err)
	}
	if result.Status != "success" {
		return fmt.Errorf("the key failed validation: %s", result.Error)
	}
	return nil
}
