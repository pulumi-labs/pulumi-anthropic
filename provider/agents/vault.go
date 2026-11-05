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

// Vault holds credentials that Managed Agents sessions use to reach MCP servers and
// other services.
type Vault struct {
	rest.Resource[VaultArgs, VaultState, *VaultState]
}

// NewVault returns the Vault resource controller.
func NewVault() *Vault {
	return &Vault{rest.Resource[VaultArgs, VaultState, *VaultState]{
		Collection: vaultCollection,
		EndOfLife:  rest.Delete,
		Tombstones: map[string]any{"metadata": nil},
	}}
}

func (v *Vault) Annotate(a infer.Annotator) {
	a.Describe(v, "A Claude Managed Agents vault: a container for the credentials sessions use "+
		"to reach MCP servers and other services. Add credentials with `VaultCredential`, and "+
		"attach vaults to sessions or deployments by ID.\n\n"+
		"Deleting this resource deletes the vault and every credential in it.")
}

type VaultArgs struct {
	DisplayName string            `pulumi:"displayName"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
}

func (v *VaultArgs) Annotate(a infer.Annotator) {
	a.Describe(&v.DisplayName, "The vault's display name, 1 to 255 characters.")
	a.Describe(&v.Metadata, "Arbitrary key-value pairs.")
}

type VaultState struct {
	VaultArgs
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *VaultState) Inputs() *VaultArgs { return &s.VaultArgs }

func (s *VaultState) Annotate(a infer.Annotator) {
	a.Describe(&s.CreatedAt, "When the vault was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the vault was last updated, in RFC 3339 format.")
}
