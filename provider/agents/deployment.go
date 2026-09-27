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
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Deployment runs an agent in an environment, on a schedule or on demand.
type Deployment struct {
	rest.Resource[DeploymentArgs, DeploymentState, *DeploymentState]
}

// NewDeployment returns the Deployment resource controller.
func NewDeployment() *Deployment {
	return &Deployment{rest.Resource[DeploymentArgs, DeploymentState, *DeploymentState]{
		Collection: deploymentCollection,
		EndOfLife:  rest.Archive,
		Tombstones: map[string]any{"metadata": nil},
	}}
}

func (d *Deployment) Annotate(a infer.Annotator) {
	a.Describe(d, "A Claude Managed Agents deployment: an agent, an environment, and the events "+
		"that start each session, run on a cron schedule or on demand.\n\n"+
		"A deployment runs one version of its agent. Set `agent.version` to the agent's "+
		"`version` output to roll the deployment forward with every change to the agent, or to "+
		"a fixed number to hold it back: updating the agent creates a new version, and "+
		"deployments move to it one at a time as their `agent.version` changes.\n\n"+
		"The API cannot delete deployments, so deleting this resource archives the deployment, "+
		"which stops its schedule and cannot be undone.")
}

type DeploymentArgs struct {
	Name          string               `pulumi:"name"`
	Description   *string              `pulumi:"description,optional"`
	Agent         AgentReference       `pulumi:"agent"`
	EnvironmentID string               `pulumi:"environmentId"`
	InitialEvents []map[string]any     `pulumi:"initialEvents"`
	Resources     []DeploymentResource `pulumi:"resources,optional"`
	VaultIDs      []string             `pulumi:"vaultIds,optional"`
	Schedule      *Schedule            `pulumi:"schedule,optional"`
	Budget        *Budget              `pulumi:"budget,optional"`
	Paused        *bool                `pulumi:"paused,optional"   wire:"-"`
	Metadata      map[string]string    `pulumi:"metadata,optional"`
}

func (d *DeploymentArgs) Annotate(a infer.Annotator) {
	a.Describe(&d.Name, "The deployment's name.")
	a.Describe(&d.Description, "A description of the deployment.")
	a.Describe(&d.Agent, "The agent to run, and optionally the version to pin.")
	a.Describe(&d.EnvironmentID, "The environment sessions run in.")
	a.Describe(&d.InitialEvents, "The events that start each session, 1 to 50, in the API's event "+
		"format. For example: `{type: \"user.message\", content: [{type: \"text\", text: \"Triage "+
		"new issues.\"}]}`.")
	a.Describe(&d.Resources, "Repositories, files, and memory stores mounted into each session, up to 500.")
	a.Describe(&d.VaultIDs, "Vaults whose credentials sessions can use, up to 50.")
	a.Describe(&d.Schedule, "When to run. Omit to run only on demand.")
	a.Describe(&d.Budget, "A spending limit for the deployment.")
	a.Describe(&d.Paused, "Whether the deployment is paused. Omit to leave pausing to other tools.")
	a.Describe(&d.Metadata, "Arbitrary key-value pairs.")
}

type DeploymentState struct {
	DeploymentArgs
	Status    string `pulumi:"status"`
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *DeploymentState) Inputs() *DeploymentArgs { return &s.DeploymentArgs }

func (s *DeploymentState) Annotate(a infer.Annotator) {
	a.Describe(&s.Status, "Whether the deployment is `active` or `paused`. The platform also "+
		"pauses a deployment when it cannot run, for example because its agent was archived.")
	a.Describe(&s.CreatedAt, "When the deployment was created, in RFC 3339 format.")
	a.Describe(&s.UpdatedAt, "When the deployment was last updated, in RFC 3339 format.")
}

// AgentReference is an agent, optionally pinned to a version.
type AgentReference struct {
	ID      string `pulumi:"id"`
	Version *int   `pulumi:"version,optional"`
}

func (r *AgentReference) Annotate(a infer.Annotator) {
	a.Describe(&r.ID, "The agent's ID.")
	a.Describe(&r.Version, "The agent version to run. If omitted, the version that is latest "+
		"when the deployment is created or updated is pinned; later versions are not picked up.")
}

