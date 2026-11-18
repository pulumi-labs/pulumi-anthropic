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
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/param"
	"github.com/anthropics/anthropic-sdk-go/v2/packages/respjson"
	"github.com/pulumi-labs/pulumi-anthropic/provider/config"
	"github.com/pulumi-labs/pulumi-anthropic/provider/internal/apiutil"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
	"github.com/pulumi/pulumi/sdk/v3/go/property"
)

// Agent is a versioned Managed Agents configuration.
type Agent struct{}

// NewAgent returns the complete Agent resource.
func NewAgent() infer.InferredResource {
	return agentResource{infer.Resource(&Agent{})}
}

func (a *Agent) Annotate(an infer.Annotator) {
	an.Describe(a, "A Claude Managed Agents agent: the model, system prompt, tools, MCP servers, "+
		"and skills that sessions run with.\n\n"+
		"Agents are versioned. Every update that changes the configuration creates a new version, "+
		"reported by the `version` output; sessions and deployments can pin a version, which makes "+
		"canary and staged rollouts a matter of which version each deployment references.\n\n"+
		"The API cannot delete agents, so deleting this resource archives the agent, which makes it "+
		"read-only. Archiving retains historical agent records and versions; this resource does not manage running sessions.")
}

type AgentArgs struct {
	Name              string             `pulumi:"name"`
	Model             Model              `pulumi:"model"`
	System            *string            `pulumi:"system,optional"`
	Description       *string            `pulumi:"description,optional"`
	Tools             *[]Tool            `pulumi:"tools,optional"`
	MCPServers        *[]MCPServer       `pulumi:"mcpServers,optional"`
	Skills            *[]SkillRef        `pulumi:"skills,optional"`
	Multiagent        *Multiagent        `pulumi:"multiagent,optional"`
	Metadata          map[string]string  `pulumi:"metadata,optional"`
	ExecutionIdentity *ExecutionIdentity `pulumi:"executionIdentity,optional"`
}

func (a *AgentArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The agent's name, 1 to 256 characters.")
	an.Describe(&a.Model, "The model the agent runs on.")
	an.Describe(&a.System, "The system prompt, up to 100,000 characters. Removing a configured prompt clears it.")
	an.Describe(&a.Description, "A description of the agent, up to 2,048 characters. Removing a configured description clears it.")
	an.Describe(&a.Tools, "The tools the agent can use, up to 256 tools across all toolsets. Removing the list clears it.")
	an.Describe(&a.MCPServers, "The MCP servers the agent can connect to, up to 20. "+
		"Each must be referenced by an `mcp_toolset` tool.")
	an.Describe(&a.Skills, "The skills available to the agent.")
	an.Describe(&a.Multiagent, "Multiagent orchestration: a legacy coordinator roster or independent advisor, subagent and workflow settings. Removing this configuration restores a single-threaded agent.")
	an.Describe(&a.ExecutionIdentity, "Principal carried by agent runs. Removing a configured principal restores the service-account default; omitting it initially uses that default.")
	an.Describe(&a.Metadata, "Arbitrary key-value pairs, up to 16. Keys are 1–64 characters and values at most 512 characters. Removing an entry from your configuration deletes that entry.")
}

type AgentState struct {
	AgentArgs
	Version   int    `pulumi:"version"`
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *AgentState) Annotate(an infer.Annotator) {
	an.Describe(&s.Version, "The agent's current version. It starts at 1 and increments with "+
		"every update that changes the configuration.")
	an.Describe(&s.CreatedAt, "When the agent was created, in RFC 3339 format.")
	an.Describe(&s.UpdatedAt, "When the agent was last updated, in RFC 3339 format.")
}

var (
	_ infer.CustomCheck[AgentArgs]              = (*Agent)(nil)
	_ infer.CustomUpdate[AgentArgs, AgentState] = (*Agent)(nil)
	_ infer.CustomRead[AgentArgs, AgentState]   = (*Agent)(nil)
	_ infer.CustomDelete[AgentState]            = (*Agent)(nil)
)

// ExecutionIdentity selects the principal carried by agent runs.
type ExecutionIdentity struct {
	Type    string  `pulumi:"type"`
	RoleARN *string `pulumi:"roleArn,optional"`
}

func (v *ExecutionIdentity) Annotate(a infer.Annotator) {
	a.Describe(v, "Execution principal for agent runs. Configuring a principal does not grant its permissions.")
	a.Describe(&v.Type, "aws_role or service_account. service_account restores the default principal.")
	a.Describe(&v.RoleARN, "AWS IAM role ARN. Required only for aws_role.")
}

type AgentAdvisor struct {
	Type  string  `pulumi:"type"`
	Model *string `pulumi:"model,optional"`
}

func (v *AgentAdvisor) Annotate(a infer.Annotator) {
	a.Describe(v, "Controls whether the primary thread can consult an advisor model.")
	a.Describe(&v.Type, "enabled or disabled.")
	a.Describe(&v.Model, "Advisor model ID, required when enabled. The model must be permitted as an advisor for the agent's model.")
}

type AgentDelegation struct {
	Type             string         `pulumi:"type"`
	InlineAgents     *string        `pulumi:"inlineAgents,optional"`
	PredefinedAgents *[]RosterEntry `pulumi:"predefinedAgents,optional"`
}

func (v *AgentDelegation) Annotate(a infer.Annotator) {
	a.Describe(v, "Controls subagent or workflow delegation independently.")
	a.Describe(&v.Type, "enabled or disabled. Disabled delegation cannot configure inline or predefined agents.")
	a.Describe(&v.InlineAgents, "enabled or disabled. Defaults to enabled; removing a configured value restores that default.")
	a.Describe(&v.PredefinedAgents, "At most 20 agent or self references. Removing the list clears it. Advisor references are not supported here. Subagents and workflows have separate lists.")
}

type FetchURLSources struct {
	ClientToolResults *FetchToolFilter `pulumi:"clientToolResults,optional"`
	ServerToolResults *FetchToolFilter `pulumi:"serverToolResults,optional"`
	UserInput         *string          `pulumi:"userInput,optional"`
}

func (v *FetchURLSources) Annotate(a infer.Annotator) {
	a.Describe(v, "Conversation sources allowed to contribute fetchable URLs.")
	a.Describe(&v.ClientToolResults, "Filter for custom tool results. Tool names must refer to custom tools in this agent.")
	a.Describe(&v.ServerToolResults, "Filter for built-in tool results. Listed names must be web_search or web_fetch.")
	a.Describe(&v.UserInput, "all or none, controlling URLs in user messages. Defaults to all.")
}

type FetchToolFilter struct {
	Type  string    `pulumi:"type"`
	Tools *[]string `pulumi:"tools,optional"`
}

func (v *FetchToolFilter) Annotate(a infer.Annotator) {
	a.Describe(v, "Selects tool results whose URLs may be fetched.")
	a.Describe(&v.Type, "all, none, only or except. only and except require a tools list.")
	a.Describe(&v.Tools, "Exact case-sensitive tool names for only or except.")
}

type SearchLocation struct {
	City     *string `pulumi:"city,optional"`
	Country  *string `pulumi:"country,optional"`
	Region   *string `pulumi:"region,optional"`
	Timezone *string `pulumi:"timezone,optional"`
}

func (v *SearchLocation) Annotate(a infer.Annotator) {
	a.Describe(v, "Approximate user location for search localization.")
	a.Describe(&v.City, "City name.")
	a.Describe(&v.Country, "Uppercase two-letter ISO 3166-1 country code.")
	a.Describe(&v.Region, "Region or state name.")
	a.Describe(&v.Timezone, "IANA timezone identifier, such as America/Los_Angeles.")
}

// Model selects and configures the model an agent runs on.
type Model struct {
	ID           string  `pulumi:"id"`
	Effort       *Effort `pulumi:"effort,optional"`
	Speed        *Speed  `pulumi:"speed,optional"`
	InferenceGeo *string `pulumi:"inferenceGeo,optional"`
}

func (m *Model) Annotate(a infer.Annotator) {
	a.Describe(m, "Model selection and inference settings for an agent.")
	a.Describe(&m.ID, "The model ID, such as `claude-opus-5-5`.")
	a.Describe(&m.Effort, "How much effort the model spends reasoning. "+
		"Defaults to the model's own default. Removing a configured effort may retain the stored effort; use replaceOnChanges on model.effort when a fresh agent is required.")
	a.Describe(&m.Speed, "The inference speed tier. Removing a configured tier restores standard.")
	a.Describe(&m.InferenceGeo, "Where inference runs, such as us or global. Removing a configured region falls back to the workspace default.")
}

// Effort is a model's reasoning effort level.
type Effort string

