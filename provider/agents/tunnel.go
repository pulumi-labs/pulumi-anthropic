// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

type Tunnel struct{}

// NewTunnel returns the complete Tunnel resource.
func NewTunnel() infer.InferredResource {
	return tunnelResource{infer.Resource(&Tunnel{})}
}

func (t *Tunnel) Annotate(a infer.Annotator) {
	a.Describe(t, "An MCP tunnel, a research preview: a connection through which Managed Agents "+
		"reach MCP servers on a private network, without exposing them to the internet. Run the "+
		"tunnel client on the private network with the `tunnelToken` output, and trust its "+
		"certificate authority with `TunnelCertificate`.\n\n"+
		"Tunnels cannot be updated, so changing the display name replaces the tunnel, which "+
		"assigns a new domain and token. Deleting this resource archives the tunnel, which "+
		"invalidates its token and archives its certificates; it cannot be undone.")
}

type TunnelArgs struct {
	DisplayName *string `pulumi:"displayName,optional" provider:"replaceOnChanges"`
}

func (t *TunnelArgs) Annotate(a infer.Annotator) {
	a.Describe(&t.DisplayName, "The tunnel's display name, 1 to 255 characters.")
}

type TunnelState struct {
	TunnelArgs
	Domain      string `pulumi:"domain"`
	TunnelToken string `pulumi:"tunnelToken" provider:"secret"`
	CreatedAt   string `pulumi:"createdAt"`
}

func (s *TunnelState) Annotate(a infer.Annotator) {
	a.Describe(&s.Domain, "The tunnel's globally unique domain, which MCP server URLs use.")
	a.Describe(&s.TunnelToken, "The token the tunnel client authenticates with.")
	a.Describe(&s.CreatedAt, "When the tunnel was created, in RFC 3339 format.")
}

func (*Tunnel) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[TunnelArgs], error) {
	args, failures, err := infer.DefaultCheck[TunnelArgs](ctx, req.NewInputs)
	v := req.NewInputs.Get("displayName")
	if !v.IsComputed() && v.IsString() {
		if n := utf8.RuneCountInString(v.AsString()); n < 1 || n > 255 {
			failures = append(failures, p.CheckFailure{Property: "displayName", Reason: "must contain 1–255 characters"})
		}
	}
	return infer.CheckResponse[TunnelArgs]{Inputs: args, Failures: failures}, err
}

