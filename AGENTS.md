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
`uuid`, `yaml.v3` and `htmx` covers this whole project. No web framework, no ORM, no
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
  URL, Tado tokens, or coordinates.
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

No Go code is involved.

1. Copy `catalog/species/_template.yaml` to `catalog/species/<slug>.yaml`. The slug is
   the scientific name in kebab-case (`monstera-deliciosa`) and is a permanent identity —
   renaming it orphans every plant referencing it.
2. Fill the fields. `SPEC.md` §7.2 lists what each constant means and the value ranges;
   pick the closest archetype rather than inventing a number.
3. `go test ./internal/catalog/...`. The validator rejects unknown fields, out-of-range
   constants, and a `base_interval_days` more than 40 % away from what the physics
   produces — that last check is a genuine sanity test on the entry, not a formality.
4. Open a PR. The species table is rebuilt from YAML at every boot; no migration, no
   manual SQL.

Never delete a species that plants reference. Set `retired: true`.

## Never commit

The Discord webhook URL, Tado tokens, or Docker Hub credentials. Cluster Helm
overlays may include timezone, hostname, and coordinates; the plantation chart
itself only holds placeholders.

## Definition of done

Your card's own list, plus: it builds, the linters are quiet, `go test ./... -race`
passes, and nothing outside your card's files changed. Report anything you had to assume.
