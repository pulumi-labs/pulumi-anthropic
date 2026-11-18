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

The Anthropic API accepts three kinds of credential. Each resource needs a particular kind of credential.
Consult the table below to determine which credentials your program needs. Configure these credentials as
environment variables or as Pulumi configuration.

| Configuration | Environment variable | Needed for |
|---|---|---|
| `anthropic:apiKey` | `ANTHROPIC_API_KEY` | Workspace resources: agents, environments, vaults, memory stores, deployments, tunnels, user profiles, and skills. Create a key in the [Claude Console](https://platform.claude.com/settings/keys). |
| `anthropic:adminKey` | `ANTHROPIC_ADMIN_KEY` | Organization resources: workspaces, members, invites, users, API keys, spend limits, compliance settings, RBAC groups, and external keys. Organization admins can create admin keys (`sk-ant-admin...`) in the Console. |
| `anthropic:authToken` | `ANTHROPIC_AUTH_TOKEN` | Service accounts and workload identity federation. These tokens are created [using the `ant` CLI or via workload identity federation](https://platform.claude.com/docs/en/manage-claude/wif-admin-api#prerequisites). |


```bash
pulumi config set --secret anthropic:apiKey sk-ant-...
pulumi config set --secret anthropic:adminKey sk-ant-admin...
```

## Configuration options

| Option | Description |
|---|---|
| `apiKey` | An Anthropic API key. Secret. Falls back to `ANTHROPIC_API_KEY` and takes precedence over OAuth for workspace-scoped operations. |
| `adminKey` | An Anthropic Admin API key. Secret. Falls back to `ANTHROPIC_ADMIN_KEY` and takes precedence over OAuth for organization operations. |
| `authToken` | A static OAuth access token, sent as a bearer token. Secret. Falls back to `ANTHROPIC_AUTH_TOKEN`. OAuth-only identity operations require the `org:admin` scope. The token is not refreshed automatically and cannot be used with `federation`. |
| `federation` | Automatically renewed OAuth credentials obtained from an explicitly configured OIDC identity token source. Requires `organizationId`, `ruleId` and `identityToken` within this object, plus top-level `workspaceId`. In `identityToken`, specify exactly one of `token` (literal JWT), `file` (absolute path, reread for each exchange) or `command` (executable and arguments, rerun for each exchange). Cannot be used with `authToken`. The federation rule determines the available permissions. |
| `workspaceId` | The workspace used for workspace-scoped operations. Required with `federation`, where it selects the workspace during token exchange. Otherwise, it is sent in the `anthropic-workspace-id` header and is needed when a credential can act on multiple workspaces. To manage several workspaces, use one explicit provider per workspace. |
| `baseUrl` | The Anthropic API base URL. Falls back to `ANTHROPIC_BASE_URL`, then defaults to `https://api.anthropic.com`. |

### Federation identity token sources

Set exactly one property in `federation.identityToken`:

- `token`: a literal OIDC JWT. It is reused and cannot be renewed by the provider.
- `file`: an absolute path to an OIDC JWT file, reread for each exchange.
- `command`: an array containing an executable and its arguments, rerun for each exchange.

For example, an explicit provider in Pulumi YAML can obtain fresh assertions from a command:

```yaml
resources:
  anthropic:
    type: pulumi:providers:anthropic
    properties:
      workspaceId: wrkspc_...
      federation:
        organizationId: your-organization-id
        ruleId: fdrl_...
        identityToken:
          command: ["/path/to/get-oidc-token", "--audience", "https://api.anthropic.com"]
```

The command must print one JWT to stdout, at most 64 KiB. Surrounding whitespace is
trimmed. It inherits the provider's environment and working directory, runs without an
implicit shell, and is stopped after 30 seconds or operation cancellation. Specify a
shell explicitly if shell syntax is needed. Command stdout and stderr are not logged.
Configuration is secret; file contents and command output are not saved in provider state.

Identity tokens with a `jti` claim, including GitHub Actions assertions, are single-use
when issuer replay protection is enabled. A command must obtain a fresh assertion on
each invocation. A shared file is not sufficient unless it is updated before every
exchange, including exchanges by other clients or restarted provider processes.
