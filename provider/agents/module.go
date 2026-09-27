// Copyright 2026, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Package agents is the anthropic:agents module: Claude Managed Agents and the
// configuration their sessions run with.
package agents

import (
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/pulumi/pulumi-anthropic/provider/config"
	"github.com/pulumi/pulumi-anthropic/provider/internal/rest"
)

// Resources returns the module's resources.
func Resources() []infer.InferredResource {
	return []infer.InferredResource{
		infer.Resource(NewAgent()),
		infer.Resource(NewDeployment()),
		infer.Resource(NewEnvironment()),
		infer.Resource(NewMemoryStore()),
		infer.Resource(NewTunnel()),
		infer.Resource(NewTunnelCertificate()),
		infer.Resource(NewUserProfile()),
		infer.Resource(NewVault()),
		infer.Resource(NewVaultCredential()),
	}
}

// The anthropic-beta headers the module's APIs require.
const (
	managedAgentsBeta = "managed-agents-2026-04-01"
	agentMemoryBeta   = "agent-memory-2026-07-22"
	mcpTunnelsBeta    = "mcp-tunnels-2026-06-22"
	userProfilesBeta  = "user-profiles-2026-08-18"
)

// The module's collections, all of which accept a workspace's API key.
var (
	agentCollection       = rest.Collection{Plane: config.Workspace, Path: "v1/agents", Beta: managedAgentsBeta}
	deploymentCollection  = rest.Collection{Plane: config.Workspace, Path: "v1/deployments", Beta: managedAgentsBeta}
	environmentCollection = rest.Collection{Plane: config.Workspace, Path: "v1/environments", Beta: managedAgentsBeta}
	memoryStoreCollection = rest.Collection{Plane: config.Workspace, Path: "v1/memory_stores", Beta: agentMemoryBeta}
	tunnelCollection      = rest.Collection{Plane: config.Workspace, Path: "v1/tunnels", Beta: mcpTunnelsBeta}
	userProfileCollection = rest.Collection{Plane: config.Workspace, Path: "v1/user_profiles", Beta: userProfilesBeta}
	vaultCollection       = rest.Collection{Plane: config.Workspace, Path: "v1/vaults", Beta: managedAgentsBeta}
)
