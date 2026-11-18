package organization

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// ApiKey adopts an existing console-created key; it never creates key material.
type ApiKey struct{}

// NewApiKey returns the complete ApiKey resource.
func NewApiKey() infer.InferredResource {
	return apiKeyResource{infer.Resource(&ApiKey{})}
}

func (k *ApiKey) Annotate(a infer.Annotator) {
	a.Describe(k, "Manages an existing API key's name and status. Create the key in the Claude Console and supply its ID; secret key material is never returned. Omitted name or status leaves that value unchanged. Removing either from configuration stops managing it without clearing it.\n\nDeleting this resource archives and permanently revokes the key. Use retainOnDelete to stop managing it without revocation. Changing apiKeyId adopts the new key before revoking the old one. Archived and expired keys remain readable.")
}

type ApiKeyArgs struct {
	ApiKeyID string        `pulumi:"apiKeyId" provider:"replaceOnChanges"`
	Name     *string       `pulumi:"name,optional"`
	Status   *ApiKeyStatus `pulumi:"status,optional"`
}

func (k *ApiKeyArgs) Annotate(a infer.Annotator) {
	a.Describe(&k.ApiKeyID, "The existing key's nonempty ID. Changing this value replaces the resource and revokes the previously managed key.")
	a.Describe(&k.Name, "The key's name, 1 to 500 characters. Omission leaves the current name unchanged.")
	a.Describe(&k.Status, "The desired status: active, inactive or archived. Archived keys cannot be reactivated. Omission leaves the current status unchanged; expired is observation-only.")
}

type ApiKeyStatus string

const archived ApiKeyStatus = "archived"

func (ApiKeyStatus) Values() []infer.EnumValue[ApiKeyStatus] {
	return []infer.EnumValue[ApiKeyStatus]{
		{Value: "active", Description: "The key can be used."}, {Value: "inactive", Description: "The key is disabled, and can be reactivated."}, {Value: archived, Description: "The key is permanently revoked."},
	}
}

type ApiKeyScope struct {
	Type        string  `pulumi:"type"`
	WorkspaceID *string `pulumi:"workspaceId,optional"`
}

func (s *ApiKeyScope) Annotate(a infer.Annotator) {
	a.Describe(s, "The organization or workspace to which a key belongs.")
	a.Describe(&s.Type, "Either organization or workspace.")
	a.Describe(&s.WorkspaceID, "The actual workspace ID, including for the default workspace; absent for organization-scoped keys.")
}

type ApiKeyActor struct {
	Type string `pulumi:"type"`
	ID   string `pulumi:"id"`
}

func (s *ApiKeyActor) Annotate(a infer.Annotator) {
	a.Describe(s, "A user or service account associated with a key.")
	a.Describe(&s.Type, "Either user or service_account.")
	a.Describe(&s.ID, "The user or service account ID.")
}

type ApiKeyState struct {
	ApiKeyArgs
	PartialKeyHint string       `pulumi:"partialKeyHint"`
	WorkspaceID    string       `pulumi:"workspaceId"`
	CreatedAt      string       `pulumi:"createdAt"`
	ExpiresAt      string       `pulumi:"expiresAt"`
	ObservedStatus string       `pulumi:"observedStatus"`
	Scope          *ApiKeyScope `pulumi:"scope,optional"`
	Principal      *ApiKeyActor `pulumi:"principal,optional"`
	CreatedBy      *ApiKeyActor `pulumi:"createdBy,optional"`
}

func (s *ApiKeyState) Annotate(a infer.Annotator) {
	a.Describe(&s.PartialKeyHint, "A redacted hint for recognizing the key; empty if unavailable.")
	a.Describe(&s.WorkspaceID, "The key's actual workspace ID, or an empty string for organization scope. See scope for the ownership boundary.")
	a.Describe(&s.CreatedAt, "When the key was created, in RFC 3339 format.")
	a.Describe(&s.ExpiresAt, "When the key expires, in RFC 3339 format, or an empty string if it never expires.")
	a.Describe(&s.ObservedStatus, "The current status, including active, inactive, archived or expired. An expired key's status is not recovered as a writable input on import.")
	a.Describe(&s.Scope, "Where the key belongs: an organization or a workspace.")
	a.Describe(&s.Principal, "The user or service account the key acts as, if bound to a principal.")
	a.Describe(&s.CreatedBy, "The actor that created the key, if recorded.")
}

