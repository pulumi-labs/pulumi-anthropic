# GitHub Actions OIDC authentication

This example uses GitHub's ID-token endpoint with `federation.identityToken.command`.
It creates a small Anthropic vault without storing an Anthropic API key or OAuth token
in GitHub. It does not run an agent session or perform inference.

The provider invokes [`get-github-oidc-token.sh`](get-github-oidc-token.sh) whenever it
needs an assertion for an access-token exchange. The script requests a fresh JWT from
GitHub with audience `https://api.anthropic.com` and writes only that JWT to stdout.
The provider captures it; the official Anthropic SDK exchanges it, caches the resulting
access token, and requests another assertion when renewal is needed.

Fetching on demand matters: GitHub JWTs contain a `jti` claim. With Anthropic issuer
replay protection enabled, an assertion can be exchanged only once. Sharing a token
file across preview, update, refresh, and independent clients can reuse that assertion.
This example does not disable replay protection and needs no background token-file
refresher.

## Prerequisites

Provision the trust configuration separately; this program consumes it rather than
creating it. The existing [workload-identity example](../workload-identity) demonstrates
provisioning an issuer, service account, workspace membership, and rule.

The federation configuration must include:

- Issuer URL: `https://token.actions.githubusercontent.com`, using JWKS discovery.
- A service account with `workspace_developer` access to the target workspace.
- A rule targeting that account and workspace with OAuth scope `workspace:developer`.
- Audience: `https://api.anthropic.com`.
- Exact subject: `repo:OWNER/REPOSITORY:ref:refs/heads/main`, using your actual repository
  identity. Run the workflow on `main`; another branch produces a different subject.

Do not broaden this rule to arbitrary pull requests or forks just to make an exchange
succeed. GitHub deployment environments also change the subject format and require a
corresponding rule.

See [Anthropic's GitHub Actions WIF guide](https://platform.claude.com/docs/en/manage-claude/wif-providers/github-actions).

## Run in this repository

1. Create an empty Pulumi Cloud stack for this example. This does not contact Anthropic:

   ```sh
   cd examples/github-actions-oidc
   pulumi stack init YOUR_ORG/github-actions-oidc/ci
   ```

2. Set these GitHub repository variables to your own values:

   | Variable | Value |
   |---|---|
   | `ANTHROPIC_OIDC_EXAMPLE_STACK` | Fully qualified Pulumi stack name, e.g. `YOUR_ORG/github-actions-oidc/ci` |
   | `ANTHROPIC_ORGANIZATION_ID` | Anthropic organization UUID |
   | `ANTHROPIC_WORKSPACE_ID` | Target workspace ID (`wrkspc_...`) |
   | `ANTHROPIC_FEDERATION_RULE_ID_MAIN` | Main-branch federation rule ID (`fdrl_...`) |
   | `ANTHROPIC_FEDERATION_SERVICE_ACCOUNT_ID` | Target service account ID (`svac_...`) |

3. Set the GitHub secret `PULUMI_ACCESS_TOKEN` for access to that Pulumi stack. This is
   separate from Anthropic authentication. No Anthropic credential secret is needed.

4. Copy [`workflow.yml`](workflow.yml) to `.github/workflows/anthropic-oidc-example.yml`
   at the repository root. The file here is a template, not an enabled workflow.

5. Merge the example and workflow onto the branch trusted by your rule. Manually run
   **Anthropic GitHub OIDC example** on `main` with `command: up`.

The workflow builds and installs the repository's `0.1.0-dev` provider. It uses the
Pulumi CLI, Bash, curl, and jq available on its Ubuntu runner. The runner supplies
`ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN` because the job has
`id-token: write`. Do not copy these runner credentials into stack configuration or
invoke the token script as a standalone workflow step that prints its output.

## Cleanup

Run the same workflow on `main` with `command: destroy` to permanently delete the vault.
This does not delete the existing federation issuer, rules, service account, or workspace.
The Pulumi stack remains available for subsequent runs.
