# Live provider engine scenarios

`TestLive<Resource>` runs a Pulumi YAML lifecycle, edits, replacements and
ID-only get assertions through a locally built and installed provider. Independent
official-SDK clients read the real resulting state. Fast isolated mapping/lifecycle
tests live in `provider/`.

## Opt-in and authorization

**Do not run live tests without separately approved resource scope, spending limits,
disposable inputs and cleanup expectations.** A credential being available does not
enable a test. Without `-live`, every `TestLive*` skips before credential/config lookup,
plugin installation or prerequisite creation.

Compile without running anything:

```sh
cd test
go test -tags acceptance -run '^$' ./...
```

After approval, select one exact scenario, for example:

```sh
go test -tags acceptance -count=1 -timeout 30m -run '^TestLiveAgent$' . -live
# Or from the repository root:
make test_engine RESOURCE=Agent LIVE=1
```

Do not use broad selection with `-live` unless the entire selected scope is approved.
The three sample-program tests use the separate `-live-examples` opt-in.

CI uses GitHub OIDC workload identity federation with `workspace:developer` scope
for workspace engine scenarios and the AgentDelivery sample, selected by an explicit
allowlist in `make test_acceptance`. Both the provider and independent SDK clients run
the [CI-owned token command](../.github/scripts/get-anthropic-oidc-token.sh)
for every exchange. Assertions are never shared between clients or provider processes,
so issuer replay protection can remain enabled. There is no background token refresher.
Admin and OAuth-only identity scenarios and the OrgBootstrap/WorkloadIdentity samples
are manual-only. CI receives no stored Anthropic API key, Admin API key or OAuth token;
short-lived OAuth access tokens are obtained through federation. Fork pull requests do
not run live tests. Offline CI compiles every scenario and runs the helper tests without
live opt-in. It also previews both GitHub Actions WIF example programs using synthetic
configuration and a local backend. These preview-only checks require the project's
Pulumi CLI and YAML runtime, never run `up`, and reject any API request.

CI selects its organization, workspace, service account and federation rules using
GitHub repository variables:
`ANTHROPIC_ORGANIZATION_ID`, `ANTHROPIC_WORKSPACE_ID`,
`ANTHROPIC_FEDERATION_SERVICE_ACCOUNT_ID`, `ANTHROPIC_FEDERATION_RULE_ID_MAIN`
and `ANTHROPIC_FEDERATION_RULE_ID_PULL_REQUEST`. These identifiers do not authenticate
requests; GitHub OIDC supplies the credential.

## Blocked scenarios

Known blocked scenarios are retained but disabled even with `-live`. Each skips
before credentials, plugin installation or SDK setup, and reports its blocker:

| Scenario | Prerequisite for live coverage |
|---|---|
| Invite | A disposable recipient accepted by the invitation service. |
| RbacGroup | Verified Enterprise RBAC API access. |
| SpendLimit | An organization with Admin API spend-limit writes enabled. |
| ComplianceSettings | Directly writable compliance settings whose original values can be restored. |
| Tunnel, TunnelCertificate | Verified Tunnels API access, including parent tunnel creation. |

To investigate a selected case after addressing its blocker, explicitly add
`-include-blocked` as well as `-live`, for example:

```sh
go test -tags acceptance -count=1 -timeout 30m -run '^TestLiveInvite$' . \
  -live -include-blocked -credential-env ANTHROPIC_ADMIN_KEY
```

This flag does not fix prerequisites or turn failures into passes. Do not enable
it in CI or use broad selection unless every selected blocked scenario is approved.

## Credentials and inputs

Provision credentials externally. `-credential-env` selects exactly one:
- `ANTHROPIC_API_KEY` (default): provider API key and workspace SDK client.
- `ANTHROPIC_ADMIN_KEY`: provider/admin SDK key.
- `ANTHROPIC_ADMIN_OAUTH_KEY`: OAuth `authToken`, usable by applicable workspace,
  admin and OAuth SDK clients.
- `ANTHROPIC_IDENTITY_TOKEN_FILE`: rotating OIDC assertion file for workload identity
  federation. Use only when the assertion can be refreshed before each exchange.
- `ANTHROPIC_IDENTITY_TOKEN_COMMAND`: a JSON array containing an executable and its
  arguments. The command prints a fresh JWT to stdout on every invocation. CI selects
  this source explicitly; the harness does not enable it merely because it is present.

Both federation sources also require `-federation-rule-id`, `-organization-id` and
`-workspace-id`; pass `-federation-service-account-id` to check the service-account target.
For example, from the repository root, the GitHub Actions job sets:

```sh
export ANTHROPIC_IDENTITY_TOKEN_COMMAND
ANTHROPIC_IDENTITY_TOKEN_COMMAND=$(jq -cn \
  --arg script "$PWD/.github/scripts/get-anthropic-oidc-token.sh" \
  '["bash", $script]')
```

The command runs on demand with the GitHub runner's `ACTIONS_ID_TOKEN_REQUEST_URL`
and `ACTIONS_ID_TOKEN_REQUEST_TOKEN`. Neither assertion output nor runner credentials
are put in stack configuration. File-based federation remains available for other
platforms; a shared GitHub assertion file is not safe with single-use `jti` claims.

Cases spanning credential planes require suitable OAuth access. The helper reads
only the selected credential after opt-in. It writes provider credentials into encrypted
temporary stack config, not config-setting arguments, and clears ambient Anthropic
credential/base-URL discovery in plugin subprocesses. `-workspace-id` selects the
workspace explicitly; `-organization-id` supplies the organization for federation.

Scenarios create their disposable prerequisites through SDK calls and register
cleanup immediately. Setup passes returned IDs and versions directly to the copied
YAML configuration; users supply credentials, not prerequisite resources.

`-model-id` optionally selects the model used by Deployment setup, which checks
availability before allocating resources. Federation tests use locally generated
public keys and inline JWKS configuration.

Live coverage excludes existing-user/key adoption, user memberships and user-scoped
spending, cloud-KMS-backed resources, and hosted OIDC discovery. Provider contracts
for these surfaces remain covered by fast tests; live coverage needs SDK-owned setup.

Install the project's Pulumi CLI and YAML runtime before running the scenarios.
The helper builds and installs the provider into an isolated plugin home and uses
a private local backend. `-provider-version` selects the version assigned to that build.

## Identity, cleanup and privacy

Each scenario logs a unique run name. Engine checkpoint IDs, including replacement
history, drive resource SDK observations; prerequisite IDs come directly from SDK
setup responses.

SDK prerequisites register cleanup immediately. ProgramTest destroys managed resources;
resource-specific SDK readbacks verify deletion or retained archival state. Compliance
settings retain and restore the original singleton value. Archive is not hard deletion.

**A failure before an engine checkpoint can leave an object whose identity the test
does not know.** No helper guesses that ID or sweeps existing resources. Report the
unique run name and obtain scope for reconciliation/cleanup. Encrypted stack config
and checkpoints stay in private temporary directories and are removed after the run.
Build output, test results and Pulumi CLI diagnostics stream directly to the console.
Pulumi masks secret values in its output, and GitHub Actions masks registered secrets.
Federation assertions and access tokens must never be logged: dynamically obtained
credentials are not automatically registered with GitHub's masking. Keep checkpoints
and credential-bearing files out of CI artifacts. HTTP tracing is not enabled.
