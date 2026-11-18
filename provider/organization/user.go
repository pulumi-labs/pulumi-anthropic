package organization

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// User manages an existing organization membership, not a global account.
type User struct{}

// NewUser returns the complete User resource.
func NewUser() infer.InferredResource {
	return userResource{infer.Resource(&User{})}
}

func (u *User) Annotate(a infer.Annotator) {
	a.Describe(u, "Manages an existing user's organization membership and role. People join by accepting an `anthropic.organization.Invite`; creating this resource adopts an existing user by ID and assigns the configured role. Deleting this resource removes the user from the organization, including when replacing userId. Use retainOnDelete to stop managing a user without removing them. Import uses the user ID; privileged roles can be observed but cannot be assigned, so select an assignable role before managing an imported privileged user.")
}

type UserArgs struct {
	UserID string           `pulumi:"userId" provider:"replaceOnChanges"`
	Role   OrganizationRole `pulumi:"role"`
}

func (u *UserArgs) Annotate(a infer.Annotator) {
	a.Describe(&u.UserID, "The existing user's ID. Changing this adopts another user and removes the old membership unless retainOnDelete is set.")
	a.Describe(&u.Role, "The role to assign. Console organizations support user, developer, billing and claude_code_user; Enterprise organizations support user and managed. Assigning managed additionally requires write:rbac_groups permission. Administrator and owner roles cannot be assigned.")
}

type UserState struct {
	UserID  string  `pulumi:"userId"`
	Role    string  `pulumi:"role"`
	Email   *string `pulumi:"email,optional"`
	Name    *string `pulumi:"name,optional"`
	AddedAt *string `pulumi:"addedAt,optional"`
}

func (s *UserState) Annotate(a infer.Annotator) {
	a.Describe(&s.UserID, "The existing user's ID.")
	a.Describe(&s.Role, "The observed organization role, including privileged administrator or owner roles that cannot be assigned.")
	a.Describe(&s.Email, "The user's email address.")
	a.Describe(&s.Name, "The user's name.")
	a.Describe(&s.AddedAt, "When the user joined the organization, in RFC 3339 format.")
}

func (*User) Create(ctx context.Context, req infer.CreateRequest[UserArgs]) (infer.CreateResponse[UserState], error) {
	out := UserState{UserID: req.Inputs.UserID, Role: string(req.Inputs.Role)}
	if req.DryRun {
		return infer.CreateResponse[UserState]{Output: out}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.Users.Get(ctx, req.Inputs.UserID, option.WithMaxRetries(0), option.WithResponseBodyInto(&raw))
	if apiutil.IsNotFound(err) {
		return infer.CreateResponse[UserState]{}, fmt.Errorf("user %s does not exist; users join by accepting an anthropic.organization.Invite", req.Inputs.UserID)
	}
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	out, err = userObservation(raw, req.Inputs.UserID)
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	if out.Role == string(req.Inputs.Role) {
		return infer.CreateResponse[UserState]{ID: out.UserID, Output: out}, nil
	}
	// Capture bytes rather than json.RawMessage: the latter asks the SDK to
	// decode JSON, conflating successful assignment with malformed observation.
	var body []byte
	_, err = c.Organization.Users.Update(ctx, out.UserID, sdk.OrganizationUserUpdateParams{Role: sdk.OrganizationUserUpdateParamsRole(req.Inputs.Role)}, option.WithMaxRetries(0), option.WithResponseBodyInto(&body))
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	out, err = userObservation(body, req.Inputs.UserID)
	if err != nil {
		return infer.CreateResponse[UserState]{}, err
	}
	return infer.CreateResponse[UserState]{ID: req.Inputs.UserID, Output: out}, nil
}

func (*User) Read(ctx context.Context, req infer.ReadRequest[UserArgs, UserState]) (infer.ReadResponse[UserArgs, UserState], error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.ReadResponse[UserArgs, UserState]{}, err
	}
	var raw json.RawMessage
	_, err = c.Organization.Users.Get(ctx, req.ID, option.WithMaxRetries(0), option.WithResponseBodyInto(&raw))
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[UserArgs, UserState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[UserArgs, UserState]{}, err
	}
	out, err := userObservation(raw, req.ID)
	if err != nil {
		return infer.ReadResponse[UserArgs, UserState]{}, err
	}
	return infer.ReadResponse[UserArgs, UserState]{ID: req.ID, Inputs: UserArgs{UserID: req.ID, Role: OrganizationRole(out.Role)}, State: out}, nil
}