func (*Tunnel) Create(ctx context.Context, req infer.CreateRequest[TunnelArgs]) (infer.CreateResponse[TunnelState], error) {
	if req.DryRun {
		return infer.CreateResponse[TunnelState]{Output: TunnelState{TunnelArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[TunnelState]{}, err
	}
	params := anthropic.BetaTunnelNewParams{}
	if req.Inputs.DisplayName != nil {
		params.DisplayName = anthropic.String(*req.Inputs.DisplayName)
	}
	// Allocation is documented as non-idempotent. An ambiguous failure needs manual reconciliation.
	r, err := c.Beta.Tunnels.New(ctx, params, option.WithMaxRetries(0))
	if err != nil {
		return infer.CreateResponse[TunnelState]{}, err
	}
	s, err := tunnelState(r)
	if err != nil {
		return infer.CreateResponse[TunnelState]{}, err
	}
	s.TunnelToken, err = tunnelReveal(ctx, c, r.ID)
	if err != nil {
		return infer.CreateResponse[TunnelState]{}, err
	}
	return infer.CreateResponse[TunnelState]{ID: r.ID, Output: s}, nil
}

func (*Tunnel) Read(ctx context.Context, req infer.ReadRequest[TunnelArgs, TunnelState]) (infer.ReadResponse[TunnelArgs, TunnelState], error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, err
	}
	r, err := c.Beta.Tunnels.Get(ctx, req.ID, anthropic.BetaTunnelGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, err
	}
	s, err := tunnelState(r)
	if err != nil {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, err
	}
	if r.ID != req.ID {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, fmt.Errorf("tunnel response identity mismatch")
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, nil
	}
	s.TunnelToken, err = tunnelReveal(ctx, c, req.ID)
	if err != nil {
		return infer.ReadResponse[TunnelArgs, TunnelState]{}, err
	}
	return infer.ReadResponse[TunnelArgs, TunnelState]{ID: req.ID, Inputs: s.TunnelArgs, State: s}, nil
}

// There is no tunnel update operation; refresh obtains current observations and token.
func (*Tunnel) Update(_ context.Context, req infer.UpdateRequest[TunnelArgs, TunnelState]) (infer.UpdateResponse[TunnelState], error) {
	s := req.State
	if req.DryRun {
		s.TunnelArgs = req.Inputs
	}
	return infer.UpdateResponse[TunnelState]{Output: s}, nil
}

func (*Tunnel) Delete(ctx context.Context, req infer.DeleteRequest[TunnelState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.Tunnels.Archive(ctx, req.ID, anthropic.BetaTunnelArchiveParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

func (*Tunnel) WireDependencies(f infer.FieldSelector, a *TunnelArgs, s *TunnelState) {
	f.OutputField(&s.TunnelToken).AlwaysSecret()
}

// Narrow infer v1.5 accommodations: Check loses output dependencies and Read
// does not apply secret wiring.
type tunnelResource struct{ infer.InferredResource }

func (r tunnelResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&Tunnel{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r tunnelResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&Tunnel{}).Diff(ctx, req)
}

func (r tunnelResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	resp, err := infer.Resource(&Tunnel{}).Create(ctx, req)
	if err == nil {
		resp.Properties = tunnelOutput(resp.Properties)
	}
	return resp, err
}

func (r tunnelResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&Tunnel{}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		resp.Properties = tunnelOutput(resp.Properties)
	}
	return resp, err
}

func (r tunnelResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&Tunnel{}).Update(ctx, req)
}

func (r tunnelResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Tunnel{}).Delete(ctx, req)
}

func tunnelOutput(m property.Map) property.Map {
	if !m.Get("tunnelToken").IsNull() {
		m = m.Set("tunnelToken", m.Get("tunnelToken").WithSecret(true))
	}
	return m
}

func tunnelState(r *anthropic.BetaTunnel) (TunnelState, error) {
	s := TunnelState{}
	if r == nil {
		return s, fmt.Errorf("missing tunnel response")
	}
	if r.JSON.DisplayName.Valid() && apiutil.JSONString(r.JSON.DisplayName.Raw()) {
		name := r.DisplayName
		s.DisplayName = &name
	}
	if r.JSON.Domain.Valid() && apiutil.JSONString(r.JSON.Domain.Raw()) {
		s.Domain = r.Domain
	}
	if r.JSON.CreatedAt.Valid() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if !r.JSON.ID.Valid() || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID == "" || s.Domain == "" || s.CreatedAt == "" ||
		(!apiutil.JSONString(r.JSON.DisplayName.Raw()) && r.JSON.DisplayName.Raw() != "null") ||
		(!r.JSON.ArchivedAt.Valid() && r.JSON.ArchivedAt.Raw() != "null") {
		return s, fmt.Errorf("incomplete or invalid tunnel response")
	}
	return s, nil
}

func tunnelReveal(ctx context.Context, c *anthropic.Client, id string) (string, error) {
	r, err := c.Beta.Tunnels.RevealToken(ctx, id, anthropic.BetaTunnelRevealTokenParams{})
	if err != nil {
		return "", fmt.Errorf("revealing tunnel token: %w", err)
	}
	if r == nil || !r.JSON.TunnelToken.Valid() || !apiutil.JSONString(r.JSON.TunnelToken.Raw()) || r.TunnelToken == "" {
		return "", fmt.Errorf("missing tunnel token")
	}
	return r.TunnelToken, nil
}