func (Effort) Values() []infer.EnumValue[Effort] {
	return []infer.EnumValue[Effort]{
		{Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"}, {Value: "max"},
	}
}

// Speed is a model's inference speed tier.
type Speed string

func (Speed) Values() []infer.EnumValue[Speed] {
	return []infer.EnumValue[Speed]{{Value: "standard"}, {Value: "fast"}}
}

// ToolType discriminates the kinds of tool an agent can be given.
type ToolType string

const (
	AgentToolset ToolType = "agent_toolset_20260401"
	MCPToolset   ToolType = "mcp_toolset"
	CustomTool   ToolType = "custom"
)

func (ToolType) Values() []infer.EnumValue[ToolType] {
	return []infer.EnumValue[ToolType]{
		{Value: AgentToolset, Description: "Anthropic's built-in agent tools: bash, edit, read, " +
			"write, glob, grep, web_fetch, and web_search."},
		{Value: MCPToolset, Description: "The tools of an MCP server declared in `mcpServers`."},
		{Value: CustomTool, Description: "A tool the client executes, declared by name and input schema."},
	}
}

// Tool grants an agent a set of tools. Which fields apply depends on the type.
type Tool struct {
	Type          ToolType       `pulumi:"type"`
	Configs       *[]ToolConfig  `pulumi:"configs,optional"`
	DefaultConfig *ToolDefaults  `pulumi:"defaultConfig,optional"`
	MCPServerName *string        `pulumi:"mcpServerName,optional"`
	Name          *string        `pulumi:"name,optional"`
	Description   *string        `pulumi:"description,optional"`
	InputSchema   map[string]any `pulumi:"inputSchema,optional"`
}

func (t *Tool) Annotate(a infer.Annotator) {
	a.Describe(t, "An agent toolset, MCP toolset or client-executed custom tool. Only fields applicable to its type may be configured.")
	a.Describe(&t.Type, "The kind of tool.")
	a.Describe(&t.Configs, "Per-tool settings, for `agent_toolset_20260401` and `mcp_toolset`.")
	a.Describe(&t.DefaultConfig, "Settings for tools without an entry in `configs`, "+
		"for `agent_toolset_20260401` and `mcp_toolset`.")
	a.Describe(&t.MCPServerName, "The `mcpServers` entry whose tools to grant. Required for `mcp_toolset`.")
	a.Describe(&t.Name, "The tool's name. Required for `custom`.")
	a.Describe(&t.Description, "What the tool does, for the model. Required for `custom`.")
	a.Describe(&t.InputSchema, "The JSON Schema of the tool's input. Required for `custom`.")
}

// ToolConfig configures one tool of a toolset.
type ToolConfig struct {
	Name             string            `pulumi:"name"`
	Enabled          *bool             `pulumi:"enabled,optional"`
	PermissionPolicy *PermissionPolicy `pulumi:"permissionPolicy,optional"`
	MaxContentTokens *int              `pulumi:"maxContentTokens,optional"`
	AllowedDomains   *[]string         `pulumi:"allowedDomains,optional"`
	BlockedDomains   *[]string         `pulumi:"blockedDomains,optional"`
	UserLocation     *SearchLocation   `pulumi:"userLocation,optional"`
	URLSources       *FetchURLSources  `pulumi:"urlSources,optional"`
}

func (c *ToolConfig) Annotate(a infer.Annotator) {
	a.Describe(c, "Settings for one tool within an agent or MCP toolset.")
	a.Describe(&c.Name, "The tool's name, such as `bash` or `web_fetch`.")
	a.Describe(&c.MaxContentTokens, "Maximum fetched content tokens, for web_fetch.")
	a.Describe(&c.AllowedDomains, "Domains the web_fetch or web_search tool may access. Cannot be combined with blockedDomains.")
	a.Describe(&c.BlockedDomains, "Domains the web_fetch or web_search tool must not access. Cannot be combined with allowedDomains.")
	a.Describe(&c.UserLocation, "Approximate location used to localize web_search results.")
	a.Describe(&c.URLSources, "Conversation sources whose URLs web_fetch may fetch. Each omitted source allows all URLs from that source.")
	a.Describe(&c.Enabled, "Whether the agent may use the tool.")
	a.Describe(&c.PermissionPolicy, "Whether a tool call needs approval.")
}

// ToolDefaults configures the tools of a toolset that have no entry of their own.
type ToolDefaults struct {
	Enabled          *bool             `pulumi:"enabled,optional"`
	PermissionPolicy *PermissionPolicy `pulumi:"permissionPolicy,optional"`
}

func (c *ToolDefaults) Annotate(a infer.Annotator) {
	a.Describe(c, "Default settings for tools without an individual override.")
	a.Describe(&c.Enabled, "Whether the agent may use the tools.")
	a.Describe(&c.PermissionPolicy, "Whether a tool call needs approval.")
}

// PermissionPolicy decides whether a tool call needs approval.
type PermissionPolicy string

func (PermissionPolicy) Values() []infer.EnumValue[PermissionPolicy] {
	return []infer.EnumValue[PermissionPolicy]{
		{Value: "always_allow", Description: "Calls run without approval."},
		{Value: "always_ask", Description: "Every call waits for approval."},
		{Value: "auto", Description: "The platform decides per call."},
	}
}

// MCPServer declares a remote MCP server an agent can connect to.
type MCPServer struct {
	Name string `pulumi:"name"`
	URL  string `pulumi:"url"`
}

func (s *MCPServer) Annotate(a infer.Annotator) {
	a.Describe(s, "A named remote MCP endpoint available to the agent.")
	a.Describe(&s.Name, "The server's name, which an `mcp_toolset` tool refers to.")
	a.Describe(&s.URL, "The server's URL. Credentials for it are supplied by a vault at session time.")
}

// SkillType discriminates Anthropic-provided skills from an organization's own.
type SkillType string

func (SkillType) Values() []infer.EnumValue[SkillType] {
	return []infer.EnumValue[SkillType]{
		{Value: "anthropic", Description: "A skill provided by Anthropic, such as `xlsx`."},
		{Value: "custom", Description: "A skill uploaded to the organization, such as an `anthropic.skills.Skill`."},
	}
}

// SkillRef attaches a skill to an agent.
type SkillRef struct {
	Type    SkillType `pulumi:"type"`
	SkillID string    `pulumi:"skillId"`
	Version *string   `pulumi:"version,optional"`
}

func (s *SkillRef) Annotate(a infer.Annotator) {
	a.Describe(s, "An Anthropic-provided or organization-uploaded skill available to an agent.")
	a.Describe(&s.Type, "Whether the skill is provided by Anthropic or uploaded to the organization.")
	a.Describe(&s.SkillID, "The skill's ID.")
	a.Describe(&s.Version, "The skill version to pin. Omit to always use the latest version.")
}

// Multiagent makes an agent a coordinator that delegates to a roster of agents.
type Multiagent struct {
	Agents    *[]RosterEntry   `pulumi:"agents,optional"`
	Type      *string          `pulumi:"type,optional"`
	Advisor   *AgentAdvisor    `pulumi:"advisor,optional"`
	Subagents *AgentDelegation `pulumi:"subagents,optional"`
	Workflows *AgentDelegation `pulumi:"workflows,optional"`
}

func (m *Multiagent) Annotate(a infer.Annotator) {
	a.Describe(m, "Legacy coordinator or independent advisor, subagent and workflow orchestration configuration.")
	a.Describe(&m.Agents, "The agents the coordinator can delegate to, between 1 and 20. Only for coordinator mode.")
	a.Describe(&m.Type, "Orchestration mode: coordinator (the default) or multiagent_20261001. The latter configures advisor, subagents and workflows independently.")
	a.Describe(&m.Advisor, "Advisor model configuration for multiagent_20261001. Defaults to disabled. Removing a previously configured advisor restores that default.")
	a.Describe(&m.Subagents, "Subagent delegation for multiagent_20261001. Defaults to enabled with inline agents allowed and no predefined agents. Removing this configuration restores those defaults.")
	a.Describe(&m.Workflows, "Workflow delegation for multiagent_20261001. Defaults to enabled with inline agents allowed and no predefined agents. Removing this configuration restores those defaults.")
}

// RosterEntryType discriminates the kinds of coordinator roster entry.
type RosterEntryType string

func (RosterEntryType) Values() []infer.EnumValue[RosterEntryType] {
	return []infer.EnumValue[RosterEntryType]{
		{Value: "agent", Description: "Another agent, by ID and optionally version."},
		{Value: "self", Description: "The coordinator itself."},
		{Value: "advisor", Description: "A model consulted for advice, by model ID."},
	}
}

// RosterEntry is one member of a coordinator's roster.
type RosterEntry struct {
	Type    RosterEntryType `pulumi:"type"`
	ID      *string         `pulumi:"id,optional"`
	Version *int            `pulumi:"version,optional"`
	Model   *string         `pulumi:"model,optional"`
}

func (r *RosterEntry) Annotate(a infer.Annotator) {
	a.Describe(r, "An agent, self reference or legacy coordinator advisor. Modern predefined-agent lists support only agent and self references.")
	a.Describe(&r.Type, "The kind of roster entry.")
	a.Describe(&r.ID, "The agent's ID. Required for `agent`.")
	a.Describe(&r.Version, "The agent version to delegate to. If omitted, the version current "+
		"when the coordinator is saved is pinned; later versions are not picked up.")
	a.Describe(&r.Model, "The advisor's model ID. Required for `advisor`.")
}

// Check validates the references between tools and MCP servers, which the API would
// otherwise reject only at deployment time.
func (a *Agent) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[AgentArgs], error) {
	inputs := agentNormalizeCheck(req.NewInputs)
	args, failures, err := infer.DefaultCheck[AgentArgs](ctx, inputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[AgentArgs]{Inputs: args, Failures: failures}, err
	}

	failures = append(failures, agentMCPFailures(inputs)...)
	failures = append(failures, agentValidation(inputs)...)
	return infer.CheckResponse[AgentArgs]{Inputs: args, Failures: failures}, nil
}

func (controller *Agent) Create(ctx context.Context, req infer.CreateRequest[AgentArgs]) (infer.CreateResponse[AgentState], error) {
	if req.DryRun {
		return infer.CreateResponse[AgentState]{Output: AgentState{AgentArgs: req.Inputs}}, nil
	}
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.CreateResponse[AgentState]{}, err
	}
	params, err := agentNew(req.Inputs)
	if err != nil {
		return infer.CreateResponse[AgentState]{}, err
	}
	r, err := client.Beta.Agents.New(ctx, params)
	if err != nil {
		return infer.CreateResponse[AgentState]{}, err
	}
	s, err := agentObserved(r, req.Inputs, agentObservationOptions{})
	if err != nil {
		// A failed conversion must not strand an allocation whose new identity
		// is trustworthy. Never guess an ID or archive on ambiguous SDK errors.
		if r != nil && apiutil.StringField(r.JSON.ID) && r.ID != "" &&
			(r.JSON.Type.Raw() == "" || (apiutil.StringField(r.JSON.Type) && r.Type == "agent")) {
			_, cleanupErr := client.Beta.Agents.Archive(context.WithoutCancel(ctx), r.ID, sdk.BetaAgentArchiveParams{})
			if cleanupErr != nil && !apiutil.IsNotFound(cleanupErr) {
				err = errors.Join(err, fmt.Errorf("could not archive newly created agent %s: %w", r.ID, cleanupErr))
			}
		}
		return infer.CreateResponse[AgentState]{}, err
	}
	return infer.CreateResponse[AgentState]{ID: r.ID, Output: s}, nil
}

func (controller *Agent) Read(ctx context.Context, req infer.ReadRequest[AgentArgs, AgentState]) (infer.ReadResponse[AgentArgs, AgentState], error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.ReadResponse[AgentArgs, AgentState]{}, err
	}
	r, err := client.Beta.Agents.Get(ctx, req.ID, sdk.BetaAgentGetParams{})
	if apiutil.IsNotFound(err) {
		return infer.ReadResponse[AgentArgs, AgentState]{}, nil
	}
	if err != nil {
		return infer.ReadResponse[AgentArgs, AgentState]{}, err
	}
	if r == nil || r.ID != req.ID {
		return infer.ReadResponse[AgentArgs, AgentState]{}, fmt.Errorf("agent read returned a missing or different identity")
	}
	// Existing managed inputs distinguish refresh from ID-only import;
	// creation time is not an ownership sentinel.
	importing := req.Inputs.Name == "" && req.State.Name == "" && req.Inputs.Model.ID == "" && req.State.Model.ID == ""
	if req.Inputs.Name == "" && req.Inputs.Model.ID == "" && !importing {
		req.Inputs = req.State.AgentArgs
	}
	s, err := agentObserved(r, req.Inputs, agentObservationOptions{importing: importing})
	if err != nil {
		return infer.ReadResponse[AgentArgs, AgentState]{}, err
	}
	if !r.ArchivedAt.IsZero() {
		return infer.ReadResponse[AgentArgs, AgentState]{}, nil
	}
	return infer.ReadResponse[AgentArgs, AgentState]{ID: req.ID, Inputs: s.AgentArgs, State: s}, nil
}

func (controller *Agent) Update(ctx context.Context, req infer.UpdateRequest[AgentArgs, AgentState]) (infer.UpdateResponse[AgentState], error) {
	if req.DryRun {
		return infer.UpdateResponse[AgentState]{Output: AgentState{AgentArgs: req.Inputs}}, nil
	}
	warnKeptEffort(ctx, req.State.Model, req.Inputs.Model)
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.UpdateResponse[AgentState]{}, err
	}
	params, err := agentUpdate(req.State, req.Inputs)
	if err != nil {
		return infer.UpdateResponse[AgentState]{}, err
	}
	// Never GET here. The engine owns refresh. Send all desired managed state.
	r, err := client.Beta.Agents.Update(ctx, req.ID, params)
	if err != nil {
		if apiutil.IsStatus(err, http.StatusConflict) {
			return infer.UpdateResponse[AgentState]{}, fmt.Errorf("agent version conflict; run `pulumi refresh` to reconcile, then update again: %w", err)
		}
		return infer.UpdateResponse[AgentState]{}, err
	}
	accepted := req.Inputs
	// This optional is deliberately not reset by the update's omission.
	if accepted.Model.Effort == nil && req.State.Model.Effort != nil {
		accepted.Model.Effort = req.State.Model.Effort
	}
	if r == nil || !apiutil.StringField(r.JSON.ID) || r.ID != req.ID {
		return infer.UpdateResponse[AgentState]{}, fmt.Errorf("agent update returned a missing or different identity")
	}
	s, err := agentObserved(r, accepted, agentObservationOptions{})
	if err != nil {
		return infer.UpdateResponse[AgentState]{}, err
	}
	return infer.UpdateResponse[AgentState]{Output: s}, nil
}

