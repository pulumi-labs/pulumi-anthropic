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

package agents

import (
	"context"
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Tunnel exposes MCP servers on a private network to Managed Agents.
type Tunnel struct {
	rest.Resource[TunnelArgs, TunnelState, *TunnelState]
}

// NewTunnel returns the Tunnel resource controller.
func NewTunnel() *Tunnel {
	return &Tunnel{rest.Resource[TunnelArgs, TunnelState, *TunnelState]{
		Collection: tunnelCollection,
		EndOfLife:  rest.Archive,
	}}
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

func (s *TunnelState) Inputs() *TunnelArgs { return &s.TunnelArgs }

func (s *TunnelState) Annotate(a infer.Annotator) {
	a.Describe(&s.Domain, "The tunnel's globally unique domain, which MCP server URLs use.")
	a.Describe(&s.TunnelToken, "The token the tunnel client authenticates with.")
	a.Describe(&s.CreatedAt, "When the tunnel was created, in RFC 3339 format.")
}

var (
	_ infer.CustomUpdate[TunnelArgs, TunnelState] = (*Tunnel)(nil)
	_ infer.CustomRead[TunnelArgs, TunnelState]   = (*Tunnel)(nil)
	_ infer.CustomDelete[TunnelState]             = (*Tunnel)(nil)
)

// Create creates the tunnel and reveals its token.
func (t *Tunnel) Create(ctx context.Context, req infer.CreateRequest[TunnelArgs]) (infer.CreateResponse[TunnelState], error) {
	resp, err := t.Resource.Create(ctx, req)
	if err != nil || req.DryRun {
		return resp, err
	}
	resp.Output.TunnelToken, err = t.revealToken(ctx, resp.ID)
	return resp, rest.InitFailed(err)
}

// Update reveals the token of a tunnel whose creation failed before it could, which is
// the only update a tunnel has: every input replaces it.
func (t *Tunnel) Update(
	ctx context.Context, req infer.UpdateRequest[TunnelArgs, TunnelState],
) (infer.UpdateResponse[TunnelState], error) {
	out := req.State
	out.TunnelArgs = req.Inputs
	if req.DryRun || out.TunnelToken != "" {
		return infer.UpdateResponse[TunnelState]{Output: out}, nil
	}
	token, err := t.revealToken(ctx, req.ID)
	out.TunnelToken = token
	return infer.UpdateResponse[TunnelState]{Output: out}, err
}

func (t *Tunnel) revealToken(ctx context.Context, id string) (string, error) {
	var raw []byte
	if err := t.Collection.Action(ctx, id, "reveal_token", &raw); err != nil {
		return "", fmt.Errorf("revealing the token of tunnel %s: %w", id, err)
	}
	var token struct {
		TunnelToken string `pulumi:"tunnelToken"`
	}
	err := rest.Decode(raw, &token)
	return token.TunnelToken, err
}

// Read refreshes the tunnel. The API returns the token only when asked to reveal it, so
// the token already known is kept, and a tunnel without one, as on import, reveals it.
func (t *Tunnel) Read(
	ctx context.Context, req infer.ReadRequest[TunnelArgs, TunnelState],
) (infer.ReadResponse[TunnelArgs, TunnelState], error) {
	resp, err := t.Resource.Read(ctx, req)
	if err != nil || resp.ID == "" {
		return resp, err
	}
	resp.State.TunnelToken = req.State.TunnelToken
	if resp.State.TunnelToken == "" {
		resp.State.TunnelToken, err = t.revealToken(ctx, resp.ID)
	}
	return resp, err
}
