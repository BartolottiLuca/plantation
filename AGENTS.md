# Agent instructions

Conventions for anyone — human or agent — implementing a card from [tasks/](tasks/).
The behavioural contract is [SPEC.md](SPEC.md); the order of work is [PLAN.md](PLAN.md).

## Before you start

1. Read `SPEC.md` in full. It is long on purpose: it is the only shared context between
   cards, so everything you need to avoid guessing is in there.
2. Read your card. Do what it says and stop there. Another card owns the next thing.
3. If the card and the spec disagree, the spec wins — say so and fix the card.
4. If the spec is wrong or silent on something load-bearing, amend `SPEC.md` in the same
   change. Do not encode a private assumption in code.

## Scope discipline

Touch only the files your card names. Cards are run in parallel waves; two agents editing
`main.go` at once is how a wave gets thrown away. If you need a change in a package you
do not own, state it in your summary rather than making it.

Do not add dependencies beyond those the card names. The standard library plus `pgx`,
`uuid`, `htmx` and the official `openai-go` SDK (species drafting only) covers this
whole project. No web framework, no ORM, no
logging library, no assertion library.

## Go conventions

- Go 1.23 or newer. `gofmt`, `go vet` and `golangci-lint run` clean before you are done.
- The import rules in `SPEC.md` §2 are hard: **`internal/care` imports only the
  standard library and `internal/domain`**, providers do not import each other, only
  `cmd/plantation` builds concrete implementations.
- `context.Context` is the first parameter of anything doing I/O, and every outbound HTTP
  call sets an explicit timeout.
- Wrap errors with context: `fmt.Errorf("fetching rooms: %w", err)`. Expected degraded
  states (stale weather, unlinked Tado) are typed data on the result, not errors.
- `log/slog` with the JSON handler, `snake_case` keys. **Never log** the Discord webhook
  URL, Tado tokens, the OpenAI API key, or coordinates.
- Anything that reads the clock takes a `Clock`. No `time.Now()` outside `cmd`.

## Comments

Before writing a comment, delete it and ask whether a reader with the surrounding code
could reconstruct the same fact from the code, a type, a name, or a linked doc. If they
could, leave it deleted. What survives that test is worth keeping: a non-obvious
constraint, an operational fact learned the hard way, a why-not-what tradeoff. One line
when it survives.

```go
// ❌ restates the code
// clamp f_dry between 0.5 and 2.0
fDry = clamp(fDry, 0.5, 2.0)

// ✅ a fact the code cannot carry
// Tado's refresh token rotates on use: persist the new one before spending the access
// token, or a crash here costs a manual re-link.
```

## Tests

- Table-driven, in the same package, named for the behaviour they pin down.
- `go test ./... -race` is the gate.
- HTTP clients are tested against `httptest` with recorded fixtures in `testdata/`. Record
  a real response once, strip anything identifying, commit it.
- Integration tests needing Postgres read `PLANTATION_TEST_DSN` and `t.Skip` when unset.
- Do not test getters. Do test every branch of the care engine — that is where wrong
  answers are plausible enough to survive review.

## Adding a species

Species are rows in the `species` table; there is no file to edit, nothing is loaded at
boot and nothing is seeded — a fresh database starts with an empty catalog.

1. In the app: add a plant, choose "not in the list?", and describe it. Where an OpenAI
   API key is configured, the app drafts the record and saves it straight away if it
   validates; without one, the same screen is a blank form.
2. Check the numbers afterwards on the species' edit page — a drafted species is saved
   without review. `SPEC.md` §7.2 lists what each constant means and the value ranges;
   pick the closest archetype rather than inventing a number.
3. Saving runs `catalog.Validate`, which rejects out-of-range constants, task kinds a
   species cannot declare, and a `base_interval_days` more than 40 % away from what the
   physics produces — that last check is a genuine sanity test on the entry, not a
   formality. A drafted species gets its base interval computed from the model.

The slug is the scientific name in kebab-case (`monstera-deliciosa`) and is a permanent
identity — renaming it orphans every plant referencing it.

Never delete a species that plants reference. Set `retired: true`.

## Never commit

The Discord webhook URL, Tado tokens, the OpenAI API key, or Docker Hub credentials
(the key reaches the cluster as a SealedSecret in the overlay, never in this repo). Cluster Helm
overlays may include timezone, hostname, and coordinates; the plantation chart
itself only holds placeholders.

## Definition of done

Your card's own list, plus: it builds, the linters are quiet, `go test ./... -race`
passes, and nothing outside your card's files changed. Report anything you had to assume.