func (controller *Agent) Delete(ctx context.Context, req infer.DeleteRequest[AgentState]) (infer.DeleteResponse, error) {
	client, err := config.ClientFor(ctx, config.Workspace)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = client.Beta.Agents.Archive(ctx, req.ID, sdk.BetaAgentArchiveParams{})
	if apiutil.IsNotFound(err) {
		err = nil
	}
	return infer.DeleteResponse{}, err
}

// Preserve normalized input knownness, secrecy and dependencies through Check.
// Custom-resource outputs do not inherit their inputs' dependency annotations.
type agentResource struct{ infer.InferredResource }

func (r agentResource) Check(ctx context.Context, req p.CheckRequest) (p.CheckResponse, error) {
	req.Inputs = agentNormalizeCheck(req.Inputs)
	resp, err := infer.Resource(&Agent{}).Check(ctx, req)
	if err == nil && len(resp.Failures) == 0 {
		resp.Inputs = req.Inputs
	}
	return resp, err
}

func (r agentResource) Diff(ctx context.Context, req p.DiffRequest) (p.DiffResponse, error) {
	return infer.Resource(&Agent{}).Diff(ctx, req)
}

func (r agentResource) Create(ctx context.Context, req p.CreateRequest) (p.CreateResponse, error) {
	resp, err := infer.Resource(&Agent{}).Create(ctx, req)
	if err == nil && req.DryRun {
		resp.Properties = agentPreviewOutputs(resp.Properties, req.Properties)
	}
	return resp, err
}

func (r agentResource) Read(ctx context.Context, req p.ReadRequest) (p.ReadResponse, error) {
	resp, err := infer.Resource(&Agent{}).Read(ctx, req)
	if err == nil && resp.ID != "" {
		// Read does not apply infer's secret wiring. Protect each returned
		// property that corresponds to a secret input, including nested secrets.
		// Dependency annotations deliberately do not flow to resource outputs.
		for key, input := range req.Inputs.All {
			if !input.HasSecrets() {
				continue
			}
			if value, ok := resp.Inputs.GetOk(key); ok {
				resp.Inputs = resp.Inputs.Set(key, value.WithSecret(true))
			}
			if value, ok := resp.Properties.GetOk(key); ok {
				resp.Properties = resp.Properties.Set(key, value.WithSecret(true))
			}
		}
	}
	return resp, err
}

func (r agentResource) Update(ctx context.Context, req p.UpdateRequest) (p.UpdateResponse, error) {
	resp, err := infer.Resource(&Agent{}).Update(ctx, req)
	if err == nil && req.DryRun {
		resp.Properties = agentPreviewOutputs(resp.Properties, req.Inputs)
	}
	return resp, err
}

func (r agentResource) Delete(ctx context.Context, req p.DeleteRequest) error {
	return infer.Resource(&Agent{}).Delete(ctx, req)
}

// An omitted or null mode selects coordinator, just like an explicit value.
// Add that value rather than deleting an explicit mode, so its own secrecy and
// dependencies stay at the same path. Unknown modes/objects stay unresolved.
func agentNormalizeCheck(inputs property.Map) property.Map {
	multi := inputs.Get("multiagent")
	if !multi.IsMap() {
		return inputs
	}
	m := multi.AsMap()
	typ := m.Get("type")
	if !typ.IsNull() {
		return inputs
	}
	return inputs.Set("multiagent", property.WithGoValue(multi, m.Set("type", property.WithGoValue(typ, "coordinator"))))
}

// agentMCPFailures uses raw knownness only for this cross-property constraint.
// Typed Check decodes unresolved names as empty strings, which cannot establish
// either a missing server or an unused server. Unrelated unknown values (for
// example a server URL) do not prevent checking known names.
func agentMCPFailures(inputs property.Map) []p.CheckFailure {
	servers := map[string]bool{}
	serverInputs := inputs.Get("mcpServers")
	unknownServer := serverInputs.IsComputed()
	if serverInputs.IsArray() {
		for _, server := range serverInputs.AsArray().All {
			if server.IsComputed() {
				unknownServer = true
				continue
			}
			if server.IsMap() {
				name := server.AsMap().Get("name")
				if name.IsComputed() {
					unknownServer = true
				} else if name.IsString() {
					servers[name.AsString()] = false
				}
			}
		}
	}
	var failures []p.CheckFailure
	toolInputs := inputs.Get("tools")
	unknownReference := toolInputs.IsComputed()
	if toolInputs.IsArray() {
		for i, tool := range toolInputs.AsArray().All {
			if tool.IsComputed() {
				unknownReference = true
				continue
			}
			if !tool.IsMap() {
				continue // DefaultCheck owns structural failures.
			}
			t := tool.AsMap()
			typ := t.Get("type")
			if typ.IsComputed() {
				unknownReference = true
				continue
			}
			if !typ.IsString() || typ.AsString() != string(MCPToolset) {
				continue
			}
			name := t.Get("mcpServerName")
			if name.IsComputed() {
				unknownReference = true
				continue
			}
			if !name.IsString() || name.AsString() == "" {
				continue
			}
			if _, ok := servers[name.AsString()]; !ok && !unknownServer {
				failures = append(failures, p.CheckFailure{
					Property: fmt.Sprintf("tools[%d].mcpServerName", i),
					Reason:   fmt.Sprintf("no MCP server named %q is declared in mcpServers", name.AsString()),
				})
			}
			servers[name.AsString()] = true
		}
	}
	if !unknownReference && serverInputs.IsArray() {
		for i, server := range serverInputs.AsArray().All {
			if !server.IsMap() {
				continue
			}
			name := server.AsMap().Get("name")
			if name.IsString() && !servers[name.AsString()] {
				failures = append(failures, p.CheckFailure{
					Property: fmt.Sprintf("mcpServers[%d]", i),
					Reason:   fmt.Sprintf("MCP server %q must be referenced by an mcp_toolset tool", name.AsString()),
				})
			}
		}
	}
	return failures
}

// Server-generated facts are unknown until the preview is applied.
func agentPreviewOutputs(m, inputs property.Map) property.Map {
	// An unresolved optional slice can decode to a nil pointer and disappear
	// before infer applies preview wiring. Preserve only that unknown fact,
	// never the input's dependency annotations or a speculative known value.
	for key, value := range inputs.All {
		if value.IsComputed() && m.Get(key).IsNull() {
			m = m.Set(key, property.New(property.Computed).WithSecret(value.Secret()))
		}
	}
	for _, k := range []string{"version", "createdAt", "updatedAt"} {
		m = m.Set(k, property.New(property.Computed).WithSecret(m.Get(k).Secret()))
	}
	return m
}