func (controller *ApiKey) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[ApiKeyArgs], error) {
	in, failures, err := infer.DefaultCheck[ApiKeyArgs](ctx, req.NewInputs)
	for _, f := range []struct {
		key    string
		valid  func(string) bool
		reason string
	}{
		{"apiKeyId", func(s string) bool { return s != "" }, "must not be empty"},
		{"name", func(s string) bool { n := utf8.RuneCountInString(s); return n >= 1 && n <= 500 }, "must contain 1 to 500 characters"},
		{"status", apiKeyWritable, "must be active, inactive or archived"},
	} {
		v := req.NewInputs.Get(f.key)
		if v.IsString() && !f.valid(v.AsString()) {
			failures = append(failures, p.CheckFailure{Property: f.key, Reason: f.reason})
		}
	}
	return infer.CheckResponse[ApiKeyArgs]{Inputs: in, Failures: failures}, err
}

func (controller *ApiKey) Create(ctx context.Context, req infer.CreateRequest[ApiKeyArgs]) (infer.CreateResponse[ApiKeyState], error) {
	if req.DryRun {
		return infer.CreateResponse[ApiKeyState]{Output: ApiKeyState{ApiKeyArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, err
	}
	r, err := c.Organization.APIKeys.Get(ctx, req.Inputs.ApiKeyID)
	if apiutil.IsNotFound(err) {
		return infer.CreateResponse[ApiKeyState]{}, fmt.Errorf("API key %s does not exist; create it in the Claude Console first", req.Inputs.ApiKeyID)
	}
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, err
	}
	// Validate the adopted identity before any mutation; a foreign response must never be managed.
	baseline, err := apiKeyObserved(r, req.Inputs.ApiKeyID, req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, err
	}
	if req.Inputs.Name == nil && req.Inputs.Status == nil {
		return infer.CreateResponse[ApiKeyState]{ID: req.Inputs.ApiKeyID, Output: baseline}, nil
	}
	r, err = c.Organization.APIKeys.Update(ctx, req.Inputs.ApiKeyID, apiKeyParams(req.Inputs))
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, err
	}
	out, err := apiKeyObserved(r, req.Inputs.ApiKeyID, req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[ApiKeyState]{}, err
	}
	return infer.CreateResponse[ApiKeyState]{ID: req.Inputs.ApiKeyID, Output: out}, err
}

func (controller *ApiKey) Read(ctx context.Context, req infer.ReadRequest[ApiKeyArgs, ApiKeyState]) (infer.ReadResponse[ApiKeyArgs, ApiKeyState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, err
	}
	r, err := c.Organization.APIKeys.Get(ctx, req.ID)
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, err
	}
	out, err := apiKeyObserved(r, req.ID, req.Inputs, req.Inputs.ApiKeyID == "")
	if err != nil {
		return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{}, err
	}
	return infer.ReadResponse[ApiKeyArgs, ApiKeyState]{ID: req.ID, Inputs: out.ApiKeyArgs, State: out}, err
}

