# pulumi-anthropic

A native Anthropic provider built with `pulumi-go-provider/infer` and the official
`github.com/anthropics/anthropic-sdk-go/v2` SDK.

## Core rules

- Use provider-configured official SDK clients and explicit Pulumi↔SDK conversions.
  Do not add generic CRUD, reflective wire, recursive patch, parallel-client, HTTP
  interception, recording/replay or coordinator frameworks.
- Preserve unrelated work. Never commit, reset, stash, clean or push unless asked.
  Keep credentials, authorization data, private logs/checkpoints, recordings and agent
  sessions uncommitted. Production tests and tooling must not depend on private artifacts.
- Prefer typed infer interfaces and default Diff after normalization. Use raw properties
  only for demonstrated framework limitations involving knownness, metadata or path changes.

## Layout

- `provider/<module>/`: resources and functions with tokens
  `anthropic:<module>:<Resource>`.
- Keep each resource implementation in one `<resource>.go`: public types and annotations,
  lifecycle methods, infer adapters, then conversions/helpers. Constructors return the
  complete registered resource; module registries call them directly.
- Keep one `<resource>_test.go` per resource. Shared module helpers/metadata tests and
  provider-wide config, schema and function tests may remain separate. Do not add
  production exports solely for test package access; use test-only bridges when needed.
- Registered wrappers are stateless. Each lifecycle invocation uses a fresh typed
  controller and invocation-local raw-input/observation state. Configured clients may
  live for the provider process.
- `provider/config/`: configuration, credentials and SDK clients.
- `provider/internal/fakeapi/` and `provider/internal/providertest/`: isolated test support,
  not evidence of platform behavior.
- `provider/schema.json`: public schema golden; regenerate only for deliberate changes.
- `examples/`: runnable samples. `test/`: opted-in installed-plugin YAML engine scenarios.
  Keep resource setup and assertions visible in their scenario files.

## Resource behavior

- Pulumi fields use `pulumi:"camelName[,optional]"` plus appropriate secret/replacement
  annotations. Do not add JSON tags to inferred structs.
- Define validation, normalization, equivalence, removal and replacement per property.
  Normalization must be idempotent, tolerate unknowns and preserve secrecy/dependency
  metadata on logical inputs, including moved paths. Use pointer-to-slice when omission
  and explicit emptiness differ.
- Custom-resource outputs do not inherit input dependency annotations. Preserve output
  secrecy independently. Ignore dependency annotations only in Diff comparison copies,
  never in checked or saved inputs.
- Distinguish omission, empty, zero and explicit null in SDK parameters. Follow the
  declared API contract—prefer the matching TypeScript SDK for response optionality,
  nullability and unions because Go decoder behavior is permissive.
- Create and Update return accurate known state on success. Failures return ordinary
  errors, not initialization checkpoints or reconstructed desired state. Retain only safe,
  resource-specific compensation for trustworthy allocations and report cleanup failure.
- Update uses engine-supplied state and never performs a pre-update refresh. Send the whole
  desired mutable managed state, excluding immutable/create-only/output-only values. Do not
  duplicate Diff or skip requests merely because old and new look equal. Compare old state
  only for documented removal encodings such as merged-map key deletion.
- Read queries the platform, recovers available import/get inputs and honestly preserves
  known write-only inputs. Only confirmed absence clears the ID. Never hide authorization,
  transient or decode errors, or managed zero-value drift.
- Verify runtime secrets on Create and ID-only Read; schema annotations alone may not be
  sufficient. Delete uses the resource's documented delete/archive/revoke/remove/forget
  operation. Replacement and collision behavior is resource-specific.
- Keep familiar names. Annotate every public resource, function, type and field with user
  documentation, including constraints and significant lifecycle behavior.

## Documentation

Write registry descriptions for users: purpose, constraints, defaults, clearing behavior,
replacement/deletion and important secret/import limitations. Do not describe SDK calls,
callbacks, serialization, null tombstones, deletion patches or review history. Explain
observable behavior instead of using ambiguous ownership jargon.

When descriptions change, refresh `provider/schema.json` and generated SDK documentation
without unrelated generated-code or manifest churn.

## Credentials and tests

- Resources never read credentials directly. Only API endpoint changes replace the provider;
  credentials, federation and workspace selection update without provider replacement.
  Do not make network calls during preview for unresolved configuration.
- Every resource needs fast lifecycle, mapping and metadata regressions plus an applicable
  YAML engine scenario. Independent official-SDK readbacks establish real server effects;
  fake tests cannot establish new platform truth. Preserve normative assertions and record
  blocked or unverified coverage rather than weakening tests.
- Saved state identifies its provider version. Resource List is not implemented. Listing
  functions must paginate and return usable identities. Avoid generic repeated-Delete,
  equal-input Update, concurrency or engine-owned dependency tests unless required by a
  resource contract.

### Live tests

- Live engine tests require explicit `-live`; examples require `-live-examples`. Credentials
  being present is never authorization. Live Anthropic/KMS calls, paid sessions and
  credential inspection require separately approved scope.
- CI uses GitHub OIDC federation with `workspace:developer` scope for an explicit workspace
  scenario allowlist. Never give CI admin-scoped or stored Anthropic credentials, or silently
  add admin-dependent scenarios. Admin and OAuth-only identity scenarios are manual-only.
  Federation identifiers are repository variables, not credentials. Normal build/test/CLI
  diagnostics belong in CI logs; never log credentials or bypass Pulumi's secret masking.
- Known blocked scenarios additionally require `-include-blocked` and otherwise skip before
  setup. Keep their blocker reasons and regressions; do not enable them in CI or treat access
  failures as passes.
- Scenarios create disposable SDK-provisionable prerequisites and register cleanup
  immediately. Credentials are the bootstrap exception. Do not require preexisting IDs,
  KMS coordinates, hosted issuers, mailboxes or private fixture files. If setup cannot be
  provisioned safely, record the coverage gap. Never sweep unrelated resources.
- Live runs report cleanup/leaks. A failure before checkpointing may require separately
  approved reconciliation using the logged unique run name. Keep temporary checkpoints and
  credential-bearing files private and uncommitted. Do not log assertions, access tokens or
  authorization headers, including credentials obtained dynamically through federation.

## Commands

- `make build`, `make install`: build/install the provider plugin.
- `make test`: fast provider tests with the race detector.
- `make schema`: deliberately refresh the schema golden.
- `make sdks`: regenerate SDKs after a deliberate public-surface change.
- `make test_acceptance`: approved non-admin live allowlist; never run implicitly.