// All validators inspect known leaves; a computed sibling never establishes a
// missing variant field or excuses an independently invalid known value.
func agentValidation(inputs property.Map) []p.CheckFailure {
	var failures []p.CheckFailure
	fail := func(path, reason string) { failures = append(failures, p.CheckFailure{Property: path, Reason: reason}) }
	text := func(m property.Map, k, path string, min, max int) {
		v := m.Get(k)
		if v.IsString() {
			n := utf8.RuneCountInString(v.AsString())
			if n < min || n > max {
				fail(path, fmt.Sprintf("must contain %d–%d characters", min, max))
			}
		}
	}
	enum := func(m property.Map, k, path string, values ...string) {
		v := m.Get(k)
		if !v.IsString() {
			return
		}
		for _, x := range values {
			if v.AsString() == x {
				return
			}
		}
		fail(path, "unsupported value")
	}
	required := func(m property.Map, k, path string) {
		if m.Get(k).IsNull() {
			fail(path, "required for this variant")
		}
	}
	absent := func(m property.Map, k, path string) {
		if v := m.Get(k); !v.IsNull() && !v.IsComputed() {
			fail(path, "not supported for this variant")
		}
	}
	listLimit := func(v property.Value, path string, min, max int) {
		if v.IsArray() && (v.AsArray().Len() < min || v.AsArray().Len() > max) {
			fail(path, fmt.Sprintf("must contain %d–%d entries", min, max))
		}
	}
	text(inputs, "name", "name", 1, 256)
	text(inputs, "description", "description", 0, 2048)
	text(inputs, "system", "system", 0, 100000)
	if v := inputs.Get("model"); v.IsMap() {
		m := v.AsMap()
		text(m, "id", "model.id", 1, 256)
		enum(m, "effort", "model.effort", "low", "medium", "high", "xhigh", "max")
		enum(m, "speed", "model.speed", "standard", "fast")
	}
	if v := inputs.Get("metadata"); v.IsMap() {
		if v.AsMap().Len() > 16 {
			fail("metadata", "must contain at most 16 entries")
		}
		v.AsMap().All(func(k string, v property.Value) bool {
			n := utf8.RuneCountInString(k)
			if n < 1 || n > 64 {
				fail("metadata", "keys must contain 1–64 characters")
			}
			if v.IsString() && utf8.RuneCountInString(v.AsString()) > 512 {
				fail("metadata", "values must contain at most 512 characters")
			}
			return true
		})
	}
	servers := inputs.Get("mcpServers")
	listLimit(servers, "mcpServers", 0, 20)
	names := map[string]bool{}
	if servers.IsArray() {
		for i, s := range servers.AsArray().All {
			if s.IsMap() {
				m := s.AsMap()
				path := fmt.Sprintf("mcpServers[%d].name", i)
				text(m, "name", path, 1, 255)
				if n := m.Get("name"); n.IsString() {
					if names[n.AsString()] {
						fail(path, "server names must be unique")
					}
					names[n.AsString()] = true
				}
			}
		}
	}
	if tools := inputs.Get("tools"); tools.IsArray() {
		// Custom entries each contribute one tool regardless of unknown names or
		// schemas. Toolset expansion may require server discovery, so count only
		// this proven lower bound rather than treating each toolset as one tool.
		knownTools := 0
		for _, v := range tools.AsArray().All {
			if v.IsMap() {
				typ := v.AsMap().Get("type")
				if typ.IsString() && typ.AsString() == string(CustomTool) {
					knownTools++
				}
			}
		}
		if knownTools > 256 {
			fail("tools", "must contain at most 256 tools across all toolsets")
		}
		for i, v := range tools.AsArray().All {
			if !v.IsMap() {
				continue
			}
			m := v.AsMap()
			path := fmt.Sprintf("tools[%d]", i)
			enum(m, "type", path+".type", string(AgentToolset), string(MCPToolset), string(CustomTool))
			typ := m.Get("type")
			if !typ.IsString() {
				continue
			}
			switch ToolType(typ.AsString()) {
			case CustomTool:
				for _, k := range []string{"name", "description", "inputSchema"} {
					required(m, k, path+"."+k)
				}
				text(m, "name", path+".name", 1, 128)
				for _, k := range []string{"configs", "defaultConfig", "mcpServerName"} {
					absent(m, k, path+"."+k)
				}
			case AgentToolset, MCPToolset:
				if ToolType(typ.AsString()) == MCPToolset {
					required(m, "mcpServerName", path+".mcpServerName")
					text(m, "mcpServerName", path+".mcpServerName", 1, 255)
				} else {
					absent(m, "mcpServerName", path+".mcpServerName")
				}
				for _, k := range []string{"name", "description", "inputSchema"} {
					absent(m, k, path+"."+k)
				}
				if d := m.Get("defaultConfig"); d.IsMap() {
					enum(d.AsMap(), "permissionPolicy", path+".defaultConfig.permissionPolicy", "always_allow", "always_ask", "auto")
				}
				if configs := m.Get("configs"); configs.IsArray() {
					for j, c := range configs.AsArray().All {
						if c.IsMap() {
							cm := c.AsMap()
							cp := fmt.Sprintf("%s.configs[%d]", path, j)
							enum(cm, "permissionPolicy", cp+".permissionPolicy", "always_allow", "always_ask", "auto")
							text(cm, "name", cp+".name", 1, 128)
							if ToolType(typ.AsString()) == AgentToolset {
								enum(cm, "name", cp+".name", "bash", "edit", "read", "write", "glob", "grep", "web_fetch", "web_search")
							}
							if !cm.Get("allowedDomains").IsNull() && !cm.Get("allowedDomains").IsComputed() && !cm.Get("blockedDomains").IsNull() && !cm.Get("blockedDomains").IsComputed() {
								fail(cp, "allowedDomains and blockedDomains cannot be combined")
							}
							if n := cm.Get("maxContentTokens"); n.IsNumber() && (n.AsNumber() < 1 || n.AsNumber() != float64(int64(n.AsNumber()))) {
								fail(cp+".maxContentTokens", "must be a positive integer")
							}
							if u := cm.Get("urlSources"); u.IsMap() {
								um := u.AsMap()
								enum(um, "userInput", cp+".urlSources.userInput", "all", "none")
								for _, key := range []string{"clientToolResults", "serverToolResults"} {
									if f := um.Get(key); f.IsMap() {
										fm := f.AsMap()
										fp := cp + ".urlSources." + key
										enum(fm, "type", fp+".type", "all", "none", "only", "except")
										if typ := fm.Get("type"); typ.IsString() {
											if typ.AsString() == "only" || typ.AsString() == "except" {
												required(fm, "tools", fp+".tools")
											} else {
												absent(fm, "tools", fp+".tools")
											}
										}
										if key == "serverToolResults" {
											if ts := fm.Get("tools"); ts.IsArray() {
												for i, x := range ts.AsArray().All {
													if x.IsString() && x.AsString() != "web_fetch" && x.AsString() != "web_search" {
														fail(fmt.Sprintf("%s.tools[%d]", fp, i), "must name web_fetch or web_search")
													}
												}
											}
										}
									}
								}
							}
							if name := cm.Get("name"); name.IsString() {
								if ToolType(typ.AsString()) != AgentToolset || (name.AsString() != "web_fetch" && name.AsString() != "web_search") {
									for _, k := range []string{"allowedDomains", "blockedDomains"} {
										absent(cm, k, cp+"."+k)
									}
								}
								if ToolType(typ.AsString()) != AgentToolset || name.AsString() != "web_fetch" {
									absent(cm, "maxContentTokens", cp+".maxContentTokens")
									absent(cm, "urlSources", cp+".urlSources")
								}
								if ToolType(typ.AsString()) != AgentToolset || name.AsString() != "web_search" {
									absent(cm, "userLocation", cp+".userLocation")
								}
							}
						}
					}
				}
			}
		}
	}
	if skills := inputs.Get("skills"); skills.IsArray() {
		for i, v := range skills.AsArray().All {
			if v.IsMap() {
				m := v.AsMap()
				path := fmt.Sprintf("skills[%d]", i)
				enum(m, "type", path+".type", "custom", "anthropic")
				text(m, "skillId", path+".skillId", 1, 256)
			}
		}
	}
	if e := inputs.Get("executionIdentity"); e.IsMap() {
		m := e.AsMap()
		enum(m, "type", "executionIdentity.type", "aws_role", "service_account")
		if typ := m.Get("type"); typ.IsString() {
			if typ.AsString() == "aws_role" {
				required(m, "roleArn", "executionIdentity.roleArn")
				text(m, "roleArn", "executionIdentity.roleArn", 1, 2048)
			} else {
				absent(m, "roleArn", "executionIdentity.roleArn")
			}
		}
	}
	roster := func(v property.Value, path string, advisor bool) {
		if !v.IsArray() {
			return
		}
		for i, r := range v.AsArray().All {
			if !r.IsMap() {
				continue
			}
			m := r.AsMap()
			pth := fmt.Sprintf("%s[%d]", path, i)
			values := []string{"agent", "self"}
			if advisor {
				values = append(values, "advisor")
			}
			enum(m, "type", pth+".type", values...)
			if typ := m.Get("type"); typ.IsString() {
				switch typ.AsString() {
				case "agent":
					required(m, "id", pth+".id")
					text(m, "id", pth+".id", 1, 256)
					absent(m, "model", pth+".model")
				case "advisor":
					required(m, "model", pth+".model")
					absent(m, "id", pth+".id")
					absent(m, "version", pth+".version")
				case "self":
					for _, k := range []string{"id", "version", "model"} {
						absent(m, k, pth+"."+k)
					}
				}
			}
			if ver := m.Get("version"); ver.IsNumber() && (ver.AsNumber() < 1 || ver.AsNumber() != float64(int64(ver.AsNumber()))) {
				fail(pth+".version", "must be a positive integer")
			}
		}
	}
	if v := inputs.Get("multiagent"); v.IsMap() {
		m := v.AsMap()
		enum(m, "type", "multiagent.type", "coordinator", "multiagent_20261001")
		typ := m.Get("type")
		if typ.IsComputed() {
			return failures
		}
		coordinator := typ.IsNull() || (typ.IsString() && typ.AsString() == "coordinator")
		if coordinator {
			required(m, "agents", "multiagent.agents")
			listLimit(m.Get("agents"), "multiagent.agents", 1, 20)
			roster(m.Get("agents"), "multiagent.agents", true)
			for _, k := range []string{"advisor", "subagents", "workflows"} {
				absent(m, k, "multiagent."+k)
			}
		} else {
			absent(m, "agents", "multiagent.agents")
			if a := m.Get("advisor"); a.IsMap() {
				am := a.AsMap()
				enum(am, "type", "multiagent.advisor.type", "enabled", "disabled")
				if t := am.Get("type"); t.IsString() {
					if t.AsString() == "enabled" {
						required(am, "model", "multiagent.advisor.model")
						text(am, "model", "multiagent.advisor.model", 1, 256)
					} else {
						absent(am, "model", "multiagent.advisor.model")
					}
				}
			}
			for _, key := range []string{"subagents", "workflows"} {
				if d := m.Get(key); d.IsMap() {
					dm := d.AsMap()
					path := "multiagent." + key
					enum(dm, "type", path+".type", "enabled", "disabled")
					enum(dm, "inlineAgents", path+".inlineAgents", "enabled", "disabled")
					roster(dm.Get("predefinedAgents"), path+".predefinedAgents", false)
					listLimit(dm.Get("predefinedAgents"), path+".predefinedAgents", 0, 20)
					if t := dm.Get("type"); t.IsString() && t.AsString() == "disabled" {
						absent(dm, "inlineAgents", path+".inlineAgents")
						absent(dm, "predefinedAgents", path+".predefinedAgents")
					}
				}
			}
		}
	}
	return failures
}

type agentObservationOptions struct {
	importing bool
}

// Effort omission is documented as preservation, not a reset, and the platform does
// not clear it when sent as null. Do not claim clearing.
func warnKeptEffort(ctx context.Context, old, new Model) {
	if old.Effort == nil || new.Effort != nil {
		return
	}
	p.GetLogger(ctx).Warningf("removing model.effort may retain its stored value %q. "+
		"Use \"model.effort\" in replaceOnChanges if a fresh agent with the model default is required.", string(*old.Effort))
}

func agentPtr[T any](v T) *T { return &v }

func agentEffort(v string) bool {
	return v == "low" || v == "medium" || v == "high" || v == "xhigh" || v == "max"
}

func agentPermission(v string) bool { return v == "always_allow" || v == "always_ask" || v == "auto" }

