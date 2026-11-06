# Live trials

Some questions about the Anthropic API cannot be answered by any test in this
repository. This package answers them against the real API, and records each answer.

## The question this package exists for

The provider sends an explicit null for a field a program stops declaring, at every
depth. `rest.UpdateBody` documents that this makes removal work "whether the API merges
or replaces" the object. That is a claim about Anthropic, and nothing checked it.

Two mechanisms hide the answer:

- The fake in `internal/fakeapi` behaves the way each test's `SetHooks` says it does. It
  agrees with whatever the author assumed.
- A refresh never compares a field the program dropped. `rest.Project` keeps an unset
  field unset, so that a server default does not read as drift. The cost is that a value
  the API failed to clear is invisible.

So a removal that silently fails looks identical to one that worked. `pulumi up` reports
success, `pulumi refresh` reports no drift, and the old value stays on the server.

## The three parts

```
     provider/schema.json                 provider/livebehavior.json
     every optional field                 one verdict for each field
     the provider accepts                 cleared | kept | pending | unverifiable
             │                                        │       ▲
             │                                        │       │ writes
             └──────────────┐          ┌──────────────┘       │
                            ▼          ▼                      │
                   ┌──────────────────────────┐    ┌──────────┴───────────┐
                   │  provider/               │    │  make trial          │
                   │  behavior_test.go        │    │  (this package)      │
                   │                          │    │                      │
                   │  Does every field have   │    │  create, set a field │
                   │  a verdict?              │    │  remove it, read it  │
                   │                          │    │  back as an import   │
                   │  Runs in CI.             │    │                      │
                   │  Calls no API.           │    │  Needs a key.        │
                   └──────────────────────────┘    │  Creates real        │
                            │                      │  objects.            │
                      pass / fail                  └──────────────────────┘
```

`behavior_test.go` compares two lists of field names. It never contacts Anthropic, so it
runs on every commit at no cost. `make trial` is the part that costs something, so a
person runs it deliberately.

## Why the trial reads through an import

A normal read projects the server's object onto what the program declared, which drops
exactly the field under test. An import has no prior state, so `rest.Refresh` adopts the
server's values wholesale. That is the only view which shows a field the program stopped
declaring.

`onServer` does this with a `Read` that carries no properties and no inputs, which makes
`rest.IsImport` true.

## What a verdict means

| Verdict | Meaning | What to do |
|---|---|---|
| `cleared` | The API forgot the field, or reset it to its default. | Nothing. |
| `kept` | The API kept the old value. A program cannot un-set the field. | Decide. See below. |
| `pending` | Nobody has asked. | Write a case here, then run `make trial`. |
| `unverifiable` | The API cannot answer, for a stated reason. | Record the reason. |

A `kept` verdict is a user-visible defect, and the guard requires a note explaining what
a user sees. The provider cannot repair it, so there are three lesser moves:

- Warn from the resource's `Update`, and name the resource option that does work.
  `Agent.Update` does this for `model.effort`.
- Report the defect to Anthropic. This is the only route to a real fix.
- State the limit in the resource's description, which reaches the Registry page.

## Add a case

Append to the table in `TestAgentOptionalFieldsClear`, or write the same shape for
another resource:

```go
{
    field:     []string{"model", "effort"}, // where to look on the server
    record:    "model.effort",              // the key in livebehavior.json
    defaulted: "medium",                    // the value an undeclared field gets
    withField: func(n string) property.Map { ... },  // inputs that set it
    bare:      func(n string) property.Map { ... },  // inputs that omit it
}
```

The two input builders cannot be generated from the schema. A valid `Deployment` needs a
real agent and environment to point at, and a valid `VaultCredential` needs a parent
vault, so each resource needs its own.

`defaulted` matters where the API fills a field it was not given. Without it, a reset to
the default looks the same as a leftover.

## Run it

```sh
make trial
```

The target reads a key from `~/.config/anthropic/trial-key`. Override the path with
`TRIAL_KEY_FILE`.

Caution: each case creates a real object in the workspace the key belongs to, and deletes
it again. A failed delete logs `LEAKED` with the object's ID. Before the first run,
record what the workspace already holds, so that a later cleanup can tell a leftover from
an object that was always there. `guinsnotes/inventory.py` does this, and
`guinsnotes/agent-surface-baseline.json` holds the result from 2026-11-06.

The run writes its verdicts into `provider/livebehavior.json`. Review that diff: it is
the artifact, not the test output.

## State on 2026-11-06

Five fields of `agents:Agent` carry a verdict. Seventy-three are pending.
`model.effort` is `kept`.

`mcp-tunnels-2026-06-22` is not granted to the organization behind the key used, so
`agents:Tunnel` and `agents:TunnelCertificate` cannot be reached. A request to
`/v1/tunnels` returns 401 rather than the 400 the beta-headers documentation describes.
