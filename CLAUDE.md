# pulumi-anthropic

A native Pulumi provider for the Anthropic platform, built with
[pulumi-go-provider](https://github.com/pulumi/pulumi-go-provider)'s `infer` package.

## Layout

- `provider/<module>/` — one Go package per Pulumi module (`agents`, `skills`,
  `organization`, `identity`, `security`, `functions`). A resource's Go package is its
  Pulumi module, so `provider/agents.Agent` is `anthropic:agents:Agent`. Each package
  exports `Resources()` (or `Functions()`), which `provider/provider.go` concatenates.
- `provider/config/` — provider configuration and the credential planes.
- `provider/internal/rest/` — the Anthropic API's REST conventions, implemented once.
- `provider/internal/fakeapi/` — an in-memory fake of the API for tests.
- `provider/internal/providertest/` — runs the provider against the fake.
- `provider/schema.json` — golden schema; `go test ./provider -run TestSchema -update`.

## Conventions for resources

**Declare, don't implement.** A resource that follows the API's REST conventions (POST
create, GET read, POST partial update, DELETE or POST /archive) embeds one of three
lifecycles from `rest`, each configured as data:
- `rest.Resource[Args, State, *State]`: a top-level collection, with its
  `rest.Collection`, its `EndOfLife`, any `Tombstones`, and `DeleteBeforeReplace` when a
  replacement would collide with the original. See `provider/agents/agent.go`.
- `rest.Child`: a sub-collection of a parent object, such as a vault's credentials. It
  embeds a `rest.Resource` whose `Collection` is the parents' collection, and names the
  sub-collection and the inputs holding the parent's ID (and, for natural keys, the
  child's). Its resource ID is `rest.JoinID(parent, child)`. `Listed` reads a child
  from its parent's list, for sub-collections with no single-item GET.
- `rest.Adopted`: an object the API cannot create (users, API keys), adopted by an ID
  input; fields left undeclared are unmanaged rather than cleared.

Each module declares its collections, and any beta headers, once in `module.go`.
Override a method only where the API departs from the conventions, and build overrides
from the `rest` helpers (`Collection.Do/Get/Action`, `List`, `Encode`, `DecodeState`,
`Refresh`, `IsImport`, `UpdateBody`, `Diff`, `End`, `InitFailed`, `JoinID/SplitID`).

**One struct per shape.** Fields carry only `pulumi:"camelName[,optional]"` tags (plus
`provider:"secret"` / `provider:"replaceOnChanges"`). Never add `json` tags: the
pulumi-go-provider mapper reads them too. The wire name is always the snake_case form of
the Pulumi name (`rest/codec.go`), so choose Pulumi names that snake_case to the API's
field names. A field that is not on the wire is tagged `wire:"-"`; one the API accepts on
create but rejects in an update is tagged `wire:"createOnly"`. A struct whose API object
carries fields beyond its properties — a constant `type` discriminator, or an explicit
value for an unset field that the API would otherwise leave unchanged — implements
`rest.WireExtender`. An enum the API wraps as
`{"type": "x"}` implements `MarshalJSON`/`UnmarshalJSON` with `rest.MarshalTypeTag` /
`rest.UnmarshalTypeTag`. A discriminated union is one flat struct with a `type` enum and
optional per-variant fields, matching the API's own flat encoding.

**Optionality.** Optional fields are pointers, slices, or maps, so unset is distinct
from zero. `State` embeds `Args`, adds output-only fields, and implements
`Inputs() *Args`.

**Semantics decided once:**
- Delete does the strongest end-of-life the API offers: delete, else archive, else
  forget (with a warning). Say which in the resource description. `EndOfLife` has no
  default: a resource that declares none fails to delete rather than hard-deleting.
- Refresh projects the server object onto what the program declared (`rest.Project`):
  server defaults never produce diffs; genuine drift does. Archived objects read as gone.
  Only an import (`rest.IsImport`: no prior state) adopts the server's values wholesale.
- Updates send only changed top-level fields; a changed object is sent whole, with null
  for every key removed at any depth, so removal works whether the API merges or
  replaces it.
- Diffs honor `replaceOnChanges` at any depth (`rest.Diff`), including on an object
  being added or removed.
- A Create that fails after the API created the object returns `rest.InitFailed`, so
  the object is tracked and the next update finishes it, rather than orphaned.
- Output-only fields tagged `provider:"secret"` are marked secret at runtime by the
  lifecycles' `WireDependencies`; the tag alone reaches only the schema.
- Secret inputs are write-only in the API; refresh keeps their declared values.
- Fields that cannot change in place are `provider:"replaceOnChanges"`.
- Maps the API merges key by key (e.g. `metadata`) are listed in `Tombstones` with the
  value that removes a key.
- Resources the API cannot create (users, API keys) adopt an existing object by ID.
- A resource identified by a parent/child pair uses `rest.JoinID` as its ID.

**Credentials.** Pick the plane the endpoint accepts: `config.Workspace` (API key),
`config.Admin` (Admin key), or `config.OrgAdminOAuth` (OAuth token only). Never read
keys directly.

**Documentation.** Every resource, type, and field has a description via `Annotate`,
written for the registry: what it is, constraints, and non-obvious lifecycle behavior.

## Testing

Every resource has tests in its package (`<resource>_test.go`, package `<module>_test`)
that run against `fakeapi` via `providertest.New`, using `providertest.URN`, `Create`,
`Read`, and `Diff` to drive the provider as the engine does:
- an `integration.LifeCycleTest` covering create, an update, and delete;
- assertions on the exact request bodies and paths the provider sent;
- refresh behavior (no drift from server defaults, drift from out-of-band changes);
- replacement for immutable fields, and each override's special behavior.

Server behavior the fake doesn't model generically (defaults, versioning, custom
endpoints) is set up in the test with `SetHooks` and `Handle`. The fake is a stand-in:
anything only the real API can confirm is covered by the acceptance tests in `examples/`.

## Commands

- `make build` — build the plugin into `bin/`.
- `make test` — unit tests; `make schema` — regenerate the golden schema.
- `make sdks` — generate all language SDKs from the schema.
- `make test_acceptance` — run the examples against the live API (needs credentials).
