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
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// ExternalKey is a customer-managed encryption key held in a cloud KMS.
type ExternalKey struct{ policy *externalKeyUpdatePolicy }

// NewExternalKey returns the complete ExternalKey resource.
func NewExternalKey() infer.InferredResource {
	return externalKeyResource{infer.Resource(&ExternalKey{})}
}

func (k *ExternalKey) Annotate(a infer.Annotator) {
	a.Describe(k, "A customer-managed encryption key (CMEK) held in AWS KMS, Google Cloud KMS, "+
		"or Azure Key Vault. A workspace encrypts its data with the key once the workspace's "+
		"`externalKeyId` refers to it.\n\n"+
		"Creating the key validates it with a round trip to the KMS. Failed validation attempts "+
		"to delete the configuration. If deletion also fails, the configuration may remain and require manual cleanup. "+
		"Changing the provider configuration, geography or customer binding replaces the configuration.\n\n"+
		"Deleting this resource deletes only the configuration, never the underlying cloud key. "+
		"Deletion is refused while a live or archived workspace uses it.")
}

type ExternalKeyArgs struct {
	DisplayName    *string        `pulumi:"displayName,optional"`
	UserProfileID  *string        `pulumi:"userProfileId,optional" provider:"replaceOnChanges"`
	Geo            *string        `pulumi:"geo,optional"         provider:"replaceOnChanges"`
	ProviderConfig ProviderConfig `pulumi:"providerConfig"       provider:"replaceOnChanges"`
}

func (k *ExternalKeyArgs) Annotate(a infer.Annotator) {
	a.Describe(&k.DisplayName, "A display name for the key, 1 to 255 characters. Removing a configured name clears it.")
	a.Describe(&k.Geo, "The geography the key serves. The only accepted value is `us`.")
	a.Describe(&k.ProviderConfig, "Where the key is held. Changing any of these coordinates replaces the configuration.")
	a.Describe(&k.UserProfileID, "The customer user profile to bind this key to. Only workspaces with the same user profile can use a bound key. Omit only for your organization's own key: an unbound key can be used by any workspace, including another customer's. Changing or removing the binding replaces the configuration.")
}

type ExternalKeyState struct {
	ExternalKeyArgs
	Attachment Attachment `pulumi:"attachment"`
	CreatedAt  string     `pulumi:"createdAt"`
	UpdatedAt  string     `pulumi:"updatedAt"`
}

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

// Attachment reports whether a live or archived workspace uses an external key.
type Attachment string

