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

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Model selects and configures the model an agent runs on.
type Model struct {
	ID           string  `pulumi:"id"`
	Effort       *Effort `pulumi:"effort,optional"`
	Speed        *Speed  `pulumi:"speed,optional"`
	InferenceGeo *string `pulumi:"inferenceGeo,optional"`
}

func (m *Model) Annotate(a infer.Annotator) {
	a.Describe(&m.ID, "The model ID, such as `claude-opus-5-5`.")
	a.Describe(&m.Effort, "How much effort the model spends reasoning. "+
		"Defaults to the model's own default, and is kept across changes that leave `id` unchanged.")
	a.Describe(&m.Speed, "The inference speed tier.")
	a.Describe(&m.InferenceGeo, "Where inference runs, such as `us` or `global`.")
}

// Effort is a model's reasoning effort level.
type Effort string

func (Effort) Values() []infer.EnumValue[Effort] {
	return []infer.EnumValue[Effort]{
		{Value: "low"}, {Value: "medium"}, {Value: "high"}, {Value: "xhigh"}, {Value: "max"},
	}
}

func (e Effort) MarshalJSON() ([]byte, error) { return rest.MarshalTypeTag(string(e)) }

func (e *Effort) UnmarshalJSON(raw []byte) error {
	s, err := rest.UnmarshalTypeTag(raw)
	*e = Effort(s)
	return err
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
	Configs       []ToolConfig   `pulumi:"configs,optional"`
	DefaultConfig *ToolDefaults  `pulumi:"defaultConfig,optional"`
	MCPServerName *string        `pulumi:"mcpServerName,optional"`
	Name          *string        `pulumi:"name,optional"`
	Description   *string        `pulumi:"description,optional"`
	InputSchema   map[string]any `pulumi:"inputSchema,optional"`
}

func (t *Tool) Annotate(a infer.Annotator) {
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
}

func (c *ToolConfig) Annotate(a infer.Annotator) {
	a.Describe(&c.Name, "The tool's name, such as `bash` or `web_fetch`.")
	a.Describe(&c.Enabled, "Whether the agent may use the tool.")
	a.Describe(&c.PermissionPolicy, "Whether a tool call needs approval.")
}

// ToolDefaults configures the tools of a toolset that have no entry of their own.
type ToolDefaults struct {
	Enabled          *bool             `pulumi:"enabled,optional"`
	PermissionPolicy *PermissionPolicy `pulumi:"permissionPolicy,optional"`
}

func (c *ToolDefaults) Annotate(a infer.Annotator) {
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

func (p PermissionPolicy) MarshalJSON() ([]byte, error) { return rest.MarshalTypeTag(string(p)) }

func (p *PermissionPolicy) UnmarshalJSON(raw []byte) error {
	s, err := rest.UnmarshalTypeTag(raw)
	*p = PermissionPolicy(s)
	return err
}

// MCPServer declares a remote MCP server an agent can connect to.
type MCPServer struct {
	Name string `pulumi:"name"`
	URL  string `pulumi:"url"`
}

func (s *MCPServer) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "The server's name, which an `mcp_toolset` tool refers to.")
	a.Describe(&s.URL, "The server's URL. Credentials for it are supplied by a vault at session time.")
}

func (MCPServer) ExtendWire(obj map[string]any) { obj["type"] = "url" }

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
	a.Describe(&s.Type, "Whether the skill is provided by Anthropic or uploaded to the organization.")
	a.Describe(&s.SkillID, "The skill's ID.")
	a.Describe(&s.Version, "The skill version to pin. Omit to always use the latest version.")
}

// Multiagent makes an agent a coordinator that delegates to a roster of agents.
type Multiagent struct {
	Agents []RosterEntry `pulumi:"agents"`
}

func (m *Multiagent) Annotate(a infer.Annotator) {
	a.Describe(&m.Agents, "The agents the coordinator can delegate to, between 1 and 20.")
}

func (Multiagent) ExtendWire(obj map[string]any) { obj["type"] = "coordinator" }

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
	a.Describe(&r.Type, "The kind of roster entry.")
	a.Describe(&r.ID, "The agent's ID. Required for `agent`.")
	a.Describe(&r.Version, "The agent version to delegate to. If omitted, the version current "+
		"when the coordinator is saved is pinned; later versions are not picked up.")
	a.Describe(&r.Model, "The advisor's model ID. Required for `advisor`.")
}