// MarshalJSON sends a reference without a version as the bare agent ID, which the API
// resolves to the agent's latest version, and one with a version as an agent object.
func (r AgentReference) MarshalJSON() ([]byte, error) {
	if r.Version == nil {
		return json.Marshal(r.ID)
	}
	return json.Marshal(map[string]any{"type": "agent", "id": r.ID, "version": *r.Version})
}

// UnmarshalJSON accepts either form. The API always returns the object form, with the
// version it resolved.
func (r *AgentReference) UnmarshalJSON(raw []byte) error {
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		*r = AgentReference{ID: id}
		return nil
	}
	var obj struct {
		ID      string `json:"id"`
		Version *int   `json:"version"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("expected an agent ID or object: %w", err)
	}
	*r = AgentReference{ID: obj.ID, Version: obj.Version}
	return nil
}

// DeploymentResourceType discriminates the kinds of session resource.
type DeploymentResourceType string

func (DeploymentResourceType) Values() []infer.EnumValue[DeploymentResourceType] {
	return []infer.EnumValue[DeploymentResourceType]{
		{Value: "github_repository", Description: "A GitHub repository, cloned into the sandbox."},
		{Value: "file", Description: "An uploaded file, mounted into the sandbox."},
		{Value: "memory_store", Description: "A memory store, mounted into the sandbox."},
	}
}

// DeploymentResource is mounted into each of a deployment's sessions. Which fields apply
// depends on the type.
type DeploymentResource struct {
	Type               DeploymentResourceType `pulumi:"type"`
	URL                *string                `pulumi:"url,optional"`
	Checkout           *Checkout              `pulumi:"checkout,optional"`
	AuthorizationToken *string                `pulumi:"authorizationToken,optional" provider:"secret"`
	FileID             *string                `pulumi:"fileId,optional"`
	MemoryStoreID      *string                `pulumi:"memoryStoreId,optional"`
	Access             *MemoryAccess          `pulumi:"access,optional"`
	Instructions       *string                `pulumi:"instructions,optional"`
	MountPath          *string                `pulumi:"mountPath,optional"`
}

func (r *DeploymentResource) Annotate(a infer.Annotator) {
	a.Describe(&r.Type, "The kind of resource.")
	a.Describe(&r.URL, "The repository's URL. Required for `github_repository`.")
	a.Describe(&r.Checkout, "The branch or commit to check out. For `github_repository`; "+
		"defaults to the default branch.")
	a.Describe(&r.AuthorizationToken, "A token that can read the repository. For `github_repository`.")
	a.Describe(&r.FileID, "The file's ID. Required for `file`.")
	a.Describe(&r.MemoryStoreID, "The memory store's ID. Required for `memory_store`.")
	a.Describe(&r.Access, "Whether the agent can write to the memory store. For `memory_store`.")
	a.Describe(&r.Instructions, "How the agent should use the memory store. For `memory_store`.")
	a.Describe(&r.MountPath, "Where the resource is mounted in the sandbox. For `github_repository` and `file`.")
}

// Checkout is the branch or commit of a repository to check out.
type Checkout struct {
	Type CheckoutType `pulumi:"type"`
	Name *string      `pulumi:"name,optional"`
	Sha  *string      `pulumi:"sha,optional"`
}

func (c *Checkout) Annotate(a infer.Annotator) {
	a.Describe(&c.Type, "Whether to check out a branch or a commit.")
	a.Describe(&c.Name, "The branch. Required for `branch`.")
	a.Describe(&c.Sha, "The commit SHA. Required for `commit`.")
}

// CheckoutType discriminates branch from commit checkouts.
type CheckoutType string

func (CheckoutType) Values() []infer.EnumValue[CheckoutType] {
	return []infer.EnumValue[CheckoutType]{{Value: "branch"}, {Value: "commit"}}
}

// MemoryAccess is an agent's access to a memory store.
type MemoryAccess string

func (MemoryAccess) Values() []infer.EnumValue[MemoryAccess] {
	return []infer.EnumValue[MemoryAccess]{{Value: "read_write"}, {Value: "read_only"}}
}

// Schedule runs a deployment on a cron schedule.
type Schedule struct {
	Expression string `pulumi:"expression"`
	Timezone   string `pulumi:"timezone"`
}

func (s *Schedule) Annotate(a infer.Annotator) {
	a.Describe(&s.Expression, "A cron expression, such as `0 9 * * 1-5`.")
	a.Describe(&s.Timezone, "The IANA time zone the expression is evaluated in, such as `America/New_York`.")
}

func (Schedule) ExtendWire(obj map[string]any) { obj["type"] = "cron" }

// Budget limits what a deployment may spend.
type Budget struct {
	MaxListCost MonetaryAmount `pulumi:"maxListCost"`
}

func (b *Budget) Annotate(a infer.Annotator) {
	a.Describe(&b.MaxListCost, "The most the deployment may spend, at list price.")
}

func (Budget) ExtendWire(obj map[string]any) { obj["type"] = "limit" }

// MonetaryAmount is an amount of money.
type MonetaryAmount struct {
	Amount   string `pulumi:"amount"`
	Currency string `pulumi:"currency"`
}

func (m *MonetaryAmount) Annotate(a infer.Annotator) {
	a.Describe(&m.Amount, "The amount in the currency's minor units, as an integer string: "+
		"`2500` is $25.00.")
	a.Describe(&m.Currency, "The ISO 4217 currency code. Only `USD` is supported.")
}

var (
	_ infer.CustomUpdate[DeploymentArgs, DeploymentState] = (*Deployment)(nil)
	_ infer.CustomRead[DeploymentArgs, DeploymentState]   = (*Deployment)(nil)
	_ infer.CustomDelete[DeploymentState]                 = (*Deployment)(nil)
)

// Create creates the deployment, then pauses it if it is declared paused.
func (d *Deployment) Create(
	ctx context.Context, req infer.CreateRequest[DeploymentArgs],
) (infer.CreateResponse[DeploymentState], error) {
	resp, err := d.Resource.Create(ctx, req)
	if err != nil || req.DryRun {
		return resp, err
	}
	resp.Output, err = d.setPaused(ctx, resp.ID, resp.Output)
	return resp, rest.InitFailed(err)
}

// Update applies the changed configuration, then pauses or unpauses the deployment.
func (d *Deployment) Update(
	ctx context.Context, req infer.UpdateRequest[DeploymentArgs, DeploymentState],
) (infer.UpdateResponse[DeploymentState], error) {
	resp, err := d.Resource.Update(ctx, req)
	if err != nil || req.DryRun {
		return resp, err
	}
	resp.Output, err = d.setPaused(ctx, req.ID, resp.Output)
	return resp, err
}

// Read refreshes the deployment, reporting whether it is paused if that is managed.
func (d *Deployment) Read(
	ctx context.Context, req infer.ReadRequest[DeploymentArgs, DeploymentState],
) (infer.ReadResponse[DeploymentArgs, DeploymentState], error) {
	resp, err := d.Resource.Read(ctx, req)
	if err != nil || resp.ID == "" {
		return resp, err
	}
	// The status is reported when pausing is declared, or on import, which declares nothing.
	if req.State.Paused != nil || rest.IsImport(req) {
		paused := resp.State.Status == "paused"
		resp.Inputs.Paused = &paused
		resp.State.Paused = &paused
	}
	return resp, nil
}

// setPaused pauses or unpauses the deployment to match the declared state, if any.
func (d *Deployment) setPaused(ctx context.Context, id string, state DeploymentState) (DeploymentState, error) {
	if state.Paused == nil || *state.Paused == (state.Status == "paused") {
		return state, nil
	}
	action := "unpause"
	if *state.Paused {
		action = "pause"
	}
	var raw []byte
	if err := d.Collection.Action(ctx, id, action, &raw); err != nil {
		return state, fmt.Errorf("could not %s deployment %s: %w", action, id, err)
	}
	state, _, err := rest.DecodeState[DeploymentArgs, DeploymentState](raw, state.DeploymentArgs)
	return state, err
}
