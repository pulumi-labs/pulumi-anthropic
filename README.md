# Anthropic Provider for Pulumi

Manage the Anthropic platform as code: Claude Managed Agents and the environments, vaults,
skills, and schedules they run with, plus the organization around them — workspaces,
members, service accounts, workload identity federation, encryption keys, and spend
limits.

```typescript
import * as pulumi from "@pulumi/pulumi";
import * as anthropic from "@pulumi-labs/anthropic";

const triage = new anthropic.skills.Skill("triage", {
    source: new pulumi.asset.FileArchive("./skills/triage"),
});

const agent = new anthropic.agents.Agent("triage", {
    name: "issue-triage",
    model: { id: "claude-opus-5-5" },
    system: "You triage GitHub issues.",
    tools: [{ type: "agent_toolset_20260401" }],
    skills: [{ type: "custom", skillId: triage.id, version: triage.latestVersionId }],
});
```

Agents are versioned: each `pulumi up` that changes an agent's configuration creates a new
version, reported by its `version` output. A `Deployment` runs an agent on a schedule, and
can follow the latest version or stay pinned to one. That makes canaries and staged
rollouts a matter of which version each deployment references; see
[`examples/agent-delivery`](examples/agent-delivery).

## Installing

The provider is available in every Pulumi language:

| Language | Package |
|---|---|
| TypeScript/JavaScript | `npm install @pulumi-labs/anthropic` |
| Python | `pip install pulumi-labs-anthropic` |
| Go | `go get github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic` |
| .NET | `dotnet add package Pulumi.Labs.Anthropic` |
| Java | `com.pulumi.labs:anthropic` |

YAML programs can use it directly.

## Configuring

The Anthropic API has three kinds of credential, and each resource needs a particular one:

| Setting | Environment variable | Used by |
|---|---|---|
| `anthropic:apiKey` | `ANTHROPIC_API_KEY` | Agents, environments, vaults, memory stores, deployments, tunnels, user profiles, skills |
| `anthropic:adminKey` | `ANTHROPIC_ADMIN_KEY` | Workspaces, members, invites, users, API keys, spend limits, compliance, RBAC groups, external keys |
| `anthropic:authToken` | `ANTHROPIC_AUTH_TOKEN` | Service accounts and workload identity federation, which require an OAuth token with the `org:admin` scope. It can also stand in for either key. |

`anthropic:workspaceId` selects the workspace for credentials that can act on more than
one. To manage several workspaces, use an explicit provider for each.

## Resources

| Module | Resources |
|---|---|
| `agents` | `Agent`, `Environment`, `Vault`, `VaultCredential`, `MemoryStore`, `Deployment`, `Tunnel`, `TunnelCertificate`, `UserProfile` |
| `skills` | `Skill` |
| `organization` | `Workspace`, `WorkspaceMember`, `Invite`, `User`, `ApiKey`, `SpendLimit`, `ComplianceSettings` |
| `identity` | `ServiceAccount`, `ServiceAccountWorkspace`, `FederationIssuer`, `FederationRule`, `FederationRuleWorkspace`, `RbacGroup`, `RbacGroupMember` |
| `security` | `ExternalKey` |

Functions: `getOrganization`, `getModels`, `getRateLimits`.

Sessions, events, memories, files, batches, and usage reports are runtime data rather
than configuration, so they are not resources.

Some lifecycle behavior follows from what the API allows:

- **Deleting** does the strongest thing the API supports. Agents, deployments,
  workspaces, service accounts, federation issuers and rules, tunnels, and tunnel
  certificates cannot be deleted, only archived, so deleting them archives them. Use
  `retainOnDelete` to leave an object in place.
- **Users and API keys** cannot be created through the API. The `User` and `ApiKey`
  resources adopt an existing object by ID and manage its role, or its name and status.
- **Secrets** such as vault credential tokens are write-only in the API, so changes made
  to them outside of Pulumi cannot be detected.

## Developing

See [CLAUDE.md](CLAUDE.md) for the architecture and conventions. `make build test` builds
the provider and runs its tests against an in-memory fake of the API; `make
test_acceptance` runs the non-admin live acceptance tests in [`test/`](test/), given
an `ANTHROPIC_API_KEY`.
Runnable sample programs live in `examples/`. Explicitly opted-in live YAML engine
scenarios live in `test/`; see its README for scope, credentials and setup inputs.
For keyless GitHub Actions authentication, see [`examples/github-actions-wif`](examples/github-actions-wif).