func (*User) Update(ctx context.Context, req infer.UpdateRequest[UserArgs, UserState]) (infer.UpdateResponse[UserState], error) {
	if req.DryRun {
		return infer.UpdateResponse[UserState]{Output: UserState{UserID: req.Inputs.UserID, Role: string(req.Inputs.Role)}}, nil
	}
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	// Validate the response independently of the SDK's permissive decoder.
	var body []byte
	_, err = c.Organization.Users.Update(ctx, req.ID, sdk.OrganizationUserUpdateParams{Role: sdk.OrganizationUserUpdateParamsRole(req.Inputs.Role)}, option.WithMaxRetries(0), option.WithResponseBodyInto(&body))
	if err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	out, err := userObservation(body, req.ID)
	if err != nil {
		return infer.UpdateResponse[UserState]{}, err
	}
	return infer.UpdateResponse[UserState]{Output: out}, nil
}

func (*User) Delete(ctx context.Context, req infer.DeleteRequest[UserState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Admin)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Organization.Users.Remove(ctx, req.ID, option.WithMaxRetries(0))
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve validated Check inputs without copying their graph annotations to outputs.
type userResource struct{ infer.InferredResource }

func (r userResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	out, err := infer.Resource(&User{}).Check(ctx, req)
	if err != nil {
		return out, err
	}
	if v := req.Inputs.Get("userId"); v.IsString() && v.AsString() == "" {
		out.Failures = append(out.Failures, p.CheckFailure{Property: "userId", Reason: "must not be empty"})
	}
	if v := req.Inputs.Get("role"); v.IsString() {
		switch v.AsString() {
		case "user", "developer", "billing", "claude_code_user", "managed":
		default:
			out.Failures = append(out.Failures, p.CheckFailure{Property: "role", Reason: "must be an assignable organization role"})
		}
	}
	if len(out.Failures) == 0 {
		out.Inputs = req.Inputs
	}
	return out, err
}

func (r userResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&User{}).Diff(ctx, req)
}

func (r userResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&User{}).Create(ctx, req)
}

func (r userResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&User{}).Read(ctx, req)
}

func (r userResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&User{}).Update(ctx, req)
}

func (r userResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&User{}).Delete(ctx, req)
}

// Capture the generated service's raw response because its decoder coerces JSON
// primitives to strings. Validate each observation and never accept a foreign ID.
func userObservation(raw json.RawMessage, id string) (UserState, error) {
	out := UserState{UserID: id}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return out, err
	}
	str := func(k string) (string, bool) {
		var s string
		b := fields[k]
		ok := len(b) > 0 && b[0] == '"' && json.Unmarshal(b, &s) == nil
		return s, ok
	}
	observed, ok := str("id")
	if !ok || observed != id {
		return out, fmt.Errorf("invalid user response identity")
	}
	var r sdk.OrganizationUser
	decodeErr := json.Unmarshal(raw, &r)
	valid := true
	for _, field := range []struct {
		k    string
		dest **string
	}{{"email", &out.Email}, {"name", &out.Name}} {
		value, ok := str(field.k)
		if !ok {
			valid = false
		} else {
			*field.dest = &value
		}
	}
	out.Role, ok = str("role")
	valid = valid && ok
	switch out.Role {
	case "admin", "billing", "claude_code_user", "developer", "managed", "membership_admin", "owner", "primary_owner", "user":
	default:
		out.Role = ""
		valid = false
	}
	if _, ok := str("added_at"); ok && !r.AddedAt.IsZero() {
		value := r.AddedAt.Format(time.RFC3339Nano)
		out.AddedAt = &value
	} else {
		valid = false
	}
	typ, ok := str("type")
	valid = valid && ok && typ == "user"
	if decodeErr != nil {
		return out, decodeErr
	}
	if !valid {
		return out, fmt.Errorf("incomplete or invalid user response")
	}
	return out, nil
}
