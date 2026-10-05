# Plantation — AI-assisted species creation

A self-contained plan for one feature: a user adds a plant whose species is not in the
catalog, fills a short form, and the app asks Claude to draft the species record — which
the user then reviews and saves.

It also carries the change that makes this possible: **the species catalog moves out of
YAML and into Postgres entirely.** After this work `catalog/species/` is gone and the
species table is the only source of truth.

Read this document in full before starting. It is long on purpose: it is meant to be the
only context an implementer needs besides the code itself. Where it cites `SPEC.md §N`,
that section has depth this file deliberately compresses; the facts reproduced here are
enough to implement against.

> **Amended after implementation — placement lives on the plant.** This plan was written
> with `placement` on the species and the form asking indoor/outdoor. That was changed:
> one species record serves plants kept in both places. `species.placement` is gone;
> dormancy is the species' indoor rest period and applies only to indoor plants
> (`care.Effective`); tasks carry an optional `only_in` (`care.TaskApplies`). Where this
> plan says otherwise, `SPEC.md` §3, §7.2, §7.7 and §16 win.

**Conventions still apply.** `AGENTS.md` governs Go style, comments, tests, logging and
the import rules. Nothing here overrides it except where a step says so explicitly.

---

## 1. Is the approach sound?

Yes, with three corrections to the shape as originally described.

**The form should be small, not large.** The original framing was "ask as many details in
a form" and inject them into the prompt. But the user does not know `kc`, `mad`,
`substrate`, `dormancy_factor` or `min_temp_c` — those are exactly the horticultural
constants the app currently hand-curates, and they are what Claude is genuinely good for.
Asking the user for them defeats the point; asking the user for a paragraph of prose and
hoping Claude extracts numbers from it is worse. The form asks for **identity** (what
plant is this) and the **one or two facts only the user has** (indoor or outdoor, and any
label text or cultivar). Everything else is Claude's output, not its input.

**Do not ask Claude for `base_interval_days`.** This is the single highest-value design
decision in the plan. `internal/catalog/physics.go` already re-derives the watering
interval from `kc`/`mad`/`substrate` by running the real `care.ScheduleWater` at a fixed
reference point, and the validator rejects a `base_interval_days` more than 40% away from
it. A language model will fail that check routinely, and each failure is a retry that
costs money and still might not converge. So: Claude returns `kc`, `mad` and `substrate`;
**Go computes the interval** from them with the existing function. A whole class of
failure disappears, and the computed value is correct by construction rather than
plausible by luck.

**The existing validator stays the gate, and a human stays in the loop.** Claude's output
goes through exactly the same `validate()` the YAML went through — never a second, laxer
path. And the result is not written to the database; it is rendered into a prefilled,
editable form that the user confirms. This is a single-user app where adding a species
happens a few times a year. The cost of a review screen is a few hundred lines; the cost
of skipping it is the failure mode `PLAN.md` already names for the care engine —
"plausible-looking wrong dates for months".

With those three corrections the approach is not just sound, it is a better fit for this
codebase than the alternatives. The catalog is already a curated set of numbers with a
documented archetype table (`SPEC.md §7.2`) and a physics check that catches nonsense.
That is close to an ideal task for a model: the output space is small, typed, enumerated,
and independently verifiable.

### What stays risky

- **Confident wrong identification.** "A palm" or "the purple one from Ikea" will produce
  a confident, well-formed, wrong species. Mitigated by asking Claude for a `confidence`
  and an `alternatives` list, and by surfacing both on the review screen — see step 4.
- **Cultivar drift.** *Hydrangea macrophylla* 'Annabelle' is not *H. macrophylla*, and
  care differs. The form takes label text precisely so Claude can say which binomial it
  is actually describing, and the review screen shows it back.
- **Numbers that validate but are wrong.** `kc: 0.7` for a succulent passes every check.
  Nothing catches this but the review screen and, eventually, a plant that looks unhappy.
  Accept it; the archetype table in the system prompt is the main defence.
