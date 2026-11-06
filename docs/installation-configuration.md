---
title: Anthropic Installation & Configuration
meta_desc: Information on how to install and configure the Anthropic provider for Pulumi.
layout: package
---

## Installation

The Anthropic provider is available as a package in all Pulumi languages:

* JavaScript/TypeScript: [`@pulumi-labs/anthropic`](https://www.npmjs.com/package/@pulumi-labs/anthropic)
* Python: [`pulumi-labs-anthropic`](https://pypi.org/project/pulumi-labs-anthropic/)
* Go: [`github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic`](https://pkg.go.dev/github.com/pulumi-labs/pulumi-anthropic/sdk/go/anthropic)
* .NET: [`Pulumi.Labs.Anthropic`](https://www.nuget.org/packages/Pulumi.Labs.Anthropic)
* Java: [`com.pulumi.labs/anthropic`](https://central.sonatype.com/artifact/com.pulumi.labs/anthropic)

## Credentials

The Anthropic API accepts three kinds of credential, and each resource needs a particular
one. Configure the ones your program uses, either as Pulumi configuration or as
environment variables:

| Configuration | Environment variable | Needed for |
|---|---|---|
| `anthropic:apiKey` | `ANTHROPIC_API_KEY` | Workspace resources: agents, environments, vaults, memory stores, deployments, tunnels, user profiles, and skills. Create a key in the [Claude Console](https://platform.claude.com/settings/keys). |
| `anthropic:adminKey` | `ANTHROPIC_ADMIN_KEY` | Organization resources: workspaces, members, invites, users, API keys, spend limits, compliance settings, RBAC groups, and external keys. Admin keys (`sk-ant-admin...`) are created in the Console by organization admins. |
| `anthropic:authToken` | `ANTHROPIC_AUTH_TOKEN` | Service accounts and workload identity federation, which accept only an OAuth access token with the `org:admin` scope. An OAuth token can also stand in for either key. |

```bash
pulumi config set --secret anthropic:apiKey sk-ant-...
pulumi config set --secret anthropic:adminKey sk-ant-admin...
```

## Configuration options

| Option | Description |
|---|---|
| `apiKey` | An Anthropic API key. Secret. |
| `adminKey` | An Anthropic Admin API key. Secret. |
| `authToken` | An OAuth access token, sent as a bearer token. Secret. |
| `baseUrl` | The API base URL. Defaults to `https://api.anthropic.com`. |
| `workspaceId` | The workspace workspace-scoped resources are created in, for credentials that can act on more than one. To manage several workspaces, use one explicit provider per workspace. |
