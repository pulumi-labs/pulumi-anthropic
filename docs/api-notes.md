# Anthropic API behavior observed against the live API

The provider's tests run against an in-memory fake, which models only what is written
down here and in the API reference. These are behaviors confirmed against the live API
that the code depends on, with the date they were observed. Re-verify them with
`make test_acceptance` when the API changes.

## Vault credentials (2026-09-26)

`auth.mcp_server_url` is stored in a canonical form, which is what reads return:

| Sent | Stored |
|---|---|
| `https://api.example.com/mcp/` | `https://api.example.com/mcp` |
| `https://api.example.com/a//` | `https://api.example.com/a` |
| `https://API.Example.com/B` | `https://api.example.com/B` |
| `https://api.example.com` | `https://api.example.com/` |
| `https://api.example.com:443/c` | `https://api.example.com/c` |
| `https://api.example.com/d?x=1` | unchanged |
| `https://api.example.com/e/../f` | unchanged |

The canonical form is also the uniqueness key within a vault: `https://api.example.com`
conflicts with an existing `https://api.example.com/`. The provider declares the URL in
this form (`agents/mcp_url.go`), because the URL cannot change in place and reporting the
normalization as drift would replace the credential.

Secret values (`token`, and the other write-only fields) are never returned.

## Workload identity federation (2026-09-26)

- An organization can have only one active issuer per `issuer_url`. Creating a second
  fails with `create oidc issuer: oidc federation issuer already exists`.
- An issuer cannot be archived while a live rule references it:
  `cannot archive issuer: 1 live federation rules reference it`.
- Archiving a rule clears its workspace bindings (`workspace_id` becomes null,
  `workspace_ids` empty).
- Once, creating a rule 0.7s after its issuer failed with `issuer fdis_... not found in
  organization` (request `req_011CfRAcL2y5HY9MbHMGkFdU`). It did not reproduce in eight
  further attempts, including with no delay, so it is treated as a transient API fault
  rather than retried by the provider.

## Spend limits (2026-09-26)

Writing spend limits is gated per organization. Without access, `POST
/v1/organizations/spend_limits` fails with 403 and error code
`spend_limits_admin_api_writes_not_enabled`. The `org-bootstrap` example creates a limit
only when `monthlyLimitCents` is set.

## Not yet verified against the live API

- Whether workspace `tags` are merged or replaced on update.
- The default spend-limit `period`, and user-scoped spend limits.
- Whether an environment's `config.type` can change in place (modeled as a replacement).
- Whether every nested optional field can be cleared by sending null.
- RBAC groups and members, which require Claude Enterprise.
- How a federation rule's workspace binding reads once its rule is archived.