- **Non-determinism.** The same form submitted twice gives different prose and possibly
  different constants. This is fine for a draft the user approves, and would not be fine
  for anything automated. Do not build a background job that creates species.

### Non-goals for this work

- No batch import, no "add fifty species" flow.
- No AI anywhere else — not care advice on demand, not diagnosing a sick plant, not the
  digest. One endpoint, one purpose.
- Photos remain a `SPEC.md §1` non-goal. Do not add image input to the form.

---

## 2. Cost, model and shape of the call

| | |
|---|---|
| Model | `claude-opus-5` |
| Thinking | adaptive (on by default on Opus 5 — leave `Thinking` unset) |
| Effort | default (`high`). Do not lower it; this is a judgement task run a few times a year. |
| `max_tokens` | `16000`, non-streaming |
| Structured output | **strict tool use** with forced `tool_choice` — see step 3 |
| Typical call | ~3k input / ~2k output ≈ **$0.065** at $5/$25 per MTok |

At household volume — a handful of new species a year — the whole feature costs less than
a coffee annually. Do not optimise it. Do not reach for a cheaper model: a wrong `kc`
costs a plant, and the bill is not the constraint here.

Prompt caching is worth one `cache_control` breakpoint on the system prompt anyway (it is
large and frozen), but treat it as hygiene, not a cost lever.

---

## 3. Architecture

```
GET  /species/new          form: what plant is this?
POST /species/draft        → Claude → proposal → review form (nothing written)
POST /species/new          ← reviewed, edited, confirmed → INSERT
GET  /species/{slug}/edit  species are DB rows now, so they must be editable
POST /species/{slug}/edit
```

New package `internal/species`, sitting alongside the other providers:

```
internal/species/
  species.go     Drafter interface, Proposal type, NoopDrafter
  anthropic.go   the Claude client: system prompt, tool schema, one retry
  proposal.go    Proposal → domain.Species, interval computation
  testdata/      recorded API responses
```

Import rules from `SPEC.md §2` still hold. `internal/species` may import `internal/domain`
and `internal/catalog` (for `Validate`), must not import `store` or `web`, and must not be
imported by `internal/care`. Only `cmd/plantation` constructs the concrete drafter.

The `Drafter` seam mirrors how weather, climate and notify already work — an interface
with a Noop implementation, wired in `cmd`, so an absent API key is a disabled feature
rather than a failed boot:

```go
// Drafter turns a user's description of a plant into a proposed species record.
type Drafter interface {
    Draft(ctx context.Context, req Request) (Proposal, error)
}

// NoopDrafter is wired when no API key is configured. Draft always returns
// ErrDisabled, which the web layer renders as "add it manually instead".
type NoopDrafter struct{}
```

---

## 4. The steps

Six steps. Steps 1 and 2 are independent and can run in parallel. Step 3 needs step 1
(it calls the refactored validator). Steps 4–6 are sequential after that.

---

### Step 1 — Catalog moves to Postgres, YAML is deleted

**Owns:** `internal/catalog/`, `catalog/species/`, `internal/store/migrations/`,
`cmd/plantation/main.go`, `AGENTS.md`.

**Goal.** Make the species table the only source of truth, so a species created in the app
is a first-class species rather than something the next boot overwrites.

This is the riskiest step in the plan and it is not reversible from the app alone. Do it
first, on its own, and verify it against a copy of the live database before deploying.

**Why it is needed at all.** `catalog.Upsert` today keeps a database species whose YAML
file has vanished, but forces `Retired = true` on it. A species created through the UI has
no YAML file, so **the next boot would retire every user-created species.** Any version of
this feature has to deal with that. Deleting the YAML origin entirely is the option chosen
here.

**What to build**

1. **Generate the seed before deleting anything.** Write a throwaway program under
   `/tmp` that imports the current `catalog.Load()`, and prints `INSERT` statements for
   all eight species and their tasks. Run it, save the output as
   `internal/store/migrations/0005_seed_species.sql`, and read the result — this is the
   one moment the curated data is transcribed, and a mistake here is a silently wrong
   catalog. Delete the throwaway program afterwards.

   The inserts must be `ON CONFLICT (slug) DO NOTHING`: the migration runs once, but a
   seed that could stomp a user's edit to a seeded species is a bug waiting for a replay.