// Map server facts while retaining the configured management boundary.
func agentObserved(r *sdk.BetaManagedAgentsAgent, prior AgentArgs, options agentObservationOptions) (AgentState, error) {
	s := AgentState{}
	if r == nil {
		return AgentState{}, fmt.Errorf("missing agent response")
	}
	var invalid bool
	required := func(valid bool) {
		if !valid {
			invalid = true
		}
	}
	required(apiutil.StringField(r.JSON.ID) && r.ID != "")
	required(apiutil.StringField(r.JSON.Name) && r.Name != "")
	if apiutil.StringField(r.JSON.Name) {
		s.Name = r.Name
	}
	required(r.JSON.Model.Valid() && apiutil.StringField(r.Model.JSON.ID) && r.Model.ID != "")
	if apiutil.StringField(r.Model.JSON.ID) {
		s.Model.ID = r.Model.ID
	}
	if raw := r.Model.JSON.Effort.Raw(); raw != "" {
		if raw == "null" || !r.Model.JSON.Effort.Valid() || !apiutil.StringField(r.Model.Effort.JSON.Type) || !agentEffort(r.Model.Effort.Type) {
			invalid = true
		} else if options.importing || prior.Model.Effort != nil {
			s.Model.Effort = agentPtr(Effort(r.Model.Effort.Type))
		}
	}
	if raw := r.Model.JSON.Speed.Raw(); raw != "" {
		if raw == "null" || !apiutil.StringField(r.Model.JSON.Speed) || (r.Model.Speed != "standard" && r.Model.Speed != "fast") {
			invalid = true
		} else if options.importing || prior.Model.Speed != nil {
			s.Model.Speed = agentPtr(Speed(r.Model.Speed))
		}
	}
	if raw := r.Model.JSON.InferenceGeo.Raw(); raw != "" {
		if raw == "null" || !apiutil.StringField(r.Model.JSON.InferenceGeo) {
			invalid = true
		} else if options.importing || prior.Model.InferenceGeo != nil {
			s.Model.InferenceGeo = agentPtr(r.Model.InferenceGeo)
		}
	}
	// The API declares description and system as string | null.
	required(apiutil.StringField(r.JSON.Description) || r.JSON.Description.Raw() == "null")
	if apiutil.StringField(r.JSON.Description) {
		s.Description = nil
		if options.importing || prior.Description != nil {
			s.Description = agentPtr(r.Description)
		}
	}
	required(apiutil.StringField(r.JSON.System) || r.JSON.System.Raw() == "null")
	if apiutil.StringField(r.JSON.System) {
		s.System = nil
		if options.importing || prior.System != nil {
			s.System = agentPtr(r.System)
		}
	}
	required(agentMetadataValid(r))
	if r.JSON.Metadata.Valid() {
		s.Metadata = agentObservedMetadata(r, prior.Metadata, options)
	}
	required(r.JSON.MCPServers.Valid())
	if r.JSON.MCPServers.Valid() {
		a := make([]MCPServer, len(r.MCPServers))
		good := true
		for i, x := range r.MCPServers {
			if !apiutil.StringField(x.JSON.Name) || !apiutil.StringField(x.JSON.URL) || !apiutil.StringField(x.JSON.Type) || x.Name == "" || x.Type != "url" {
				good = false
			}
			if apiutil.StringField(x.JSON.Name) {
				a[i].Name = x.Name
			}
			if apiutil.StringField(x.JSON.URL) {
				a[i].URL = x.URL
			}
		}
		if !good {
			invalid = true
		}
		if good {
			s.MCPServers = nil
			if options.importing || prior.MCPServers != nil {
				s.MCPServers = &a
			}
		}
	}
	required(r.JSON.Skills.Valid())
	if r.JSON.Skills.Valid() {
		a := make([]SkillRef, len(r.Skills))
		good := true
		for i, x := range r.Skills {
			if !apiutil.StringField(x.JSON.Type) || !apiutil.StringField(x.JSON.SkillID) || (x.Type != "custom" && x.Type != "anthropic") || x.SkillID == "" {
				good = false
			}
			if apiutil.StringField(x.JSON.Type) && (x.Type == "custom" || x.Type == "anthropic") {
				a[i].Type = SkillType(x.Type)
			}
			if apiutil.StringField(x.JSON.SkillID) {
				a[i].SkillID = x.SkillID
			}
			if !apiutil.StringField(x.JSON.Version) {
				good = false
			} else if options.importing || i >= len(agentSlice(prior.Skills)) || (*prior.Skills)[i].Version != nil {
				a[i].Version = agentPtr(x.Version)
			}
		}
		if !good {
			invalid = true
		}
		if good {
			s.Skills = nil
			if options.importing || prior.Skills != nil {
				s.Skills = &a
			}
		}
	}
	required(r.JSON.Tools.Valid())
	if r.JSON.Tools.Valid() {
		a, err := agentStrictTools(r.Tools, agentSlice(prior.Tools), options.importing)
		if err != nil {
			invalid = true
		}
		if err == nil {
			s.Tools = nil
			if options.importing || prior.Tools != nil {
				s.Tools = &a
			}
		}
	}
	if raw := r.JSON.Multiagent.Raw(); raw == "null" {
		s.Multiagent = nil
	} else if raw == "" {
		invalid = true
	} else {
		m, err := agentObservedMulti(r.Multiagent, prior.Multiagent, options.importing, r.ID)
		if err != nil {
			invalid = true
		} else if options.importing || prior.Multiagent != nil {
			s.Multiagent = m
		}
	}
	if raw := r.JSON.ExecutionIdentity.Raw(); raw != "" {
		x := r.ExecutionIdentity
		if !r.JSON.ExecutionIdentity.Valid() || !apiutil.StringField(x.JSON.Type) || (x.Type != "aws_role" && x.Type != "service_account") || (x.Type == "aws_role" && (!apiutil.StringField(x.JSON.RoleARN) || x.RoleARN == "")) {
			invalid = true
		} else {
			s.ExecutionIdentity = nil
			if options.importing || prior.ExecutionIdentity != nil {
				s.ExecutionIdentity = &ExecutionIdentity{Type: x.Type}
				if x.Type == "aws_role" {
					s.ExecutionIdentity.RoleARN = agentPtr(x.RoleARN)
				}
			}
		}
	} else {
		invalid = true
	}
	required(apiutil.StringField(r.JSON.CreatedAt) && !r.CreatedAt.IsZero())
	if apiutil.StringField(r.JSON.CreatedAt) && !r.CreatedAt.IsZero() {
		s.CreatedAt = r.CreatedAt.Format(time.RFC3339Nano)
	}
	required(apiutil.StringField(r.JSON.UpdatedAt) && !r.UpdatedAt.IsZero())
	if apiutil.StringField(r.JSON.UpdatedAt) && !r.UpdatedAt.IsZero() {
		s.UpdatedAt = r.UpdatedAt.Format(time.RFC3339Nano)
	}
	required(apiutil.Int64Field(r.JSON.Version) && r.Version >= 1)
	if apiutil.Int64Field(r.JSON.Version) && r.Version >= 1 {
		s.Version = int(r.Version)
	}
	if raw := r.JSON.ArchivedAt.Raw(); raw != "null" && (raw == "" || !apiutil.StringField(r.JSON.ArchivedAt)) {
		invalid = true
	}
	if r.JSON.Type.Raw() != "" && (!apiutil.StringField(r.JSON.Type) || r.Type != "agent") {
		invalid = true
	}
	if invalid {
		return AgentState{}, fmt.Errorf("incomplete or invalid agent response")
	}
	return s, nil
}

func agentStrictTools(v []sdk.BetaManagedAgentsAgentToolUnion, prior []Tool, importing bool) ([]Tool, error) {
	a := make([]Tool, len(v))
	for i, x := range v {
		if !agentToolValid(x) {
			return nil, fmt.Errorf("invalid agent tool response")
		}
		t := Tool{Type: ToolType(x.Type)}
		old := Tool{}
		if i < len(prior) && prior[i].Type == t.Type {
			old = prior[i]
		}
		switch t.Type {
		case AgentToolset, MCPToolset:
			if !x.JSON.DefaultConfig.Valid() || !x.JSON.Configs.Valid() {
				return nil, fmt.Errorf("incomplete toolset response")
			}
			if t.Type == MCPToolset {
				if !x.JSON.MCPServerName.Valid() || x.MCPServerName == "" {
					return nil, fmt.Errorf("invalid MCP server reference")
				}
				t.MCPServerName = agentPtr(x.MCPServerName)
			}
			if x.JSON.DefaultConfig.Raw() != "" && x.JSON.DefaultConfig.Raw() != "null" {
				d := x.DefaultConfig
				if !x.JSON.DefaultConfig.Valid() || !d.JSON.Enabled.Valid() || !d.JSON.PermissionPolicy.Valid() || !agentPermission(d.PermissionPolicy.Type) {
					return nil, fmt.Errorf("invalid tool defaults")
				}
				if importing || old.DefaultConfig != nil {
					t.DefaultConfig = &ToolDefaults{}
					if d.JSON.Enabled.Valid() && (importing || old.DefaultConfig.Enabled != nil) {
						t.DefaultConfig.Enabled = agentPtr(d.Enabled)
					}
					if d.JSON.PermissionPolicy.Valid() && (importing || old.DefaultConfig.PermissionPolicy != nil) {
						t.DefaultConfig.PermissionPolicy = agentPtr(PermissionPolicy(d.PermissionPolicy.Type))
					}
				}
			}
			if x.JSON.Configs.Raw() != "" && x.JSON.Configs.Raw() != "null" {
				if !x.JSON.Configs.Valid() {
					return nil, fmt.Errorf("invalid tool configs")
				}
				if t.Type == AgentToolset {
					configs := x.Configs.OfBetaManagedAgentsAgentToolConfigArray
					t.Configs = agentPtr(make([]ToolConfig, len(configs)))
					for j, c := range configs {
						if !c.JSON.Name.Valid() || c.Name == "" || !c.JSON.Enabled.Valid() || !c.JSON.PermissionPolicy.Valid() || !agentPermission(c.PermissionPolicy.Type) {
							return nil, fmt.Errorf("invalid builtin tool config")
						}
						conf := ToolConfig{Name: c.Name}
						oldConf := ToolConfig{}
						if j < len(agentSlice(old.Configs)) && (*old.Configs)[j].Name == c.Name {
							oldConf = (*old.Configs)[j]
						}
						if c.JSON.Enabled.Valid() && (importing || oldConf.Enabled != nil) {
							conf.Enabled = agentPtr(c.Enabled)
						}
						if c.JSON.PermissionPolicy.Valid() && (importing || oldConf.PermissionPolicy != nil) {
							conf.PermissionPolicy = agentPtr(PermissionPolicy(c.PermissionPolicy.Type))
						}
						if c.JSON.AllowedDomains.Valid() {
							conf.AllowedDomains = agentPtr(append([]string{}, c.AllowedDomains...))
						}
						if c.JSON.BlockedDomains.Valid() {
							conf.BlockedDomains = agentPtr(append([]string{}, c.BlockedDomains...))
						}
						if c.JSON.MaxContentTokens.Valid() {
							conf.MaxContentTokens = agentPtr(int(c.MaxContentTokens))
						}
						if c.JSON.UserLocation.Valid() {
							l := c.UserLocation
							conf.UserLocation = &SearchLocation{}
							if l.JSON.City.Valid() {
								conf.UserLocation.City = agentPtr(l.City)
							}
							if l.JSON.Country.Valid() {
								conf.UserLocation.Country = agentPtr(l.Country)
							}
							if l.JSON.Region.Valid() {
								conf.UserLocation.Region = agentPtr(l.Region)
							}
							if l.JSON.Timezone.Valid() {
								conf.UserLocation.Timezone = agentPtr(l.Timezone)
							}
						}
						if c.JSON.URLSources.Valid() {
							u := c.URLSources
							conf.URLSources = &FetchURLSources{}
							var err error
							if u.JSON.ClientToolResults.Valid() {
								conf.URLSources.ClientToolResults, err = agentObservedFetchFilter(u.ClientToolResults)
								if err != nil {
									return nil, err
								}
							}
							if u.JSON.ServerToolResults.Valid() {
								conf.URLSources.ServerToolResults, err = agentObservedFetchFilter(u.ServerToolResults)
								if err != nil {
									return nil, err
								}
							}
							if u.JSON.UserInput.Valid() {
								if u.UserInput.Type != "all" && u.UserInput.Type != "none" {
									return nil, fmt.Errorf("invalid user URL source")
								}
								conf.URLSources.UserInput = agentPtr(u.UserInput.Type)
							}
						}
						(*t.Configs)[j] = conf
					}
				} else {
					configs := x.Configs.OfBetaManagedAgentsMCPToolConfigArray
					t.Configs = agentPtr(make([]ToolConfig, len(configs)))
					for j, c := range configs {
						if !c.JSON.Name.Valid() || c.Name == "" || !c.JSON.Enabled.Valid() || !c.JSON.PermissionPolicy.Valid() || !agentPermission(string(c.PermissionPolicy.Type)) {
							return nil, fmt.Errorf("invalid MCP tool config")
						}
						conf := ToolConfig{Name: c.Name}
						oldConf := ToolConfig{}
						if j < len(agentSlice(old.Configs)) && (*old.Configs)[j].Name == c.Name {
							oldConf = (*old.Configs)[j]
						}
						if c.JSON.Enabled.Valid() && (importing || oldConf.Enabled != nil) {
							conf.Enabled = agentPtr(c.Enabled)
						}
						if c.JSON.PermissionPolicy.Valid() && (importing || oldConf.PermissionPolicy != nil) {
							conf.PermissionPolicy = agentPtr(PermissionPolicy(c.PermissionPolicy.Type))
						}
						(*t.Configs)[j] = conf
					}
				}
			}
			// Get may return default empty config lists that New omits. Keep
			// never-configured lists absent, like the other optional tool settings.
			if !importing && old.Configs == nil {
				t.Configs = nil
			}
		case CustomTool:
			if !x.JSON.Name.Valid() || !x.JSON.Description.Valid() || !x.JSON.InputSchema.Valid() {
				return nil, fmt.Errorf("invalid custom tool")
			}
			t.Name = agentPtr(x.Name)
			t.Description = agentPtr(x.Description)
			if err := json.Unmarshal([]byte(x.InputSchema.RawJSON()), &t.InputSchema); err != nil {
				return nil, fmt.Errorf("invalid custom tool schema")
			}
		default:
			return nil, fmt.Errorf("unknown agent tool variant")
		}
		a[i] = t
	}
	return a, nil
}