func (controller *ExternalKey) Create(ctx context.Context, req infer.CreateRequest[ExternalKeyArgs]) (infer.CreateResponse[ExternalKeyState], error) {
	if req.DryRun {
		return infer.CreateResponse[ExternalKeyState]{Output: ExternalKeyState{ExternalKeyArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[ExternalKeyState]{}, err
	}
	params, err := externalKeyParams(req.Inputs)
	if err != nil {
		return infer.CreateResponse[ExternalKeyState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.ExternalKeys.New(ctx, params, option.WithResponseBodyInto(&raw))
	// WithResponseBodyInto bypasses the SDK decoder but retains generated request
	// and retry policy; independently decode the official response below.
	if err != nil {
		return infer.CreateResponse[ExternalKeyState]{}, err
	}
	out, id, observeErr := externalKeyObservation(raw, "")
	if observeErr != nil {
		return infer.CreateResponse[ExternalKeyState]{}, observeErr
	}
	resp := infer.CreateResponse[ExternalKeyState]{ID: id, Output: out}
	if err := externalKeyValidate(ctx, c, id); err != nil {
		_, cleanup := c.Organization.ExternalKeys.Delete(ctx, id)
		if cleanup == nil || apiutil.IsNotFound(cleanup) {
			return infer.CreateResponse[ExternalKeyState]{}, err
		}
		return infer.CreateResponse[ExternalKeyState]{}, errors.Join(err, fmt.Errorf("deleting invalid key %s: %w", id, cleanup))
	}
	return resp, nil
}

func (controller *ExternalKey) Read(ctx context.Context, req infer.ReadRequest[ExternalKeyArgs, ExternalKeyState]) (infer.ReadResponse[ExternalKeyArgs, ExternalKeyState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[ExternalKeyArgs, ExternalKeyState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.ExternalKeys.Get(ctx, req.ID, option.WithResponseBodyInto(&raw))
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[ExternalKeyArgs, ExternalKeyState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[ExternalKeyArgs, ExternalKeyState]{}, err
	}
	out, _, err := externalKeyObservation(raw, req.ID)
	if err != nil {
		return infer.ReadResponse[ExternalKeyArgs, ExternalKeyState]{}, err
	}
	return infer.ReadResponse[ExternalKeyArgs, ExternalKeyState]{ID: req.ID, Inputs: out.ExternalKeyArgs, State: out}, nil
}

func (controller *ExternalKey) Update(ctx context.Context, req infer.UpdateRequest[ExternalKeyArgs, ExternalKeyState]) (infer.UpdateResponse[ExternalKeyState], error) {
	if req.DryRun {
		return infer.UpdateResponse[ExternalKeyState]{Output: ExternalKeyState{ExternalKeyArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[ExternalKeyState]{}, err
	}
	params := sdk.OrganizationExternalKeyUpdateParams{DisplayName: externalKeyOpt(req.Inputs.DisplayName)}
	policy := controller.policy
	if req.Inputs.DisplayName == nil && req.State.DisplayName != nil && (policy == nil || policy.clearName) {
		params.DisplayName = param.Null[string]()
	}
	var raw json.RawMessage
	_, err = c.Organization.ExternalKeys.Update(ctx, req.ID, params, option.WithResponseBodyInto(&raw))
	if err != nil {
		return infer.UpdateResponse[ExternalKeyState]{}, err
	}
	out, _, err := externalKeyObservation(raw, req.ID)
	if err != nil {
		return infer.UpdateResponse[ExternalKeyState]{}, err
	}
	if err := externalKeyValidate(ctx, c, req.ID); err != nil {
		return infer.UpdateResponse[ExternalKeyState]{}, err
	}
	return infer.UpdateResponse[ExternalKeyState]{Output: out}, nil
}

func (controller *ExternalKey) Delete(ctx context.Context, req infer.DeleteRequest[ExternalKeyState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.ExternalKeys.Delete(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// infer v1.5 loses input dependencies in Check and does not propagate a
// parent's replacement annotation to nested paths. No values move paths here.
type externalKeyResource struct{ infer.InferredResource }

// The name-clearing policy belongs to one Update invocation, not the registered resource.
type externalKeyUpdatePolicy struct{ clearName bool }

func (r externalKeyResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&ExternalKey{}).Check(ctx, req)
	if err != nil {
		return out, err
	}
	fail := func(path, reason string) {
		out.Failures = append(out.Failures, p.CheckFailure{Property: path, Reason: reason})
	}
	if v := req.Inputs.Get("displayName"); v.IsString() && (utf8.RuneCountInString(v.AsString()) < 1 || utf8.RuneCountInString(v.AsString()) > 255) {
		fail("displayName", "must contain 1 to 255 characters")
	}
	if v := req.Inputs.Get("geo"); v.IsString() && v.AsString() != "us" {
		fail("geo", "only us is supported")
	}
	if v := req.Inputs.Get("userProfileId"); v.IsString() && v.AsString() == "" {
		fail("userProfileId", "must be nonempty")
	}
	if v := req.Inputs.Get("providerConfig"); v.IsMap() {
		m := v.AsMap()
		typ := m.Get("type")
		if typ.IsString() {
			required := map[string]bool{}
			allowed := map[string]bool{"type": true}
			switch typ.AsString() {
			case "aws":
				required["kmsArn"] = true
				allowed["kmsArn"] = true
				allowed["region"] = true
			case "gcp":
				required["keyName"] = true
				allowed["keyName"] = true
			case "azure":
				for _, k := range []string{"keyName", "tenantId", "vaultUri"} {
					required[k] = true
					allowed[k] = true
				}
				allowed["clientId"] = true
			default:
				fail("providerConfig.type", "must be aws, gcp or azure")
			}
			for k := range required {
				v := m.Get(k)
				if v.IsNull() || (v.IsString() && v.AsString() == "") {
					fail("providerConfig."+k, "required and must be nonempty for "+typ.AsString())
				}
			}
			for k, v := range m.All {
				if !allowed[k] && !v.IsNull() {
					fail("providerConfig."+k, "not applicable to "+typ.AsString())
				}
				if (k == "region" || k == "clientId") && v.IsString() && v.AsString() == "" {
					fail("providerConfig."+k, "must be nonempty when specified")
				}
			}
		}
	}
	if len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, nil
}

func (r externalKeyResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	// Ignore server-derived defaults only if they were never declared. Removal
	// of a previously declared immutable value must still replace the key.
	projection := req.Inputs
	for _, k := range []string{"geo"} {
		if !req.OldInputs.Get(k).IsNull() && projection.Get(k).IsNull() {
			projection = projection.Set(k, req.OldInputs.Get(k))
		}
	}
	if old, next := req.OldInputs.Get("providerConfig"), projection.Get("providerConfig"); old.IsMap() && next.IsMap() {
		if v := old.AsMap().Get("region"); !v.IsNull() && next.AsMap().Get("region").IsNull() {
			projection = projection.Set("providerConfig", property.New(next.AsMap().Set("region", v)))
		}
	}
	req.State = externalKeyDefaultProjection(req.State, projection)
	if req.OldInputs.Len() != 0 && req.OldInputs.Get("displayName").IsNull() && req.Inputs.Get("displayName").IsNull() {
		req.State = req.State.Delete("displayName")
	}
	out, err := infer.Resource(&ExternalKey{}).Diff(ctx, req)
	for path, d := range out.DetailedDiff {
		if path == "providerConfig" || strings.HasPrefix(path, "providerConfig.") {
			delete(out.DetailedDiff, path)
			switch d.Kind {
			case p.Add:
				d.Kind = p.AddReplace
			case p.Delete:
				d.Kind = p.DeleteReplace
			case p.Update:
				d.Kind = p.UpdateReplace
			}
			out.DetailedDiff["providerConfig"] = d
		}
	}
	return out, err
}

func (r externalKeyResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&ExternalKey{}).Create(ctx, req)
}

func (r externalKeyResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	out, err := infer.Resource(&ExternalKey{}).Read(ctx, req)
	if err == nil && out.ID != "" {
		// Omitted defaulted geography/region are not new desired declarations.
		if req.Inputs.Len() != 0 {
			out.Inputs = externalKeyDefaultProjection(out.Inputs, req.Inputs)
			if req.Inputs.Get("displayName").IsNull() {
				out.Inputs = out.Inputs.Delete("displayName")
			}
		}
	}
	return out, err
}

func (r externalKeyResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	policy := &externalKeyUpdatePolicy{clearName: !req.State.Get("displayName").IsNull()}
	if req.OldInputs.Len() != 0 {
		policy.clearName = !req.OldInputs.Get("displayName").IsNull()
	}
	return infer.Resource(&ExternalKey{policy: policy}).Update(ctx, req)
}

func (r externalKeyResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&ExternalKey{}).Delete(ctx, req)
}

func externalKeyDefaultProjection(state, inputs property.Map) property.Map {
	if inputs.Get("geo").IsNull() {
		state = state.Delete("geo")
	}
	if v := state.Get("providerConfig"); v.IsMap() {
		in := inputs.Get("providerConfig")
		if in.IsMap() && in.AsMap().Get("region").IsNull() {
			state = state.Set("providerConfig", property.New(v.AsMap().Delete("region")).WithSecret(v.Secret()).WithDependencies(v.Dependencies()))
		}
	}
	return state
}

func externalKeyOpt(v *string) param.Opt[string] {
	if v == nil {
		return param.Opt[string]{}
	}
	return param.NewOpt(*v)
}

func externalKeyParams(in ExternalKeyArgs) (sdk.OrganizationExternalKeyNewParams, error) {
	p := sdk.OrganizationExternalKeyNewParams{DisplayName: externalKeyOpt(in.DisplayName), UserProfileID: externalKeyOpt(in.UserProfileID)}
	if in.Geo != nil {
		p.Geo = sdk.OrganizationExternalKeyNewParamsGeo(*in.Geo)
	}
	c := in.ProviderConfig
	value := func(v *string) string {
		if v == nil {
			return ""
		}
		return *v
	}
	switch c.Type {
	case "aws":
		p.ProviderConfig.OfAWS = &sdk.AWSExternalKeyConfigParam{KMSARN: value(c.KmsArn), Region: externalKeyOpt(c.Region)}
	case "gcp":
		p.ProviderConfig.OfGCP = &sdk.GCPExternalKeyConfigParam{KeyName: value(c.KeyName)}
	case "azure":
		p.ProviderConfig.OfAzure = &sdk.AzureExternalKeyConfigParam{KeyName: value(c.KeyName), TenantID: value(c.TenantID), VaultURI: value(c.VaultURI), ClientID: externalKeyOpt(c.ClientID)}
	default:
		return p, fmt.Errorf("unsupported key provider %q", c.Type)
	}
	return p, nil
}

// Validate identity and JSON types before using SDK strings, since the SDK
// permits primitive-to-string coercion.
func externalKeyObservation(raw json.RawMessage, expected string) (ExternalKeyState, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ExternalKeyState{}, "", err
	}
	str := func(m map[string]json.RawMessage, key string, nullable bool) (*string, bool) {
		raw, ok := m[key]
		if !ok {
			return nil, false
		}
		if string(raw) == "null" {
			return nil, nullable
		}
		var s string
		if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		return &s, true
	}
	id, ok := str(fields, "id", false)
	if !ok || id == nil || *id == "" || (expected != "" && *id != expected) {
		return ExternalKeyState{}, "", errors.New("invalid external key response identity")
	}
	var r sdk.ExternalKey
	decodeErr := json.Unmarshal(raw, &r)
	out := ExternalKeyState{}
	valid := decodeErr == nil
	checkString := func(key string, nullable bool) *string {
		v, ok := str(fields, key, nullable)
		valid = valid && ok
		return v
	}
	out.DisplayName = checkString("display_name", true)
	out.Geo = checkString("geo", false)
	out.UserProfileID = checkString("user_profile_id", true)
	if v := checkString("type", false); v == nil || *v != "external_key" {
		valid = false
	}
	if out.Geo == nil || *out.Geo != "us" {
		valid = false
	}
	var configFields map[string]json.RawMessage
	if json.Unmarshal(fields["provider_config"], &configFields) != nil {
		valid = false
	}
	typ, ok := str(configFields, "type", false)
	if !ok || typ == nil {
		valid = false
	} else {
		out.ProviderConfig.Type = KeyProvider(*typ)
		field := func(key string, required bool) *string {
			v, ok := str(configFields, key, !required)
			if !required && len(configFields[key]) == 0 {
				return nil
			}
			valid = valid && ok && (!required || (v != nil && *v != ""))
			return v
		}
		switch *typ {
		case "aws":
			out.ProviderConfig.KmsArn = field("kms_arn", true)
			out.ProviderConfig.Region = field("region", false)
		case "gcp":
			out.ProviderConfig.KeyName = field("key_name", true)
		case "azure":
			out.ProviderConfig.KeyName = field("key_name", true)
			out.ProviderConfig.TenantID = field("tenant_id", true)
			out.ProviderConfig.VaultURI = field("vault_uri", true)
			out.ProviderConfig.ClientID = field("client_id", false)
		default:
			valid = false
			out.ProviderConfig = ProviderConfig{}
		}
	}
	var attachment map[string]json.RawMessage
	_ = json.Unmarshal(fields["attachment"], &attachment)
	if v, ok := str(attachment, "type", false); ok && v != nil && (*v == "attached" || *v == "unattached") {
		out.Attachment = Attachment(*v)
	} else {
		valid = false
	}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	} else {
		valid = false
	}
	if !r.UpdatedAt.IsZero() {
		out.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	} else {
		valid = false
	}
	if !valid {
		return ExternalKeyState{}, "", errors.New("incomplete or invalid external key response")
	}
	return out, *id, nil
}

func externalKeyValidate(ctx context.Context, c *sdk.Client, id string) error {
	r, err := c.Organization.ExternalKeys.Validate(ctx, id)
	if err != nil {
		return fmt.Errorf("validating the key: %w", err)
	}
	if r.JSON.Type.Raw() != `"external_key_validation"` || r.JSON.Status.Raw() != `"success"` {
		return fmt.Errorf("the key failed validation: %s (status %q)", r.Error, r.Status)
	}
	return nil
}