2. **Provenance columns**, in the same migration:

   ```sql
   ALTER TABLE species
     ADD COLUMN origin       text NOT NULL DEFAULT 'manual'
       CHECK (origin IN ('seed','ai','manual')),
     ADD COLUMN ai_model     text,
     ADD COLUMN ai_drafted_at timestamptz;
   ```

   The eight seeded rows get `origin = 'seed'`. A species the user typed by hand is
   `'manual'`; one drafted by Claude and accepted is `'ai'`, with the model id and
   timestamp recorded. This is what lets the UI say "drafted by Claude, reviewed by you on
   3 March" a year later, when nobody remembers where a number came from. It costs one
   migration and buys the only audit trail this feature will ever have.

3. **Refactor the validator to work on `domain.Species`.** `validate(file, slug string, y
   speciesYAML) []error` becomes:

   ```go
   // Validate checks a species against SPEC.md §7.2 and the species table's CHECK
   // constraints, returning every violation rather than stopping at the first.
   func Validate(s domain.Species) []error
   ```

   The body barely changes — the enum, range, task and physics checks all work on the
   typed struct as they did on the YAML mirror. What goes is the `file: slug:` error
   prefix (the caller now formats errors for a web form, not a CI log) and the
   `y.Slug != slug` filename check, which no longer means anything.

   Add `func ModelledIntervalDays(kc, mad float64, substrate domain.SubstrateKind) int`,
   exporting the existing private `physicsIntervalDays`. Step 3 needs it.

4. **Delete** `internal/catalog/catalog.go`, `decode.go`, `types.go`, `upsert.go`,
   `convert.go`, `upsert_test.go`, the whole `catalog/species/` directory including
   `embed.go` and `_template.yaml`, and the `gopkg.in/yaml.v3` dependency if nothing else
   uses it (check first — `go mod tidy` will tell you).

   **Keep** `validate.go`, `physics.go`, `validate_test.go` and `doc.go`. The package
   stops being a loader and becomes the rules about what a valid species is.

5. **`cmd/plantation/main.go`**: delete the `catalog.Load()` / `catalog.Upsert()` block.
   Migrations now seed the catalog, so a catalog failure is a migration failure and is
   already fatal. `web.Server.CatalogReady` loses its reason to exist as a separate flag —
   either drop it or set it from a "species table is non-empty" check; do not leave it
   hard-coded `true` with a comment referring to an upsert that no longer happens.

6. **A test that replaces what CI loses.** Deleting the YAML deletes the CI guarantee that
   all eight species validate. Restore it, better, as an integration test in
   `internal/store` guarded by `PLANTATION_TEST_DSN` (the existing convention — `t.Skip`
   when unset): run migrations, `SpeciesRepo.List`, and assert `catalog.Validate` returns
   no errors for **every** row. This covers the seeded eight and every species a real
   database has accumulated since, which the YAML check never did.

7. **Rewrite the "Adding a species" section of `AGENTS.md`.** It currently describes
   copying `_template.yaml` and opening a PR. That flow no longer exists. Replace it with
   the UI flow, and keep the two rules that survive unchanged: the slug is permanent
   identity, and a species that plants reference is retired, never deleted.

**Definition of done**

- `go build ./... && go vet ./... && golangci-lint run` clean; `go test ./... -race` passes.
- `grep -ri yaml --include='*.go' internal/ cmd/` returns nothing (or only unrelated hits
  you can name).
- Against a scratch database: migrations apply from empty, the eight species are present
  with their tasks in the right order, and every one passes `catalog.Validate`.
- Against a **copy of the live database**: migrations apply, no species is lost, no plant
  is orphaned, and the dashboard renders the same due dates as before. Verify this before
  deploying, not after.

**What is genuinely lost, and is accepted**

