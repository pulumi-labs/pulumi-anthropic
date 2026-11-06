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
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/rest"
)

// TunnelCertificate is a certificate authority an MCP tunnel trusts.
type TunnelCertificate struct {
	rest.Child[TunnelCertificateArgs, TunnelCertificateState, *TunnelCertificateState]
}

// NewTunnelCertificate returns the TunnelCertificate resource controller.
func NewTunnelCertificate() *TunnelCertificate {
	return &TunnelCertificate{rest.Child[TunnelCertificateArgs, TunnelCertificateState, *TunnelCertificateState]{
		Resource: rest.Resource[TunnelCertificateArgs, TunnelCertificateState, *TunnelCertificateState]{
			Collection: tunnelCollection,
			EndOfLife:  rest.Archive,
		},
		Name:     "certificates",
		ParentID: func(a *TunnelCertificateArgs) *string { return &a.TunnelID },
	}}
}

func (c *TunnelCertificate) Annotate(a infer.Annotator) {
	a.Describe(c, "A CA certificate for an MCP tunnel, used to verify the certificates of MCP "+
		"servers behind the tunnel. A tunnel holds at most two active certificates, which allows "+
		"rotation.\n\n"+
		"Certificates cannot be updated, so any change replaces the certificate. Deleting this "+
		"resource archives the certificate. Its ID is `<tunnelId>/<certificateId>`.")
}

type TunnelCertificateArgs struct {
	TunnelID         string `pulumi:"tunnelId"         provider:"replaceOnChanges" wire:"-"`
	CACertificatePem string `pulumi:"caCertificatePem" provider:"replaceOnChanges"`
}

func (c *TunnelCertificateArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.TunnelID, "The tunnel that trusts the certificate.")
	a.Describe(&c.CACertificatePem, "A single PEM-encoded CA certificate, up to 8,192 characters.")
}

type TunnelCertificateState struct {
	TunnelCertificateArgs
	Fingerprint string `pulumi:"fingerprint"`
	ExpiresAt   string `pulumi:"expiresAt"`
	CreatedAt   string `pulumi:"createdAt"`
}

func (s *TunnelCertificateState) Inputs() *TunnelCertificateArgs { return &s.TunnelCertificateArgs }

func (s *TunnelCertificateState) Annotate(a infer.Annotator) {
	a.Describe(&s.Fingerprint, "The certificate's SHA-256 fingerprint.")
	a.Describe(&s.ExpiresAt, "When the certificate expires, in RFC 3339 format.")
	a.Describe(&s.CreatedAt, "When the certificate was added, in RFC 3339 format.")
}

var _ infer.CustomRead[TunnelCertificateArgs, TunnelCertificateState] = (*TunnelCertificate)(nil)
