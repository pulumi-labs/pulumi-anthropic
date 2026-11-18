// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/resourceutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// TunnelCertificate is a certificate authority an MCP tunnel trusts.
type TunnelCertificate struct{}

// NewTunnelCertificate returns the complete TunnelCertificate resource.
func NewTunnelCertificate() infer.InferredResource {
	return tunnelCertificateResource{infer.Resource(&TunnelCertificate{})}
}

func (c *TunnelCertificate) Annotate(a infer.Annotator) {
	a.Describe(c, "A CA certificate trusted by an MCP tunnel when connecting to MCP servers. A tunnel holds at most two active certificates. Replacement creates the new certificate before archiving the old one, so rotation requires a free certificate slot. Deleting the last active certificate stops MCP traffic until another is registered.\n\nCertificates cannot be updated; changing either input replaces the certificate. Deletion archives it. Import uses `<tunnelId>/<certificateId>` and cannot recover the original PEM; supplying a PEM after import replaces the certificate.")
}

type TunnelCertificateArgs struct {
	TunnelID         string  `pulumi:"tunnelId" provider:"replaceOnChanges"`
	CACertificatePem *string `pulumi:"caCertificatePem,optional" provider:"replaceOnChanges"`
}

func (c *TunnelCertificateArgs) Annotate(a infer.Annotator) {
	a.Describe(&c.TunnelID, "The tunnel that trusts the certificate. Changing it replaces the certificate.")
	a.Describe(&c.CACertificatePem, "A single PEM-encoded X.509 CA certificate, at most 8,192 bytes, with no private-key material. Required when creating a certificate. The original bytes are retained on refresh but cannot be recovered on import. Changing or removing this value replaces the certificate.")
}

type TunnelCertificateState struct {
	TunnelCertificateArgs
	Fingerprint string                   `pulumi:"fingerprint"`
	ExpiresAt   *string                  `pulumi:"expiresAt,optional"`
	CreatedAt   string                   `pulumi:"createdAt"`
	Status      *TunnelCertificateStatus `pulumi:"status,optional"`
}

// TunnelCertificateStatus describes a confirmed registration's trust status.
type TunnelCertificateStatus string

func (s *TunnelCertificateStatus) Annotate(a infer.Annotator) {
	a.Describe(s, "The confirmed trust status of a tunnel certificate registration.")
}

func (TunnelCertificateStatus) Values() []infer.EnumValue[TunnelCertificateStatus] {
	return []infer.EnumValue[TunnelCertificateStatus]{
		{Name: "Active", Value: "active", Description: "The certificate is in the tunnel's trusted set."},
		{Name: "Archived", Value: "archived", Description: "The certificate has been archived and is no longer trusted."},
	}
}

func (s *TunnelCertificateState) Annotate(a infer.Annotator) {
	a.Describe(&s.Fingerprint, "The lowercase hexadecimal SHA-256 fingerprint of the certificate's DER encoding.")
	a.Describe(&s.ExpiresAt, "When the certificate expires, in RFC 3339 format. Absent when the certificate does not expire.")
	a.Describe(&s.CreatedAt, "When the certificate was added, in RFC 3339 format.")
	a.Describe(&s.Status, "Whether the confirmed certificate registration is active or archived. Unavailable when the registration's identity or trust status cannot be confirmed. Active registration does not verify MCP server reachability.")
}

func (*TunnelCertificate) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[TunnelCertificateArgs], error) {
	args, failures, err := infer.DefaultCheck[TunnelCertificateArgs](ctx, req.NewInputs)
	v := req.NewInputs.Get("tunnelId")
	if v.IsString() && (v.AsString() == "" || strings.Contains(v.AsString(), "/")) {
		failures = append(failures, p.CheckFailure{Property: "tunnelId", Reason: "must be a nonempty tunnel ID without slashes"})
	}
	v = req.NewInputs.Get("caCertificatePem")
	if v.IsString() {
		if e := certificatePEMError(v.AsString()); e != nil {
			failures = append(failures, p.CheckFailure{Property: "caCertificatePem", Reason: e.Error()})
		}
	}
	return infer.CheckResponse[TunnelCertificateArgs]{Inputs: args, Failures: failures}, err
}