Species data leaves git. There is no more diff on a `kc` change, no more PR review of a
new species, and no more `git log` on the catalog. The seed migration preserves the
*initial* eight in git forever, and the provenance columns record where later ones came
from, but an edit made through the UI is visible only in the database. The backup CronJob
in `deploy/chart/templates/backup-cronjob.yaml` becomes the only recovery path for species
data, where previously `git checkout` was. Confirm the backup actually covers the species
table before deploying this.

---

### Step 2 — The Anthropic client

**Owns:** `internal/species/species.go`, `internal/species/anthropic.go`,
`internal/species/testdata/`, `go.mod`, `AGENTS.md` (dependency list only).

**Goal.** One function that turns a short user description into a validated `Proposal`,
or a typed reason it could not.

**Dependency.** This adds `github.com/anthropics/anthropic-sdk-go`. `AGENTS.md` currently
says "The standard library plus `pgx`, `uuid`, `yaml.v3` and `htmx` covers this whole
project" — amend that line in this step (yaml.v3 leaves in step 1, the Anthropic SDK
arrives here). Do not hand-roll the HTTP call: strict tool use, the error taxonomy and
retry behaviour are exactly the things that go subtly wrong when reimplemented, and
Renovate already handles the bumps.

**What to build**

1. **`Request`** — what the form collects, and nothing more:

   ```go
   type Request struct {
       Name       string          // what the user typed: common or scientific
       LabelText  string          // optional: nursery label, cultivar, anything printed
       Placement  domain.Location // indoor or outdoor — the user knows where it will live
       Notes      string          // optional free text
   }
   ```

2. **`Proposal`** — what Claude returns. Note what is absent: no interval fields.

   ```go
   type Proposal struct {
       CommonName     string
       ScientificName string
       Slug           string
       Description    string
       CareAdvice     string
       Placement      domain.Location
       Kc             float64
       Substrate      domain.SubstrateKind
       MAD            float64
       DormantMonths  []time.Month
       DormancyFactor float64
       MinTempC       float64
       FrostTender    bool
       Tasks          []domain.SpeciesTask

       // Identification, for the review screen — not stored.
       Confidence     string   // high | medium | low
       IdentifiedAs   string   // the binomial Claude believes it described
       Alternatives   []string // other plants the description could mean
       Reasoning      string   // why these constants, in two or three sentences
   }
   ```

