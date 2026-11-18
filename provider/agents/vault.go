// Copyright 2026, Pulumi Corporation.
// SPDX-License-Identifier: Apache-2.0

package agents

import (
	"context"
	"fmt"
	"maps"
	"time"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// Vault holds credential-container configuration, not child credentials.
type Vault struct{}

// NewVault returns the complete Vault resource.
func NewVault() infer.InferredResource {
	return vaultResource{infer.Resource(&Vault{})}
}

func (v *Vault) Annotate(a infer.Annotator) {
	a.Describe(v, "A workspace-scoped Claude Managed Agents credential container. Add credentials with VaultCredential and reference the vault by ID. Deleting this resource permanently deletes the vault; it does not archive it. Archived vaults are treated as deleted.")
}

type VaultArgs struct {
	DisplayName string            `pulumi:"displayName"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
}

func (v *VaultArgs) Annotate(a infer.Annotator) {
	a.Describe(&v.DisplayName, "The mutable vault display name, 1 to 255 characters. The Pulumi logical name is not used for autonaming.")
	a.Describe(&v.Metadata, "At most 16 key-value pairs; keys up to 64 characters and values up to 512 characters. Removing an entry from your configuration deletes that entry. Empty string values are retained.")
}

type VaultState struct {
	VaultArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *VaultState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the vault was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the vault was last updated, in RFC 3339 format.")
}

func (*Vault) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[VaultArgs], error) {
	args, failures, err := infer.DefaultCheck[VaultArgs](ctx, req.NewInputs)
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	if v := req.NewInputs.Get("displayName"); !v.IsComputed() && v.IsString() {
		if n := utf8.RuneCountInString(v.AsString()); n < 1 || n > 255 {
			fail("displayName", "must contain 1–255 characters")
		}
	}
	if v := req.NewInputs.Get("metadata"); !v.IsComputed() && v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 pairs")
		}
		for k, value := range v.AsMap().All {
			if utf8.RuneCountInString(k) > 64 {
				fail("metadata", "keys must contain at most 64 characters")
			}
			if !value.IsComputed() && value.IsString() && utf8.RuneCountInString(value.AsString()) > 512 {
				fail("metadata", "values must contain at most 512 characters")
			}
		}
	}
	return infer.CheckResponse[VaultArgs]{Inputs: args, Failures: failures}, err
}

func (*Vault) Create(ctx context.Context, req infer.CreateRequest[VaultArgs]) (infer.CreateResponse[VaultState], error) {
	if req.DryRun {
		return infer.CreateResponse[VaultState]{Output: VaultState{VaultArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[VaultState]{}, err
	}
	r, err := c.Beta.Vaults.New(ctx, vaultNew(req.Inputs))
	if err != nil {
		return infer.CreateResponse[VaultState]{}, err
	}
	s, err := vaultState(r, req.Inputs, false)
	if err != nil {
		return infer.CreateResponse[VaultState]{}, err
	}
	return infer.CreateResponse[VaultState]{ID: r.ID, Output: s}, nil
}

func (*Vault) Read(ctx context.Context, req infer.ReadRequest[VaultArgs, VaultState]) (infer.ReadResponse[VaultArgs, VaultState], error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[VaultArgs, VaultState]{}, err
	}
	r, err := c.Beta.Vaults.Get(ctx, req.ID, anthropic.BetaVaultGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[VaultArgs, VaultState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[VaultArgs, VaultState]{}, err
	}
	s, err := vaultState(r, req.Inputs, req.Inputs.DisplayName == "")
	if err != nil {
		return infer.ReadResponse[VaultArgs, VaultState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[VaultArgs, VaultState]{}, nil
	}
	return infer.ReadResponse[VaultArgs, VaultState]{ID: req.ID, Inputs: s.VaultArgs, State: s}, nil
}

func (*Vault) Update(ctx context.Context, req infer.UpdateRequest[VaultArgs, VaultState]) (infer.UpdateResponse[VaultState], error) {
	if req.DryRun {
		return infer.UpdateResponse[VaultState]{Output: VaultState{VaultArgs: req.Inputs}}, nil
	}
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[VaultState]{}, err
	}
	r, err := c.Beta.Vaults.Update(ctx, req.ID, vaultUpdate(req.State.VaultArgs, req.Inputs))
	if err != nil {
		return infer.UpdateResponse[VaultState]{}, err
	}
	s, err := vaultState(r, req.Inputs, false)
	if err != nil {
		return infer.UpdateResponse[VaultState]{}, err
	}
	return infer.UpdateResponse[VaultState]{Output: s}, nil
}

func (*Vault) Delete(ctx context.Context, req infer.DeleteRequest[VaultState]) (infer.DeleteResponse, error) {
	c, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = c.Beta.Vaults.Delete(ctx, req.ID, anthropic.BetaVaultDeleteParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// infer v1.5 Check encoding drops output dependencies. Vault has no defaults or
// normalization: its validated original bag is already canonical.
type vaultResource struct{ infer.InferredResource }

func (r vaultResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	resp, err := infer.Resource(&Vault{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r vaultResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&Vault{}).Diff(ctx, req)
}

func (r vaultResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	return infer.Resource(&Vault{}).Create(ctx, req)
}

func (r vaultResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	return infer.Resource(&Vault{}).Read(ctx, req)
}

func (r vaultResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	return infer.Resource(&Vault{}).Update(ctx, req)
}

func (r vaultResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Vault{}).Delete(ctx, req)
}

func vaultNew(a VaultArgs) anthropic.BetaVaultNewParams {
	return anthropic.BetaVaultNewParams{DisplayName: a.DisplayName, Metadata: maps.Clone(a.Metadata)}
}

func vaultUpdate(old, desired VaultArgs) anthropic.BetaVaultUpdateParams {
	r := anthropic.BetaVaultUpdateParams{DisplayName: anthropic.String(desired.DisplayName)}
	// The API merges maps. Old state is used only to identify deletions, never to
	// suppress desired values or decide whether an engine-requested Update runs.
	patch := apiutil.MergedStringMap(old.Metadata, desired.Metadata)
	r.SetExtraFields(map[string]any{"metadata": patch})
	return r
}

func vaultState(r *anthropic.BetaManagedAgentsVault, prior VaultArgs, importing bool) (VaultState, error) {
	s := VaultState{}
	if r == nil {
		return s, fmt.Errorf("missing vault response")
	}
	if r.JSON.DisplayName.Valid() {
		s.DisplayName = r.DisplayName
	}
	if r.JSON.Metadata.Valid() && (importing || prior.Metadata != nil || len(r.Metadata) > 0) {
		s.Metadata = maps.Clone(r.Metadata)
	}
	if r.JSON.CreatedAt.Valid() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	if r.JSON.UpdatedAt.Valid() {
		s.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "" && raw != "null" && !r.JSON.ArchivedAt.Valid() {
		return s, fmt.Errorf("invalid vault archive timestamp")
	}
	if !r.JSON.ID.Valid() || r.ID == "" || !r.JSON.DisplayName.Valid() || !r.JSON.Metadata.Valid() || !r.JSON.CreatedAt.Valid() || !r.JSON.UpdatedAt.Valid() {
		return s, fmt.Errorf("incomplete vault response")
	}
	return s, nil
}
