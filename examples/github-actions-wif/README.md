# GitHub Actions workload identity federation

This example configures Anthropic to trust a GitHub Actions workload, then uses that
trust to manage a vault without storing an Anthropic API key or OAuth token in GitHub.
It does not run an agent session or perform inference.

The two directories are separate Pulumi projects within this example:

| Directory | Purpose | Anthropic authentication |
|---|---|---|
| [`configure/`](configure/Pulumi.yaml) | Register the GitHub issuer, create a developer service account, grant workspace access, and configure a federation rule. Run this from an administrator's terminal. | A separately obtained OAuth token with `org:admin`. |
| [`workload/`](workload/Pulumi.yaml) | Run a Pulumi program in GitHub Actions using the configured federation rule. Contains the [workflow template](workload/workflow.yml) and [token script](workload/get-github-oidc-token.sh). | Fresh GitHub OIDC assertions exchanged for short-lived Anthropic access tokens. |

Use separate stacks for configuration and the workload. The workflow runs only the
workload program; it never receives the administrator's credential.

## 1. Configure Anthropic

You need an existing Anthropic workspace and an OAuth token with the `org:admin`
scope; see [Anthropic's `ant` CLI admin-access instructions](https://platform.claude.com/docs/en/cli-sdks-libraries/cli/authentication#admin-access).
Admin API keys cannot manage federation. The organization ID must identify the
organization that your administrator credential acts in, and the workspace must belong
to that organization.

Run from this example directory:

```sh
cd configure
pulumi stack init YOUR_ORG/github-actions-wif-configure/setup
export ANTHROPIC_AUTH_TOKEN="$(ant auth print-credentials --profile admin --access-token)"
pulumi config set organizationId YOUR_ANTHROPIC_ORGANIZATION_ID
pulumi config set workspaceId YOUR_WORKSPACE_ID
```

The provider reads the OAuth token from `ANTHROPIC_AUTH_TOKEN`. Do not put it in a
workflow or stack configuration. Unset the variable when you finish administration.

### Choose the exact GitHub subject

Set `githubSubject` to the exact `sub` claim expected from the workload. For a repository
using [GitHub immutable subject claims](https://docs.github.com/en/actions/reference/security/oidc#immutable-subject-claims),
the main-branch subject includes the numeric owner and repository IDs:

```sh
pulumi config set githubSubject 'repo:OWNER@OWNER_ID/REPOSITORY@REPOSITORY_ID:ref:refs/heads/main'
```

Replace the placeholders with your repository's actual names and GitHub IDs. These IDs
identify the owner and repository, not a workflow run. Repositories using the name-only
subject format instead use `repo:OWNER/REPOSITORY:ref:refs/heads/main`. Check your
repository's OIDC configuration rather than assuming a format.

The workflow template is manually run on `main`. Another branch, a pull request, or a
GitHub deployment environment produces a different subject. Anthropic's `subjectPrefix`
matches exactly unless it ends in `*`; do not add a wildcard just to make an exchange
succeed. The audience is `https://api.anthropic.com`.

### Deploy the trust resources

The program uses OIDC signing-key discovery and keeps assertion replay protection
enabled. The rule grants `workspace:developer` only in the configured workspace, and
depends on the service account's workspace membership.

If your organization already has the GitHub issuer registered, import it into this
stack before running `pulumi up`:

```sh
pulumi config set issuerName YOUR_EXISTING_ISSUER_NAME
pulumi import anthropic:identity:FederationIssuer github YOUR_EXISTING_ISSUER_ID --generate-code=false --protect=false
```

Do not adopt an issuer already managed by another stack. This program manages its
discovery and replay-protection settings; review the preview before changing a shared
issuer. Without an import, it creates a new issuer. `namePrefix` defaults to
`github-actions-wif`; choose another lowercase slug if you need distinct service-account
and rule names.

```sh
pulumi up
pulumi stack output
```

The outputs provide the identifiers used by the workload. Creating these resources does
not itself verify a token exchange.

## 2. Run the workload in GitHub Actions

Create an empty Pulumi Cloud stack for the workload; this command does not contact Anthropic:

```sh
cd ../workload
pulumi stack init YOUR_ORG/github-actions-wif/ci
```

Set these GitHub repository variables. They are identifiers, not credentials; the
example-specific names keep them separate from other CI jobs' federation configuration.

| Variable | Value |
|---|---|
| `ANTHROPIC_WIF_EXAMPLE_STACK` | Fully qualified workload stack name, e.g. `YOUR_ORG/github-actions-wif/ci` |
| `ANTHROPIC_WIF_EXAMPLE_ORGANIZATION_ID` | Configuration stack's `organizationId` output |
| `ANTHROPIC_WIF_EXAMPLE_WORKSPACE_ID` | Configuration stack's `workspaceId` output |
| `ANTHROPIC_WIF_EXAMPLE_FEDERATION_RULE_ID` | Configuration stack's `federationRuleId` output |
| `ANTHROPIC_WIF_EXAMPLE_SERVICE_ACCOUNT_ID` | Configuration stack's `serviceAccountId` output |

Set the GitHub secret `PULUMI_ACCESS_TOKEN` for access to that Pulumi Cloud stack. This
is separate from Anthropic authentication; no Anthropic credential secret is needed.

Copy [`workload/workflow.yml`](workload/workflow.yml) to
`.github/workflows/anthropic-wif-example.yml` at the repository root. It is a template,
not an enabled workflow. Merge the example and workflow onto the trusted branch, then
manually run **Anthropic GitHub WIF example** on `main` with `command: up`.

The workflow builds and installs the repository's `0.1.0-dev` provider. Its Ubuntu runner
supplies Bash, curl, jq and the GitHub ID-token endpoint credentials because the job has
`id-token: write`.

### How the workload authenticates

The provider invokes `workload/get-github-oidc-token.sh` through
`federation.identityToken.command` whenever it needs an assertion. The script fetches a
fresh JWT with audience `https://api.anthropic.com` and writes only that JWT to stdout.
The provider captures it; the Anthropic SDK exchanges it, caches the resulting access
token, and requests another assertion when renewal is needed.

GitHub JWTs carry single-use `jti` claims. Fetching on demand avoids sharing an assertion
between clients or provider processes, without disabling replay protection or running a
background token refresher. Do not invoke the token script as a standalone workflow step
that prints its output, or copy runner credentials into stack configuration.

If an exchange fails, inspect Anthropic's [authentication history](https://platform.claude.com/settings/workload-identity-federation?tab=history).
A `match_subject_prefix` failure means the actual subject differs from `githubSubject`.

## Validation

CI runs `pulumi preview` for both programs using synthetic configuration, an isolated
local backend and the locally built provider. These checks never run `pulumi up` or
contact Anthropic; unexpected API requests fail the test. They validate the programs,
not the live federation exchange. CI also runs `actionlint` on the workflow template.

## Cleanup

First run the workload workflow on the trusted branch with `command: destroy` to delete
the vault. Then, if the trust configuration is no longer needed, run `pulumi destroy`
from `configure/`, using a current administrator OAuth token.

Destroying the configuration stack archives the rule and service account and removes
the explicit workspace grant. The workspace is not deleted. The issuer is retained
because it can be shared by other workloads; archive it separately only when no active
rules use it. Already-issued access tokens remain valid until they expire. Both Pulumi
stacks remain available for subsequent runs.

See [Anthropic's GitHub Actions WIF guide](https://platform.claude.com/docs/en/manage-claude/wif-providers/github-actions)
for the platform authentication model.