func (controller *ApiKey) Update(ctx context.Context, req infer.UpdateRequest[ApiKeyArgs, ApiKeyState]) (infer.UpdateResponse[ApiKeyState], error) {
	if req.DryRun {
		return infer.UpdateResponse[ApiKeyState]{Output: ApiKeyState{ApiKeyArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[ApiKeyState]{}, err
	}
	r, err := c.Organization.APIKeys.Update(ctx, req.ID, apiKeyParams(req.Inputs))
	if err != nil {
		return infer.UpdateResponse[ApiKeyState]{}, err
	}
	out, err := apiKeyObserved(r, req.ID, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[ApiKeyState]{}, err
	}
	return infer.UpdateResponse[ApiKeyState]{Output: out}, err
}

func (controller *ApiKey) Delete(ctx context.Context, req infer.DeleteRequest[ApiKeyState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.APIKeys.Update(ctx, req.ID, sdk.OrganizationAPIKeyUpdateParams{Status: sdk.OrganizationAPIKeyUpdateParamsStatusArchived})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve validated Check inputs and preview unknown optional server observations.
type apiKeyResource struct{ infer.InferredResource }

func (r apiKeyResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&ApiKey{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r apiKeyResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&ApiKey{}).Diff(ctx, req)
}

func (r apiKeyResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	resp, err := infer.Resource(&ApiKey{}).Create(ctx, req)
	if err == nil && req.DryRun {
		resp.Properties = apiKeyPreview(resp.Properties)
	}
	return resp, err
}

func (r apiKeyResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&ApiKey{}).Read(ctx, req)
}

func (r apiKeyResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	resp, err := infer.Resource(&ApiKey{}).Update(ctx, req)
	if err == nil && req.DryRun {
		resp.Properties = apiKeyPreview(resp.Properties)
	}
	return resp, err
}

func (r apiKeyResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&ApiKey{}).Delete(ctx, req)
}

func apiKeyPreview(m property.Map) property.Map {
	// These server-only values are unknown until adoption, including optional actors.
	for _, k := range []string{"partialKeyHint", "workspaceId", "createdAt", "expiresAt", "observedStatus", "scope", "principal", "createdBy"} {
		m = m.Set(k, property.New(property.Computed))
	}
	return m
}

func apiKeyWritable(s string) bool { return s == "active" || s == "inactive" || s == "archived" }

func apiKeyParams(in ApiKeyArgs) sdk.OrganizationAPIKeyUpdateParams {
	out := sdk.OrganizationAPIKeyUpdateParams{}
	if in.Name != nil {
		out.Name = sdk.String(*in.Name)
	}
	if in.Status != nil {
		out.Status = sdk.OrganizationAPIKeyUpdateParamsStatus(*in.Status)
	}
	return out
}

// Generated decoding is permissive; validate server facts before publishing state.
func apiKeyObserved(r *sdk.APIKey, id string, in ApiKeyArgs, importing bool) (ApiKeyState, error) {
	out := ApiKeyState{ApiKeyArgs: ApiKeyArgs{ApiKeyID: id}}
	var failures []string
	bad := func(s string) { failures = append(failures, s) }
	if r == nil || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID != id || r.JSON.Type.Raw() != `"api_key"` {
		return out, errors.New("invalid API key response identity or type")
	}
	if apiutil.JSONString(r.JSON.Name.Raw()) && r.Name != "" {
		if importing || in.Name != nil {
			v := r.Name
			out.Name = &v
		}
	} else {
		bad("name")
	}
	if apiKeyWritable(string(r.Status)) || r.Status == sdk.APIKeyStatusExpired {
		out.ObservedStatus = string(r.Status)
		if apiKeyWritable(string(r.Status)) && (importing || in.Status != nil) {
			v := ApiKeyStatus(r.Status)
			out.Status = &v
		} else if r.Status == sdk.APIKeyStatusExpired {
			// Expiration is observable drift, not a valid management command.
			out.Status = nil
		}
	} else {
		bad("status")
	}
	if apiutil.JSONString(r.JSON.CreatedAt.Raw()) && !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	} else {
		bad("created_at")
	}
	if r.JSON.ExpiresAt.Raw() == "null" {
		out.ExpiresAt = ""
	} else if apiutil.JSONString(r.JSON.ExpiresAt.Raw()) && !r.ExpiresAt.IsZero() {
		out.ExpiresAt = r.ExpiresAt.Format(time.RFC3339Nano)
	} else {
		bad("expires_at")
	}
	if r.JSON.PartialKeyHint.Raw() == "null" || apiutil.JSONString(r.JSON.PartialKeyHint.Raw()) {
		out.PartialKeyHint = r.PartialKeyHint
	} else {
		bad("partial_key_hint")
	}
	switch r.Scope.Type {
	case "organization":
		out.Scope = &ApiKeyScope{Type: "organization"}
	case "workspace":
		if apiutil.JSONString(r.Scope.JSON.WorkspaceID.Raw()) && r.Scope.WorkspaceID != "" {
			v := r.Scope.WorkspaceID
			out.Scope = &ApiKeyScope{Type: "workspace", WorkspaceID: &v}
			out.WorkspaceID = v
		} else {
			bad("scope.workspace_id")
		}
	default:
		bad("scope")
	}
	if r.JSON.Principal.Raw() != "null" {
		switch r.Principal.Type {
		case "user_actor":
			if apiutil.JSONString(r.Principal.JSON.UserID.Raw()) && r.Principal.UserID != "" {
				out.Principal = &ApiKeyActor{Type: "user", ID: r.Principal.UserID}
			} else {
				bad("principal.user_id")
			}
		case "service_account_actor":
			if apiutil.JSONString(r.Principal.JSON.ServiceAccountID.Raw()) && r.Principal.ServiceAccountID != "" {
				out.Principal = &ApiKeyActor{Type: "service_account", ID: r.Principal.ServiceAccountID}
			} else {
				bad("principal.service_account_id")
			}
		default:
			bad("principal")
		}
	}
	if r.JSON.CreatedBy.Raw() == "null" {
		out.CreatedBy = nil
	} else if (r.CreatedBy.Type == "user" || r.CreatedBy.Type == "service_account") && apiutil.JSONString(r.CreatedBy.JSON.ID.Raw()) && r.CreatedBy.ID != "" {
		out.CreatedBy = &ApiKeyActor{Type: string(r.CreatedBy.Type), ID: r.CreatedBy.ID}
	} else {
		bad("created_by")
	}
	if len(failures) > 0 {
		return out, fmt.Errorf("invalid API key response fields: %v", failures)
	}
	return out, nil
}