func agentObservedFetchFilter(v sdk.BetaManagedAgentsWebFetchURLSourceToolFilterUnion) (*FetchToolFilter, error) {
	if !apiutil.StringField(v.JSON.Type) || v.Type != "all" && v.Type != "none" && v.Type != "only" && v.Type != "except" {
		return nil, fmt.Errorf("invalid URL tool filter")
	}
	r := &FetchToolFilter{Type: v.Type}
	if v.Type == "only" || v.Type == "except" {
		if !v.JSON.Tools.Valid() {
			return nil, fmt.Errorf("invalid URL tool references")
		}
		r.Tools = agentPtr(make([]string, len(v.Tools)))
		for i, x := range v.Tools {
			if !apiutil.StringField(x.JSON.Name) || !apiutil.StringField(x.JSON.Type) || x.Name == "" || x.Type != "tool_reference" {
				return nil, fmt.Errorf("invalid URL tool reference")
			}
			(*r.Tools)[i] = x.Name
		}
	}
	return r, nil
}

func agentObservedReferences(v []sdk.BetaManagedAgentsAgentReference, prior []RosterEntry, importing bool, owner string) ([]RosterEntry, error) {
	var out []RosterEntry
	if importing || prior != nil {
		out = make([]RosterEntry, 0, len(v))
	}
	for i, a := range v {
		if !apiutil.StringField(a.JSON.Type) || a.Type != "agent" || !apiutil.StringField(a.JSON.ID) || !apiutil.Int64Field(a.JSON.Version) || a.ID == "" || a.Version < 1 {
			return nil, fmt.Errorf("invalid predefined agent reference")
		}
		r := RosterEntry{Type: "agent", ID: agentPtr(a.ID)}
		if !importing && i < len(prior) && prior[i].Type == "self" && a.ID == owner {
			r.Type = "self"
			r.ID = nil
		} else if importing || i >= len(prior) || prior[i].Version != nil {
			r.Version = agentPtr(int(a.Version))
		}
		if importing || prior != nil {
			out = append(out, r)
		}
	}
	return out, nil
}

func agentObservedMulti(x sdk.BetaManagedAgentsMultiagentUnion, old *Multiagent, importing bool, owner string) (*Multiagent, error) {
	if !apiutil.StringField(x.JSON.Type) {
		return nil, fmt.Errorf("invalid orchestration discriminator")
	}
	m := &Multiagent{}
	if importing || old == nil || old.Type != nil {
		m.Type = agentPtr(x.Type)
	}
	switch x.Type {
	case "coordinator":
		if !x.JSON.Agents.Valid() {
			return nil, fmt.Errorf("invalid coordinator roster")
		}
		m.Agents = agentPtr(make([]RosterEntry, len(x.Agents)))
		for i, a := range x.Agents {
			r := RosterEntry{Type: RosterEntryType(a.Type)}
			switch a.Type {
			case "agent":
				if !apiutil.StringField(a.JSON.ID) || !apiutil.Int64Field(a.JSON.Version) || a.Version < 1 {
					return nil, fmt.Errorf("invalid agent reference")
				}
				r.ID = agentPtr(a.ID)
				if !importing && old != nil && i < len(agentSlice(old.Agents)) && (*old.Agents)[i].Type == "self" && a.ID == owner {
					r.Type = "self"
					r.ID = nil
				} else if importing || old == nil || i >= len(agentSlice(old.Agents)) || (*old.Agents)[i].Version != nil {
					r.Version = agentPtr(int(a.Version))
				}
			case "advisor":
				if !apiutil.StringField(a.JSON.Model) {
					return nil, fmt.Errorf("invalid advisor reference")
				}
				r.Model = agentPtr(a.Model)
			default:
				return nil, fmt.Errorf("unknown coordinator roster variant")
			}
			(*m.Agents)[i] = r
		}
	case "multiagent_20261001":
		m.Type = agentPtr(x.Type)
		if !x.JSON.Advisor.Valid() || !x.JSON.Subagents.Valid() || !x.JSON.Workflows.Valid() {
			return nil, fmt.Errorf("incomplete modern orchestration")
		}
		if !apiutil.StringField(x.Advisor.JSON.Type) || x.Advisor.Type != "enabled" && x.Advisor.Type != "disabled" || x.Advisor.Type == "enabled" && (!apiutil.StringField(x.Advisor.JSON.Model) || x.Advisor.Model == "") {
			return nil, fmt.Errorf("invalid advisor variant")
		}
		if importing || old == nil || old.Advisor != nil {
			m.Advisor = &AgentAdvisor{Type: x.Advisor.Type}
			if x.Advisor.Type == "enabled" {
				if !apiutil.StringField(x.Advisor.JSON.Model) || x.Advisor.Model == "" {
					return nil, fmt.Errorf("invalid advisor model")
				}
				m.Advisor.Model = agentPtr(x.Advisor.Model)
			}
		}
		sub := x.Subagents
		work := x.Workflows
		if !apiutil.StringField(sub.JSON.Type) || !apiutil.StringField(work.JSON.Type) {
			return nil, fmt.Errorf("invalid delegation discriminator")
		}
		if sub.Type == "enabled" {
			if !apiutil.StringField(sub.InlineAgents.JSON.Type) || sub.InlineAgents.Type != "enabled" && sub.InlineAgents.Type != "disabled" || !agentNullableField(sub.JSON.PredefinedAgents) {
				return nil, fmt.Errorf("invalid subagent configuration")
			}
			if _, err := agentObservedReferences(sub.PredefinedAgents, nil, true, owner); err != nil {
				return nil, err
			}
		}
		if work.Type == "enabled" {
			if !apiutil.StringField(work.InlineAgents.JSON.Type) || work.InlineAgents.Type != "enabled" && work.InlineAgents.Type != "disabled" || !agentNullableField(work.JSON.PredefinedAgents) {
				return nil, fmt.Errorf("invalid workflow configuration")
			}
			if _, err := agentObservedReferences(work.PredefinedAgents, nil, true, owner); err != nil {
				return nil, err
			}
		}
		if sub.Type != "enabled" && sub.Type != "disabled" || work.Type != "enabled" && work.Type != "disabled" {
			return nil, fmt.Errorf("invalid delegation variant")
		}
		if importing || old == nil || old.Subagents != nil {
			m.Subagents = &AgentDelegation{Type: sub.Type}
			if sub.Type == "enabled" {
				if sub.InlineAgents.Type != "enabled" && sub.InlineAgents.Type != "disabled" {
					return nil, fmt.Errorf("invalid inline agents")
				}
				if importing || old == nil || old.Subagents == nil || old.Subagents.InlineAgents != nil || sub.InlineAgents.Type != "enabled" {
					m.Subagents.InlineAgents = agentPtr(sub.InlineAgents.Type)
				}
				var refs []RosterEntry
				if old != nil && old.Subagents != nil {
					refs = agentSlice(old.Subagents.PredefinedAgents)
				}
				observed, err := agentObservedReferences(sub.PredefinedAgents, refs, importing, owner)
				if observed != nil {
					m.Subagents.PredefinedAgents = &observed
				}
				if err != nil {
					return nil, err
				}
			}
		}
		if importing || old == nil || old.Workflows != nil {
			m.Workflows = &AgentDelegation{Type: work.Type}
			if work.Type == "enabled" {
				if work.InlineAgents.Type != "enabled" && work.InlineAgents.Type != "disabled" {
					return nil, fmt.Errorf("invalid inline agents")
				}
				if importing || old == nil || old.Workflows == nil || old.Workflows.InlineAgents != nil || work.InlineAgents.Type != "enabled" {
					m.Workflows.InlineAgents = agentPtr(work.InlineAgents.Type)
				}
				var refs []RosterEntry
				if old != nil && old.Workflows != nil {
					refs = agentSlice(old.Workflows.PredefinedAgents)
				}
				observed, err := agentObservedReferences(work.PredefinedAgents, refs, importing, owner)
				if observed != nil {
					m.Workflows.PredefinedAgents = &observed
				}
				if err != nil {
					return nil, err
				}
			}
		}
	default:
		return nil, fmt.Errorf("unknown orchestration variant")
	}
	return m, nil
}

func agentNullableField(f respjson.Field) bool {
	return f.Raw() == "" || f.Raw() == "null" || f.Valid()
}

func agentToolValid(x sdk.BetaManagedAgentsAgentToolUnion) bool {
	if !apiutil.StringField(x.JSON.Type) {
		return false
	}
	if x.Type == "custom" && (!apiutil.StringField(x.JSON.Name) || !apiutil.StringField(x.JSON.Description)) {
		return false
	}
	if x.Type == "mcp_toolset" && !apiutil.StringField(x.JSON.MCPServerName) {
		return false
	}
	if x.Type == "agent_toolset_20260401" || x.Type == "mcp_toolset" {
		if !x.JSON.DefaultConfig.Valid() || !x.JSON.Configs.Valid() {
			return false
		}
	}
	if x.JSON.DefaultConfig.Raw() != "" && x.JSON.DefaultConfig.Raw() != "null" {
		d := x.DefaultConfig
		if !apiutil.BoolField(d.JSON.Enabled) || !apiutil.StringField(d.PermissionPolicy.JSON.Type) {
			return false
		}
	}
	if x.Type == "mcp_toolset" {
		for _, c := range x.Configs.OfBetaManagedAgentsMCPToolConfigArray {
			if !apiutil.StringField(c.JSON.Name) || !apiutil.BoolField(c.JSON.Enabled) || !apiutil.StringField(c.PermissionPolicy.JSON.Type) {
				return false
			}
		}
	}
	if x.Type == "agent_toolset_20260401" {
		for _, c := range x.Configs.OfBetaManagedAgentsAgentToolConfigArray {
			if !apiutil.StringField(c.JSON.Name) || !apiutil.StringField(c.JSON.Type) || c.Type != c.Name || !apiutil.BoolField(c.JSON.Enabled) || !apiutil.StringField(c.PermissionPolicy.JSON.Type) {
				return false
			}
			switch c.Type {
			case "bash", "edit", "read", "write", "glob", "grep", "web_fetch", "web_search":
			default:
				return false
			}
			for _, f := range []respjson.Field{c.JSON.MaxContentTokens, c.JSON.UserLocation, c.JSON.URLSources} {
				if !agentNullableField(f) {
					return false
				}
			}
			for _, f := range []respjson.Field{c.JSON.AllowedDomains, c.JSON.BlockedDomains} {
				if f.Raw() == "null" || f.Raw() != "" && !apiutil.StringListField(f) {
					return false
				}
			}
			if c.Type == "web_fetch" && c.JSON.URLSources.Raw() == "" {
				return false
			}
			if c.JSON.MaxContentTokens.Raw() != "" && c.JSON.MaxContentTokens.Raw() != "null" && (!apiutil.Int64Field(c.JSON.MaxContentTokens) || c.MaxContentTokens < 1) {
				return false
			}
			if c.JSON.UserLocation.Valid() {
				l := c.UserLocation
				if !apiutil.StringField(l.JSON.Type) || l.Type != "approximate" {
					return false
				}
				for _, f := range []respjson.Field{l.JSON.City, l.JSON.Country, l.JSON.Region, l.JSON.Timezone} {
					if f.Raw() != "" && f.Raw() != "null" && !apiutil.StringField(f) {
						return false
					}
				}
			}
			if c.JSON.URLSources.Valid() {
				u := c.URLSources
				for _, f := range []respjson.Field{u.JSON.ClientToolResults, u.JSON.ServerToolResults, u.JSON.UserInput} {
					// These response members are required (but nullable). Missing
					// members are incomplete observations, not configured defaults.
					if f.Raw() == "" || !agentNullableField(f) {
						return false
					}
				}
			}
		}
	}
	return true
}