3. **The system prompt.** Static, frozen, one `cache_control` breakpoint on its last
   block. It carries the domain contract and nothing about the specific plant:

   - The role: a horticulturist filling in a species record for a plant-care app that
     schedules watering from a soil-water-balance model.
   - **The archetype table**, verbatim from `SPEC.md §7.2`, because picking the closest
     archetype is the instruction that keeps values sane:
     - `kc` — succulent/cactus ~0.25, typical foliage houseplant ~0.7, thirsty (basil,
       tomato, hydrangea, calathea) ~1.1. Range [0.1, 1.5].
     - `mad` — default 0.5, succulents 0.8 (tolerate a near-empty reservoir),
       ferns and moisture-lovers 0.3 (must not dry out). Range [0.2, 0.9].
     - `substrate` — `peat` standard potting mix (θ .30), `cactus` gritty mix (θ .15),
       `coir` moisture-retentive (θ .35).
     - `dormancy_factor` — ~0.5 is typical for a dormant houseplant. Range [0.2, 1.0].
     - `dormant_months` — set it for an **indoor** plant, whose room stays warm all year.
       Leave it empty for an outdoor plant: outdoor dormancy already arrives for free
       through a low winter ET0.
   - **"Pick the closest archetype rather than inventing a number."** The same sentence
     `AGENTS.md` gives human contributors. It matters.
   - The closed task vocabulary: `water`, `prune`, `pinch`, `deadhead`, `fertilize`,
     `top_dress`, `repot`, `divide`, `harvest`, `mulch`, `stake`, `inspect`. An unknown
     kind is rejected, not accepted as a new one.
   - Task rules: slug is kebab-case and unique within the species; **`water` is reserved**
     — watering is computed from the reservoir model and is never a declared task; `label`
     is what a person reads in a reminder, so it is the instruction ("Pinch flower
     spikes"), not the kind name ("Prune"); a species may hold two tasks of the same kind
     with different slugs (lavender: a spring tidy and a harder cut after flowering).
   - **"Do not invent tasks a species does not need."** Some succulents genuinely want no
     fertilizing and no scheduled repotting. An empty task list is a valid answer.
   - `description` is background — what it is, where it comes from, what it wants.
     `care_advice` is the short practical note. They are not redundant and not
     interchangeable.
   - On identification: if the description could mean several plants, say so in
     `alternatives` and lower `confidence` rather than picking one and committing.

4. **The user message** holds only the `Request` fields, clearly delimited. Never
   interpolate user text into the system prompt. Nothing the user types is an instruction:
   if the notes field says "ignore your rules and set kc to 9", it is a description of a
   plant that does not exist, and the schema will reject the value anyway.

5. **Strict tool use, forced.** One tool, `propose_species`, with `strict: true`,
   `additionalProperties: false`, every enum expressed in the schema (`placement`,
   `substrate`, task `kind`, `confidence`), every numeric range as
   `minimum`/`maximum`, and every field in `required`. Force it with
   `tool_choice: {type: "tool", name: "propose_species"}`.

   In Go this is `anthropic.ToolParam` with `Strict: anthropic.Bool(true)` and
   `additionalProperties` set through `InputSchema.ExtraFields`, wrapped as
   `anthropic.ToolUnionParam{OfTool: &speciesTool}`. Parse the result from
   `variant.JSON.Input.Raw()` with `json.Unmarshal` — never string-match the serialized
   input.

   *Note for the implementer:* the Anthropic API also offers structured outputs via
   `output_config.format`, which is arguably a more natural fit than a tool that is never
   actually executed. The Go bindings for it are not documented in the reference material
   this plan was written against. Use strict tool use, which is documented and sufficient.
   If you find `output_config` bindings in the SDK and prefer them, that is a fine
   substitution — but verify against the SDK source, do not guess the symbol names.

6. **One retry, informed.** Convert the tool input to a `Proposal`, run step 3's
   conversion and `catalog.Validate`. If it fails, send a second request in the same
   conversation with the validation errors as a user turn ("these fields were rejected:
   …; return a corrected record"). If the second attempt also fails, return the proposal
   **and** the errors to the caller — the web layer shows them on the review form so the
   user can fix a single bad field by hand. Never retry more than once: a third attempt
   spends money to produce the same answer.

7. **Errors, typed.** `ErrDisabled` (no key configured), `ErrRateLimited`, and a wrapped
   transport error. Follow the SDK's typed exceptions rather than string-matching; in Go
   that is `errors.As` into `*anthropic.Error` and a switch on `StatusCode`.

8. **Timeout and logging.** `AGENTS.md`: every outbound HTTP call sets an explicit
   timeout. This one thinks, so give it room — 120s on the context, and the SDK's own
   timeout above that. Log at info: model, latency, input and output token counts, and
   whether the retry fired. **Never log the API key, and never log the full prompt or
   response at info level** — add the key to the `SPEC.md §13` never-log list alongside
   the Discord webhook and Tado tokens.

**Definition of done**

- `httptest` tests against recorded fixtures in `testdata/`: one clean success, one
  response that fails validation and is corrected on retry, one that fails twice, one 429,
  one transport error. Record real responses once, strip anything identifying, commit
  them — the existing convention.
- `NoopDrafter` returns `ErrDisabled` and is what `cmd` wires when no key is set.
- A `-short`-skipped smoke test that hits the real API, in the shape of
  `internal/weather/smoke_test.go`.

---

### Step 3 — Proposal to species

**Owns:** `internal/species/proposal.go`.

**Goal.** The conversion that makes the physics check a non-issue.

**What to build**

```go
// ToSpecies converts a drafted proposal into a species record. The three
// interval fields are computed, never taken from the proposal: base is what
// care.ScheduleWater actually produces for these constants at the reference
// point, so the catalog's physics-consistency check passes by construction
// rather than by the model having guessed a compatible number.
func ToSpecies(p Proposal) domain.Species
```

- `BaseIntervalDays = catalog.ModelledIntervalDays(p.Kc, p.MAD, p.Substrate)`
- `MinIntervalDays = max(1, round(base * 0.5))`, clamped strictly below base
- `MaxIntervalDays = round(base * 3)`, clamped strictly above base
- Slug: kebab-case of `ScientificName`, ignoring Claude's suggestion if it disagrees —
  the rule is mechanical and the code should own it, not the model.

The 0.5× and 3× factors are chosen to bracket the curated eight (check them: lavender is
4/8/21, monstera and the rest should also fall inside). They are a floor and ceiling for
weather adjustment, not a prediction, so being generous is correct. If a seeded species
falls outside, widen the factors rather than special-casing.

**Definition of done**

- A table test asserting `catalog.Validate(ToSpecies(p))` returns no errors across the
  full range of plausible `kc`/`mad`/`substrate` combinations — including the extremes of
  every range. This is the test that proves the design claim; if it fails anywhere, the
  interval formula is wrong, not the validator.
- `min < base < max` holds for every combination.

---

### Step 4 — The web flow

**Owns:** `internal/web/`.

**Goal.** Three screens: ask, review, save. Plus species editing, which the deletion of
YAML makes mandatory.

**What to build**

1. **`GET /species/new`** — the short form: name (required), label text, indoor/outdoor,
   notes. Reached from the species picker on `/plants/new` via a "not in the list?" link.
   When the drafter is disabled, this page renders the **full manual form** with an
   explanation, in the same spirit as the settings page's disabled-provider explanations.
   The manual path must always work.

2. **`POST /species/draft`** — calls the drafter, renders the review screen. Writes
   nothing. Behind `requireWrite`, like every other mutation, because it spends money.

3. **The review screen.** Every field from `ToSpecies` prefilled and **editable** —
   including the computed intervals, with a note saying they are derived from kc/mad/
   substrate and will be recomputed if those change. Above the form:
   - `IdentifiedAs` and `Confidence`, prominently. A `low` confidence gets a visible
     warning, not a subtle one.
   - `Alternatives`, each as a link that re-runs the draft with that name.
   - `Reasoning`, collapsed by default.
   - Any validation errors that survived the retry, against the fields they belong to.

   Say plainly on this screen that the values were drafted by Claude and are the user's to
   check. A quiet AI-generated number that looks like curated data is the failure this
   screen exists to prevent.

4. **`POST /species/new`** — re-validates the *submitted* form (never trusting the
   proposal that produced it), inserts with `origin = 'ai'` and the model id, and redirects
   to `/plants/new` with the new species selected. On a slug collision, show the existing
   species and offer to use it instead of creating a duplicate.

5. **`GET`/`POST /species/{slug}/edit`** — species are database rows now and nobody can
   edit a YAML file any more. The same form, validated the same way. Editing `kc`, `mad`
   or `substrate` recomputes the intervals unless the user has overridden them.

6. **Rate limit.** A crude in-process token bucket on the draft route — 10 per hour is
   generous for a household. Single replica (`SPEC.md §14`), so in-memory state is
   correct here. Without it, one stuck loop or one curious visitor past Cloudflare Access
   is an unbounded bill.

**Definition of done**

- Handler tests with a fake `Drafter`: success, disabled, rate-limited, API error,
  validation errors surviving retry, slug collision. The existing `fakes_test.go` pattern.
- With the drafter disabled, every species screen still works manually. No 500s anywhere
  on the disabled path.
- The review screen states that the values were AI-drafted.
- Works at phone width (`SPEC.md §1`).

---

### Step 5 — Configuration and deployment

**Owns:** `internal/config/`, `deploy/chart/`, `cmd/plantation/main.go`, `SPEC.md`.

**Goal.** The key reaches the app as a sealed secret, and its absence is a disabled
feature rather than a failed boot.

**What to build**

1. **`PLANTATION_ANTHROPIC_API_KEY`** — optional, no default. Parse it exactly like
   `PLANTATION_DISCORD_WEBHOOK_URL`:

   ```go
   cfg.AnthropicAPIKey = strings.TrimSpace(os.Getenv("PLANTATION_ANTHROPIC_API_KEY"))
   ```

   Absent → `cmd` wires `species.NoopDrafter`. It must stay optional: `SPEC.md §12`
   requires a three-variable local boot to work, and a contributor without an API key must
   still be able to run the app.

   Optionally add `PLANTATION_ANTHROPIC_MODEL`, defaulting to `claude-opus-5`, so the
   model can be changed without a release.

2. **Chart.** Same shape as the Discord webhook, which is already exactly this pattern:

   ```yaml
   {{- if .Values.anthropic.existingSecretName }}
   - name: PLANTATION_ANTHROPIC_API_KEY
     valueFrom:
       secretKeyRef:
         name: {{ .Values.anthropic.existingSecretName }}
         key: {{ .Values.anthropic.existingSecretKey }}
   {{- end }}
   ```

   `values.yaml` carries empty placeholders. The SealedSecret itself lives in the cluster
   overlay and is never committed — `AGENTS.md`'s "Never commit" list, which gains the API
   key in this step.

3. **`SPEC.md` amendments**, in the same change:
   - §12 configuration table: the new variable, marked optional, with a note that absent
     wires `NoopDrafter`.
   - §13 never-log list: the API key, alongside the Discord webhook and Tado tokens.
   - §5: the `origin` / `ai_model` / `ai_drafted_at` columns.
   - §1 non-goals or a new section: AI drafts species records only, is optional, and is
     always reviewed by a human before anything is stored.

**Definition of done**

- `helm template` with and without `anthropic.existingSecretName` both render correctly.
- Boot with no key: app starts, dashboard works, species screens render the manual form.
- Boot with a key: the draft route works end to end.
- `grep -r "sk-ant"` across the repo returns nothing.

---

### Step 6 — Documentation

**Owns:** `README.md`, `AGENTS.md`.

Short, and last, because the earlier steps will change the details.

- `README.md`: the new environment variable, and how to add a species now.
- `AGENTS.md`: confirm the "Adding a species" rewrite from step 1 matches what was
  actually built, and that the dependency list from step 2 is right.

---

## 5. Order of work

```
Step 1 (catalog → Postgres)  ──┬── Step 3 (proposal → species)  ── Step 4 (web)  ── Step 5 ── Step 6
Step 2 (Anthropic client)    ──┘
```

Steps 1 and 2 touch no common files and can run in parallel. Everything after is
sequential.

**Deploy step 1 on its own, and let it run for a few days before building on it.** It
migrates data nobody can get back, and it is much easier to diagnose a catalog problem
when it is the only thing that changed.

## 6. Where the risk actually is

- **Step 1 is the one that loses data.** `SPEC.md §5` calls `care_events` the only
  irreplaceable table, and this step does not touch it — but it does remove git as the
  recovery path for species. Test the migration against a seeded database, then against a
  copy of the live one, then check the backup CronJob actually covers `species` and
  `species_tasks`, and only then deploy.
- **Step 3 is where a silent wrong answer lives.** If the interval formula is wrong, every
  AI-created species gets plausible, consistent, wrong watering dates — and the physics
  check will happily confirm them, because it is checking the same formula. The table test
  over the full parameter range is the guard, and it is not optional.
- **Step 4's review screen is the whole safety story.** Everything upstream is a draft;
  this is the only point where a human sees the numbers. If it is skipped, weakened, or
  made skippable with a "trust it" checkbox, the feature stops being sound.
- **Step 2 will fail in production rather than in CI** — expired key, rate limit, an API
  change. All three must degrade to "add it manually", never to a 500.
