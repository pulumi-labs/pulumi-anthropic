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

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Agent is a versioned Managed Agents configuration.
type Agent struct {
	rest.Resource[AgentArgs, AgentState, *AgentState]
}

// NewAgent returns the Agent resource controller.
func NewAgent() *Agent {
	return &Agent{rest.Resource[AgentArgs, AgentState, *AgentState]{
		Collection: agentCollection,
		EndOfLife:  rest.Archive,
		Tombstones: map[string]any{"metadata": nil},
	}}
}

func (a *Agent) Annotate(an infer.Annotator) {
	an.Describe(a, "A Claude Managed Agents agent: the model, system prompt, tools, MCP servers, "+
		"and skills that sessions run with.\n\n"+
		"Agents are versioned. Every update that changes the configuration creates a new version, "+
		"reported by the `version` output; sessions and deployments can pin a version, which makes "+
		"canary and staged rollouts a matter of which version each deployment references.\n\n"+
		"The API cannot delete agents, so deleting this resource archives the agent, which makes it "+
		"read-only. Running sessions continue; new sessions cannot use it.")
}

type AgentArgs struct {
	Name        string            `pulumi:"name"`
	Model       Model             `pulumi:"model"`
	System      *string           `pulumi:"system,optional"`
	Description *string           `pulumi:"description,optional"`
	Tools       []Tool            `pulumi:"tools,optional"`
	MCPServers  []MCPServer       `pulumi:"mcpServers,optional"`
	Skills      []SkillRef        `pulumi:"skills,optional"`
	Multiagent  *Multiagent       `pulumi:"multiagent,optional"`
	Metadata    map[string]string `pulumi:"metadata,optional"`
}

func (a *AgentArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Name, "The agent's name, 1 to 256 characters.")
	an.Describe(&a.Model, "The model the agent runs on.")
	an.Describe(&a.System, "The system prompt, up to 100,000 characters.")
	an.Describe(&a.Description, "A description of the agent, up to 2,048 characters.")
	an.Describe(&a.Tools, "The tools the agent can use, up to 128.")
	an.Describe(&a.MCPServers, "The MCP servers the agent can connect to, up to 20. "+
		"Each must be referenced by an `mcp_toolset` tool.")
	an.Describe(&a.Skills, "The skills available to the agent.")
	an.Describe(&a.Multiagent, "Makes the agent a coordinator that delegates to other agents.")
	an.Describe(&a.Metadata, "Arbitrary key-value pairs, up to 16.")
}

type AgentState struct {
	AgentArgs
	Version   int    `pulumi:"version"`
	CreatedAt string `pulumi:"createdAt"`
	UpdatedAt string `pulumi:"updatedAt"`
}

func (s *AgentState) Inputs() *AgentArgs { return &s.AgentArgs }

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

// Check validates the references between tools and MCP servers, which the API would
// otherwise reject only at deployment time.
func (a *Agent) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[AgentArgs], error) {
	args, failures, err := infer.DefaultCheck[AgentArgs](ctx, req.NewInputs)
	if err != nil || len(failures) > 0 {
		return infer.CheckResponse[AgentArgs]{Inputs: args, Failures: failures}, err
	}

	// Names that are unknown during preview decode as empty and are skipped.
	servers := map[string]bool{}
	for _, s := range args.MCPServers {
		if s.Name != "" {
			servers[s.Name] = false
		}
	}
	for i, t := range args.Tools {
		if t.Type != MCPToolset || t.MCPServerName == nil || *t.MCPServerName == "" {
			continue
		}
		if _, ok := servers[*t.MCPServerName]; !ok {
			failures = append(failures, p.CheckFailure{
				Property: fmt.Sprintf("tools[%d].mcpServerName", i),
				Reason:   fmt.Sprintf("no MCP server named %q is declared in mcpServers", *t.MCPServerName),
			})
		}
		servers[*t.MCPServerName] = true
	}
	for i, s := range args.MCPServers {
		if referenced, ok := servers[s.Name]; ok && !referenced {
			failures = append(failures, p.CheckFailure{
				Property: fmt.Sprintf("mcpServers[%d]", i),
				Reason:   fmt.Sprintf("MCP server %q must be referenced by an mcp_toolset tool", s.Name),
			})
		}
	}
	return infer.CheckResponse[AgentArgs]{Inputs: args, Failures: failures}, nil
}

// Update creates a new agent version. The version Pulumi last saw is sent as an
// optimistic lock, so that a change made outside of Pulumi is surfaced rather than
// silently overwritten.
func (a *Agent) Update(
	ctx context.Context, req infer.UpdateRequest[AgentArgs, AgentState],
) (infer.UpdateResponse[AgentState], error) {
	resp, err := a.UpdateWith(ctx, req, map[string]any{"version": req.State.Version})
	if rest.IsConflict(err) {
		return resp, fmt.Errorf("agent %s was changed outside of Pulumi since version %d; "+
			"run `pulumi refresh` to reconcile, then update again: %w", req.ID, req.State.Version, err)
	}
	return resp, err
}