func agentObservedMetadata(r *sdk.BetaManagedAgentsAgent, prior map[string]string, options agentObservationOptions) map[string]string {
	if !options.importing && prior == nil {
		return nil
	}
	out := map[string]string{}
	var leaves map[string]json.RawMessage
	if json.Unmarshal([]byte(r.JSON.Metadata.Raw()), &leaves) != nil {
		return nil
	}
	for key, raw := range leaves {
		if !options.importing {
			if _, managed := prior[key]; !managed {
				continue
			}
		}
		if value, valid := apiutil.JSONStringValue(string(raw)); valid {
			out[key] = value
		}
	}
	return out
}

func agentMetadataValid(r *sdk.BetaManagedAgentsAgent) bool {
	return apiutil.StringMapField(r.JSON.Metadata)
}

func agentOpt[T comparable](v *T) param.Opt[T] {
	if v != nil {
		return param.NewOpt(*v)
	}
	return param.Opt[T]{}
}

// agentSlice preserves omission while making a present pointer to a nil slice
// an explicit empty collection for SDK conversions.
func agentSlice[T any](v *[]T) []T {
	if v == nil {
		return nil
	}
	if *v == nil {
		return []T{}
	}
	return *v
}

func agentModel(v Model) sdk.BetaManagedAgentsModelConfigParams {
	r := sdk.BetaManagedAgentsModelConfigParams{ID: v.ID, InferenceGeo: agentOpt(v.InferenceGeo)}
	if v.Speed != nil {
		r.Speed = sdk.BetaManagedAgentsModelConfigParamsSpeed(*v.Speed)
	}
	if v.Effort != nil {
		// The API accepts the string form as well as the response's tagged form.
		r.Effort.OfBetaManagedAgentsModelConfigsEffortBetaManagedAgentsEffortLevel = sdk.String(string(*v.Effort))
	}
	return r
}

func agentPolicy(v *PermissionPolicy) sdk.BetaManagedAgentsAgentToolsetDefaultConfigParamsPermissionPolicyUnion {
	r := sdk.BetaManagedAgentsAgentToolsetDefaultConfigParamsPermissionPolicyUnion{}
	if v == nil {
		return r
	}
	switch *v {
	case "always_allow":
		r.OfAlwaysAllow = &sdk.BetaManagedAgentsAlwaysAllowPolicyParam{}
	case "always_ask":
		r.OfAlwaysAsk = &sdk.BetaManagedAgentsAlwaysAskPolicyParam{}
	case "auto":
		r.OfAuto = &sdk.BetaManagedAgentsAutoPolicyParam{}
	}
	return r
}

func agentBuiltin(v ToolConfig) (sdk.BetaManagedAgentsAgentToolConfigParamsUnion, error) {
	r := sdk.BetaManagedAgentsAgentToolConfigParamsUnion{}
	p := agentPolicy(v.PermissionPolicy)
	e := agentOpt(v.Enabled)
	switch v.Name {
	case "bash":
		r.OfBash = &sdk.BetaManagedAgentsBashToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsBashToolConfigParamsPermissionPolicyUnion(p)}
	case "edit":
		r.OfEdit = &sdk.BetaManagedAgentsEditToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsEditToolConfigParamsPermissionPolicyUnion(p)}
	case "read":
		r.OfRead = &sdk.BetaManagedAgentsReadToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsReadToolConfigParamsPermissionPolicyUnion(p)}
	case "write":
		r.OfWrite = &sdk.BetaManagedAgentsWriteToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsWriteToolConfigParamsPermissionPolicyUnion(p)}
	case "glob":
		r.OfGlob = &sdk.BetaManagedAgentsGlobToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsGlobToolConfigParamsPermissionPolicyUnion(p)}
	case "grep":
		r.OfGrep = &sdk.BetaManagedAgentsGrepToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsGrepToolConfigParamsPermissionPolicyUnion(p)}
	case "web_fetch":
		x := &sdk.BetaManagedAgentsWebFetchToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsWebFetchToolConfigParamsPermissionPolicyUnion(p), AllowedDomains: agentSlice(v.AllowedDomains), BlockedDomains: agentSlice(v.BlockedDomains)}
		if v.MaxContentTokens != nil {
			x.MaxContentTokens = sdk.Int(int64(*v.MaxContentTokens))
		}
		if v.URLSources != nil {
			u := v.URLSources
			x.URLSources = sdk.BetaManagedAgentsWebFetchURLSourcesParams{ClientToolResults: agentFetchFilter(u.ClientToolResults), ServerToolResults: agentFetchFilter(u.ServerToolResults)}
			if u.UserInput != nil {
				x.URLSources.UserInput.OfBetaManagedAgentsWebFetchURLSourceShorthand = param.NewOpt(sdk.BetaManagedAgentsWebFetchURLSourceShorthand(*u.UserInput))
			}
		}
		r.OfWebFetch = x
	case "web_search":
		x := &sdk.BetaManagedAgentsWebSearchToolConfigParams{Enabled: e, PermissionPolicy: sdk.BetaManagedAgentsWebSearchToolConfigParamsPermissionPolicyUnion(p), AllowedDomains: agentSlice(v.AllowedDomains), BlockedDomains: agentSlice(v.BlockedDomains)}
		if v.UserLocation != nil {
			l := v.UserLocation
			x.UserLocation = sdk.BetaManagedAgentsUserLocationParam{City: agentOpt(l.City), Country: agentOpt(l.Country), Region: agentOpt(l.Region), Timezone: agentOpt(l.Timezone)}
		}
		r.OfWebSearch = x
	default:
		return r, fmt.Errorf("unknown built-in tool name")
	}
	extras := map[string]any{}
	if v.AllowedDomains != nil && len(agentSlice(v.AllowedDomains)) == 0 {
		extras["allowed_domains"] = []any{}
	}
	if v.BlockedDomains != nil && len(agentSlice(v.BlockedDomains)) == 0 {
		extras["blocked_domains"] = []any{}
	}
	if r.OfWebFetch != nil {
		if v.URLSources != nil {
			extras["url_sources"] = r.OfWebFetch.URLSources
		}
		r.OfWebFetch.SetExtraFields(extras)
	}
	if r.OfWebSearch != nil {
		r.OfWebSearch.SetExtraFields(extras)
	}
	return r, nil
}

func agentFetchFilter(v *FetchToolFilter) sdk.BetaManagedAgentsWebFetchURLSourceToolFilterParamsUnion {
	r := sdk.BetaManagedAgentsWebFetchURLSourceToolFilterParamsUnion{}
	if v == nil {
		return r
	}
	x := &sdk.BetaManagedAgentsWebFetchURLSourceToolFilterUnionParam{}
	refs := make([]sdk.BetaManagedAgentsWebFetchURLSourceToolReferenceParam, len(agentSlice(v.Tools)))
	for i, n := range agentSlice(v.Tools) {
		refs[i] = sdk.BetaManagedAgentsWebFetchURLSourceToolReferenceParam{Name: n}
	}
	switch v.Type {
	case "all":
		x.OfAll = &sdk.BetaManagedAgentsWebFetchURLSourceAllParam{}
	case "none":
		x.OfNone = &sdk.BetaManagedAgentsWebFetchURLSourceNoneParam{}
	case "only":
		x.OfOnly = &sdk.BetaManagedAgentsWebFetchURLSourceOnlyParam{Tools: refs}
	case "except":
		x.OfExcept = &sdk.BetaManagedAgentsWebFetchURLSourceExceptParam{Tools: refs}
	}
	r.OfBetaManagedAgentsWebFetchURLSourceToolFilterUnion = x
	return r
}

func agentTools(v []Tool) ([]sdk.BetaAgentNewParamsToolUnion, error) {
	if v == nil {
		return nil, nil
	}
	r := make([]sdk.BetaAgentNewParamsToolUnion, len(v))
	for i, t := range v {
		switch t.Type {
		case AgentToolset:
			x := &sdk.BetaManagedAgentsAgentToolset20260401Params{}
			if t.Configs != nil {
				x.Configs = make([]sdk.BetaManagedAgentsAgentToolConfigParamsUnion, len(agentSlice(t.Configs)))
				for j, c := range agentSlice(t.Configs) {
					var err error
					x.Configs[j], err = agentBuiltin(c)
					if err != nil {
						return nil, err
					}
				}
			}
			if t.DefaultConfig != nil {
				x.DefaultConfig = sdk.BetaManagedAgentsAgentToolsetDefaultConfigParams{Enabled: agentOpt(t.DefaultConfig.Enabled), PermissionPolicy: agentPolicy(t.DefaultConfig.PermissionPolicy)}
			}
			extras := map[string]any{}
			if t.Configs != nil && len(agentSlice(t.Configs)) == 0 {
				extras["configs"] = []any{}
			}
			if t.DefaultConfig != nil {
				extras["default_config"] = x.DefaultConfig
			}
			x.SetExtraFields(extras)
			r[i].OfAgentToolset20260401 = x
		case MCPToolset:
			x := &sdk.BetaManagedAgentsMCPToolsetParams{}
			if t.MCPServerName != nil {
				x.MCPServerName = *t.MCPServerName
			}
			if t.Configs != nil {
				x.Configs = make([]sdk.BetaManagedAgentsMCPToolConfigParams, len(agentSlice(t.Configs)))
				for j, c := range agentSlice(t.Configs) {
					x.Configs[j] = sdk.BetaManagedAgentsMCPToolConfigParams{Name: c.Name, Enabled: agentOpt(c.Enabled), PermissionPolicy: sdk.BetaManagedAgentsMCPToolConfigParamsPermissionPolicyUnion(agentPolicy(c.PermissionPolicy))}
				}
			}
			if t.DefaultConfig != nil {
				x.DefaultConfig = sdk.BetaManagedAgentsMCPToolsetDefaultConfigParams{Enabled: agentOpt(t.DefaultConfig.Enabled), PermissionPolicy: sdk.BetaManagedAgentsMCPToolsetDefaultConfigParamsPermissionPolicyUnion(agentPolicy(t.DefaultConfig.PermissionPolicy))}
			}
			extras := map[string]any{}
			if t.Configs != nil && len(agentSlice(t.Configs)) == 0 {
				extras["configs"] = []any{}
			}
			if t.DefaultConfig != nil {
				extras["default_config"] = x.DefaultConfig
			}
			x.SetExtraFields(extras)
			r[i].OfMCPToolset = x
		case CustomTool:
			x := &sdk.BetaManagedAgentsCustomToolParams{}
			if t.Name != nil {
				x.Name = *t.Name
			}
			if t.Description != nil {
				x.Description = *t.Description
			}
			// JSON Schema is intentionally arbitrary user JSON, not an inferred wire type.
			x.InputSchema = param.Override[sdk.BetaManagedAgentsCustomToolInputSchemaParam](maps.Clone(t.InputSchema))
			r[i].OfCustom = x
		default:
			return nil, fmt.Errorf("unknown tool type")
		}
	}
	return r, nil
}