func (*TunnelCertificate) Create(ctx context.Context, req infer.CreateRequest[TunnelCertificateArgs]) (infer.CreateResponse[TunnelCertificateState], error) {
	if req.DryRun {
		return infer.CreateResponse[TunnelCertificateState]{Output: TunnelCertificateState{TunnelCertificateArgs: req.Inputs}}, nil
	}
	if req.Inputs.CACertificatePem == nil {
		return infer.CreateResponse[TunnelCertificateState]{}, fmt.Errorf("caCertificatePem is required when creating a certificate")
	}
	if err := certificatePEMError(*req.Inputs.CACertificatePem); err != nil {
		return infer.CreateResponse[TunnelCertificateState]{}, err
	}
	if req.Inputs.TunnelID == "" || strings.Contains(req.Inputs.TunnelID, "/") {
		return infer.CreateResponse[TunnelCertificateState]{}, fmt.Errorf("invalid tunnelId")
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[TunnelCertificateState]{}, err
	}
	r, err := c.Beta.Tunnels.Certificates.New(ctx, req.Inputs.TunnelID, anthropic.BetaTunnelCertificateNewParams{CACertificatePEM: *req.Inputs.CACertificatePem}, option.WithMaxRetries(0))
	if err != nil {
		return infer.CreateResponse[TunnelCertificateState]{}, err
	}
	s, err := certificateState(r, req.Inputs)
	if r == nil || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID == "" || strings.Contains(r.ID, "/") {
		return infer.CreateResponse[TunnelCertificateState]{}, err
	}
	// Do not attach a foreign parent's observation to the requested parent.
	if apiutil.JSONString(r.JSON.TunnelID.Raw()) && r.TunnelID != req.Inputs.TunnelID {
		return infer.CreateResponse[TunnelCertificateState]{}, fmt.Errorf("certificate response parent mismatch")
	}
	if err != nil {
		return infer.CreateResponse[TunnelCertificateState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.CreateResponse[TunnelCertificateState]{}, fmt.Errorf("new certificate is already archived")
	}
	return infer.CreateResponse[TunnelCertificateState]{ID: req.Inputs.TunnelID + "/" + r.ID, Output: s}, nil
}

func (*TunnelCertificate) Read(ctx context.Context, req infer.ReadRequest[TunnelCertificateArgs, TunnelCertificateState]) (infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState], error) {
	parent, id, err := certificateID(req.ID)
	if err != nil {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, err
	}
	r, err := c.Beta.Tunnels.Certificates.Get(ctx, id, anthropic.BetaTunnelCertificateGetParams{TunnelID: parent})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, err
	}
	args := TunnelCertificateArgs{TunnelID: parent, CACertificatePem: req.Inputs.CACertificatePem}
	s, err := certificateState(r, args)
	if err != nil {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, err
	}
	if r.ID != id {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, fmt.Errorf("certificate response identity mismatch")
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{}, nil
	}
	return infer.ReadResponse[TunnelCertificateArgs, TunnelCertificateState]{ID: req.ID, Inputs: args, State: s}, nil
}

// No update operation exists; the engine owns refresh.
func (*TunnelCertificate) Update(_ context.Context, req infer.UpdateRequest[TunnelCertificateArgs, TunnelCertificateState]) (infer.UpdateResponse[TunnelCertificateState], error) {
	s := req.State
	if req.DryRun {
		s.TunnelCertificateArgs = req.Inputs
		return infer.UpdateResponse[TunnelCertificateState]{Output: s}, nil
	}
	return infer.UpdateResponse[TunnelCertificateState]{Output: s}, nil
}