func agentReferences(v []RosterEntry) []sdk.BetaManagedAgentsMultiagentPredefinedAgentParamsUnion {
	if v == nil {
		return nil
	}
	r := make([]sdk.BetaManagedAgentsMultiagentPredefinedAgentParamsUnion, len(v))
	for i, x := range v {
		if x.Type == "self" {
			r[i].OfBetaManagedAgentsMultiagentSelfs = &sdk.BetaManagedAgentsMultiagentSelfParams{}
		} else {
			a := &sdk.BetaManagedAgentsAgentParams{}
			if x.ID != nil {
				a.ID = *x.ID
			}
			if x.Version != nil {
				a.Version = sdk.Int(int64(*x.Version))
			}
			r[i].OfBetaManagedAgentsAgents = a
		}
	}
	return r
}

func agentInline(v *string) sdk.BetaManagedAgentsMultiagentInlineAgentsParamsUnion {
	r := sdk.BetaManagedAgentsMultiagentInlineAgentsParamsUnion{}
	if v != nil {
		if *v == "enabled" {
			r.OfEnabled = &sdk.BetaManagedAgentsMultiagentInlineAgentsEnabledParams{}
		} else {
			r.OfDisabled = &sdk.BetaManagedAgentsMultiagentInlineAgentsDisabledParams{}
		}
	}
	return r
}

func agentMulti(v, old *Multiagent) sdk.BetaManagedAgentsMultiagentParamsUnion {
	r := sdk.BetaManagedAgentsMultiagentParamsUnion{}
	if v == nil {
		if old != nil {
			return param.NullStruct[sdk.BetaManagedAgentsMultiagentParamsUnion]()
		}
		return r
	}
	if v.Type == nil || *v.Type == "coordinator" {
		refs := agentReferences(agentSlice(v.Agents))
		a := make([]sdk.BetaManagedAgentsMultiagentRosterEntryParamsUnion, len(refs))
		for i, x := range refs {
			a[i].OfBetaManagedAgentsAgents = x.OfBetaManagedAgentsAgents
			a[i].OfBetaManagedAgentsMultiagentSelfs = x.OfBetaManagedAgentsMultiagentSelfs
			if (*v.Agents)[i].Type == "advisor" && (*v.Agents)[i].Model != nil {
				a[i].OfBetaManagedAgentsAgents = nil
				a[i].OfBetaManagedAgentsAdvisors = &sdk.BetaManagedAgentsAdvisorParams{Model: *(*v.Agents)[i].Model}
			}
		}
		return sdk.BetaManagedAgentsMultiagentParamsOfCoordinator(a)
	}
	x := &sdk.BetaManagedAgentsMultiagent20261001Params{}
	if v.Advisor != nil {
		if v.Advisor.Type == "enabled" {
			model := ""
			if v.Advisor.Model != nil {
				model = *v.Advisor.Model
			}
			x.Advisor = sdk.BetaManagedAgentsMultiagentAdvisorParamsOfEnabled(model)
		} else {
			x.Advisor.OfDisabled = &sdk.BetaManagedAgentsMultiagentAdvisorDisabledParams{}
		}
	} else if old != nil && old.Advisor != nil {
		x.Advisor = param.NullStruct[sdk.BetaManagedAgentsMultiagentAdvisorParamsUnion]()
	}
	if d := v.Subagents; d != nil {
		if d.Type == "enabled" {
			y := &sdk.BetaManagedAgentsMultiagentSubagentsEnabledParams{PredefinedAgents: agentReferences(agentSlice(d.PredefinedAgents)), InlineAgents: agentInline(d.InlineAgents)}
			if old != nil && old.Subagents != nil {
				if d.PredefinedAgents == nil && old.Subagents.PredefinedAgents != nil {
					y.PredefinedAgents = param.NullSlice[[]sdk.BetaManagedAgentsMultiagentPredefinedAgentParamsUnion]()
				}
				if d.InlineAgents == nil && old.Subagents.InlineAgents != nil {
					y.InlineAgents = param.NullStruct[sdk.BetaManagedAgentsMultiagentInlineAgentsParamsUnion]()
				}
			}
			if d.PredefinedAgents != nil && len(agentSlice(d.PredefinedAgents)) == 0 {
				y.SetExtraFields(map[string]any{"predefined_agents": []any{}})
			}
			x.Subagents.OfEnabled = y
		} else {
			x.Subagents.OfDisabled = &sdk.BetaManagedAgentsMultiagentSubagentsDisabledParams{}
		}
	} else if old != nil && old.Subagents != nil {
		x.Subagents = param.NullStruct[sdk.BetaManagedAgentsMultiagentSubagentsParamsUnion]()
	}
	if d := v.Workflows; d != nil {
		if d.Type == "enabled" {
			y := &sdk.BetaManagedAgentsMultiagentWorkflowsEnabledParams{PredefinedAgents: agentReferences(agentSlice(d.PredefinedAgents)), InlineAgents: agentInline(d.InlineAgents)}
			if old != nil && old.Workflows != nil {
				if d.PredefinedAgents == nil && old.Workflows.PredefinedAgents != nil {
					y.PredefinedAgents = param.NullSlice[[]sdk.BetaManagedAgentsMultiagentPredefinedAgentParamsUnion]()
				}
				if d.InlineAgents == nil && old.Workflows.InlineAgents != nil {
					y.InlineAgents = param.NullStruct[sdk.BetaManagedAgentsMultiagentInlineAgentsParamsUnion]()
				}
			}
			if d.PredefinedAgents != nil && len(agentSlice(d.PredefinedAgents)) == 0 {
				y.SetExtraFields(map[string]any{"predefined_agents": []any{}})
			}
			x.Workflows.OfEnabled = y
		} else {
			x.Workflows.OfDisabled = &sdk.BetaManagedAgentsMultiagentWorkflowsDisabledParams{}
		}
	} else if old != nil && old.Workflows != nil {
		x.Workflows = param.NullStruct[sdk.BetaManagedAgentsMultiagentWorkflowsParamsUnion]()
	}
	r.OfMultiagent20261001 = x
	return r
}

func agentNew(v AgentArgs) (sdk.BetaAgentNewParams, error) {
	r := sdk.BetaAgentNewParams{Name: v.Name, Model: agentModel(v.Model), System: agentOpt(v.System), Description: agentOpt(v.Description), Metadata: maps.Clone(v.Metadata), Multiagent: agentMulti(v.Multiagent, nil)}
	var err error
	r.Tools, err = agentTools(agentSlice(v.Tools))
	if err != nil {
		return r, err
	}
	if v.MCPServers != nil {
		r.MCPServers = make([]sdk.BetaManagedAgentsURLMCPServerParams, len(agentSlice(v.MCPServers)))
		for i, s := range agentSlice(v.MCPServers) {
			r.MCPServers[i] = sdk.BetaManagedAgentsURLMCPServerParams{Name: s.Name, URL: s.URL}
		}
	}
	if v.Skills != nil {
		r.Skills = make([]sdk.BetaManagedAgentsSkillParamsUnion, len(agentSlice(v.Skills)))
		for i, s := range agentSlice(v.Skills) {
			if s.Type == "custom" {
				r.Skills[i].OfCustom = &sdk.BetaManagedAgentsCustomSkillParams{SkillID: s.SkillID, Version: agentOpt(s.Version)}
			} else {
				r.Skills[i].OfAnthropic = &sdk.BetaManagedAgentsAnthropicSkillParams{SkillID: s.SkillID, Version: agentOpt(s.Version)}
			}
		}
	}
	if v.ExecutionIdentity != nil {
		if v.ExecutionIdentity.Type == "aws_role" && v.ExecutionIdentity.RoleARN != nil {
			r.ExecutionIdentity = sdk.BetaManagedAgentsExecutionIdentityParamsOfAWSRole(*v.ExecutionIdentity.RoleARN)
		} else {
			r.ExecutionIdentity.OfServiceAccount = &sdk.BetaManagedAgentsServiceAccountExecutionIdentityParams{}
		}
	}
	extras := map[string]any{}
	if v.Tools != nil && len(agentSlice(v.Tools)) == 0 {
		extras["tools"] = []any{}
	}
	if v.Skills != nil && len(agentSlice(v.Skills)) == 0 {
		extras["skills"] = []any{}
	}
	if v.MCPServers != nil && len(agentSlice(v.MCPServers)) == 0 {
		extras["mcp_servers"] = []any{}
	}
	if v.Metadata != nil && len(v.Metadata) == 0 {
		extras["metadata"] = map[string]any{}
	}
	r.SetExtraFields(extras)
	return r, nil
}

func agentUpdate(old AgentState, v AgentArgs) (sdk.BetaAgentUpdateParams, error) {
	n, err := agentNew(v)
	if err != nil {
		return sdk.BetaAgentUpdateParams{}, err
	}
	r := sdk.BetaAgentUpdateParams{Name: sdk.String(v.Name), Model: n.Model, System: n.System, Description: n.Description, MCPServers: n.MCPServers, Skills: n.Skills, Multiagent: agentMulti(v.Multiagent, old.Multiagent), ExecutionIdentity: n.ExecutionIdentity}
	if old.Version > 0 {
		r.Version = sdk.Int(int64(old.Version))
	}
	if v.System == nil && old.System != nil {
		r.System = sdk.String("")
	}
	if v.Description == nil && old.Description != nil {
		r.Description = sdk.String("")
	}
	if v.MCPServers == nil && old.MCPServers != nil {
		r.MCPServers = []sdk.BetaManagedAgentsURLMCPServerParams{}
	}
	if v.Skills == nil && old.Skills != nil {
		r.Skills = []sdk.BetaManagedAgentsSkillParamsUnion{}
	}
	if n.Tools != nil {
		r.Tools = make([]sdk.BetaAgentUpdateParamsToolUnion, len(n.Tools))
		for i, t := range n.Tools {
			r.Tools[i] = sdk.BetaAgentUpdateParamsToolUnion(t)
		}
	} else if old.Tools != nil {
		r.Tools = []sdk.BetaAgentUpdateParamsToolUnion{}
	}
	if v.ExecutionIdentity == nil && old.ExecutionIdentity != nil {
		r.ExecutionIdentity = param.NullStruct[sdk.BetaManagedAgentsExecutionIdentityParamsUnion]()
	}
	// Model is replaced, except effort's documented omission preserves it. Do
	// not invent an unsupported null effort reset. warnKeptEffort explains this.
	if v.Model.Speed == nil && old.Model.Speed != nil {
		r.Model.Speed = "standard"
	}
	metadata := apiutil.MergedStringMap(old.Metadata, v.Metadata)
	extras := map[string]any{}
	if v.Metadata != nil || old.Metadata != nil {
		extras["metadata"] = metadata
	}
	if r.Tools != nil && len(r.Tools) == 0 {
		extras["tools"] = []any{}
	}
	if r.Skills != nil && len(r.Skills) == 0 {
		extras["skills"] = []any{}
	}
	if r.MCPServers != nil && len(r.MCPServers) == 0 {
		extras["mcp_servers"] = []any{}
	}
	r.SetExtraFields(extras)
	return r, nil
}