func (*TunnelCertificate) Delete(ctx context.Context, req infer.DeleteRequest[TunnelCertificateState]) (infer.DeleteResponse, error) {
	parent, id, err := certificateID(req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.Tunnels.Certificates.Archive(ctx, id, anthropic.BetaTunnelCertificateArchiveParams{TunnelID: parent})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// infer v1.5 loses output dependencies in Check.
type tunnelCertificateResource struct{ infer.InferredResource }

func (r tunnelCertificateResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&TunnelCertificate{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r tunnelCertificateResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&TunnelCertificate{}).Diff(ctx, req)
}

func (r tunnelCertificateResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&TunnelCertificate{}).Create(ctx, req)
}

func (r tunnelCertificateResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&TunnelCertificate{}).Read(ctx, req)
	return resp, err
}

func (r tunnelCertificateResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&TunnelCertificate{}).Update(ctx, req)
}

func (r tunnelCertificateResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&TunnelCertificate{}).Delete(ctx, req)
}

func certificatePEMError(value string) error {
	if len(value) > 8192 {
		return fmt.Errorf("must contain at most 8,192 bytes")
	}
	// Decode accepts leading arbitrary text; reject it rather than hiding private material.
	if !strings.HasPrefix(strings.TrimSpace(value), "-----BEGIN CERTIFICATE-----") || strings.Count(value, "-----BEGIN ") != 1 || strings.Count(value, "-----END ") != 1 {
		return fmt.Errorf("must contain exactly one PEM-encoded X.509 CA certificate and no other material")
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
		return fmt.Errorf("must contain exactly one PEM-encoded X.509 CA certificate and no other material")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.BasicConstraintsValid || !cert.IsCA {
		return fmt.Errorf("must contain a valid X.509 CA certificate")
	}
	return nil
}

func certificateID(id string) (string, string, error) {
	tunnelID, certificateID, ok := resourceutil.SplitID(id)
	if !ok {
		return "", "", fmt.Errorf("expected certificate ID <tunnelId>/<certificateId>")
	}
	return tunnelID, certificateID, nil
}

func certificateState(r *anthropic.BetaTunnelCertificate, args TunnelCertificateArgs) (TunnelCertificateState, error) {
	s := TunnelCertificateState{TunnelCertificateArgs: args}
	if r == nil {
		return s, fmt.Errorf("missing certificate response")
	}
	fingerprint, e := hex.DecodeString(r.Fingerprint)
	validFingerprint := apiutil.JSONString(r.JSON.Fingerprint.Raw()) && e == nil && len(fingerprint) == 32 && strings.ToLower(r.Fingerprint) == r.Fingerprint
	if validFingerprint {
		s.Fingerprint = r.Fingerprint
	}
	if r.JSON.CreatedAt.Valid() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if r.JSON.ExpiresAt.Valid() {
		expires := r.ExpiresAt.Format(time.RFC3339Nano)
		s.ExpiresAt = &expires
	}
	if apiutil.JSONString(r.JSON.TunnelID.Raw()) && r.TunnelID == args.TunnelID && apiutil.JSONString(r.JSON.ID.Raw()) && r.ID != "" && !strings.Contains(r.ID, "/") && (r.JSON.ArchivedAt.Valid() || r.JSON.ArchivedAt.Raw() == "null") {
		status := TunnelCertificateStatus("active")
		if !r.ArchivedAt.IsZero() {
			status = "archived"
		}
		s.Status = &status
	}
	if !validFingerprint || s.CreatedAt == "" || (!r.JSON.ExpiresAt.Valid() && r.JSON.ExpiresAt.Raw() != "null") || (!r.JSON.ArchivedAt.Valid() && r.JSON.ArchivedAt.Raw() != "null") || !apiutil.JSONString(r.JSON.TunnelID.Raw()) || r.TunnelID != args.TunnelID || !apiutil.JSONString(r.JSON.ID.Raw()) || r.ID == "" || strings.Contains(r.ID, "/") {
		return s, fmt.Errorf("incomplete or invalid certificate response")
	}
	return s, nil
}
