# Species drafting — follow-ups

Three open items left after [SPECIES-AI-PLAN.md](SPECIES-AI-PLAN.md) was implemented. The
behavioural contract is `SPEC.md` §16; read it first. Items 2 and 3 are decisions: pick an
option, then build it. Item 1 is cluster work and has no code change.

---

## 1. Anthropic API key as a SealedSecret

**Where:** the cluster overlay repo, not this one. Nothing in this repo changes.

**Context.** The chart already reads the key from an existing Secret
(`deploy/chart/templates/deployment.yaml`, the `anthropic.*` block in
`deploy/chart/values.yaml`). With `anthropic.existingSecretName` empty the app boots with
drafting off and the species screens show the manual form, which is the state today.

**What to do**

1. Create the Secret locally without putting the key in shell history, seal it against the
   cluster's sealed-secrets controller, and keep only the sealed output:

   ```sh
   read -rs ANTHROPIC_KEY
   kubectl create secret generic plantation-anthropic -n plantation \
     --from-literal=apiKey="$ANTHROPIC_KEY" --dry-run=client -o yaml \
     | kubeseal --format yaml > plantation-anthropic.sealedsecret.yaml
   unset ANTHROPIC_KEY
   ```

2. Commit `plantation-anthropic.sealedsecret.yaml` to the overlay, and set in the overlay's
   values:

   ```yaml
   anthropic:
     existingSecretName: plantation-anthropic
     existingSecretKey: apiKey     # the default; shown for clarity
     # model: claude-opus-5        # optional override (PLANTATION_ANTHROPIC_MODEL)
   ```

3. Never commit the plain Secret, never paste the key into an issue or chat. A trailing
   newline in the value is harmless: config trims it.

**Done when**

- The pod logs `"msg":"species drafting","enabled":true` at boot; that line is the
  confirmation the key arrived. The key itself is never logged. (The image is `scratch`,
  so there is no shell or `env` to `kubectl exec` into. To check the wiring without the
  value, `kubectl -n plantation get deploy plantation -o jsonpath='{..env[?(@.name=="PLANTATION_ANTHROPIC_API_KEY")].valueFrom}'`
  should name `plantation-anthropic` / `apiKey`.)
- One species drafted end to end through **Add → Add a species**, reviewed and saved.
- The live smoke test passes once, run locally with the same key:
  `PLANTATION_ANTHROPIC_API_KEY=... go test -tags smoke -run Live -v ./internal/species/`.
  This is the first time the prompt and schema meet the real API; until it passes, treat
  drafting as unverified.

---

## 2. The 1-day interval corner — decision needed

**Context.** A drafted species' intervals are computed, not asked for:
`base = catalog.ModelledIntervalDays(kc, mad, substrate)` and
`min, max = species.IntervalBounds(base)`. `catalog.Validate` requires
`1 ≤ min_interval_days < base_interval_days < max_interval_days`. When the model produces
a base of **1 day**, no valid `min` exists.

That happens for 10 of the 360 combinations in the documented ranges, all gritty `cactus`
mix with high `kc` and low `mad`: kc 0.9–1.5 with mad 0.2, and kc 1.3–1.5 with mad 0.3.
`TestToSpeciesValidAcrossParameterGrid` in `internal/species/proposal_test.go` lists them
and pins today's behaviour. Today those species are rejected with a `min_interval_days`
error on the review form, so nothing invalid is stored.

**Options**

- **A. Keep it (recommended).** The combination means "a thirsty, moisture-loving plant
  in fast-draining succulent mix", which no real plant is grown in. The error is the right
  answer, and the review form lets a person change the substrate or constants. Only do
  the small improvement: make the error say *why* on the form ("this combination needs
  watering every day; a moisture-loving plant is not grown in cactus mix — try peat or
  coir") instead of the bare interval message.
- **B. Allow `min == base`.** Relax the rule to `1 ≤ min ≤ base < max`, so a 1-day base
  stores as 1/1/4. Touches `internal/catalog/validate.go` (and its tests),
  `species.IntervalBounds`, and `SPEC.md` §7.6 and §16. First check that `care` behaves
  when the clamp floor equals the base — the property test in `internal/care` covers
  `[min,max]`, so extend it with `min == base`.
- **C. Clamp base up to 2.** Do not do this: a declared base of 2 against a modelled 1 is
  100 % off and fails the physics check, which would then need an exception, and the
  exception would weaken the check for every species.

**Done when** the option is chosen, `SPEC.md` §16 "Known gap" either stays (A) or is
replaced by the new rule (B), and the grid test asserts the chosen behaviour.

---

## 3. Hemisphere — decision needed

**Context.** Months in a species record are calendar months, and they are seasonal in two
places:

- `dormant_months`, the indoor rest period. The system prompt
  (`internal/species/prompt.go`) says "northern hemisphere" here.
- task `active_months` — a spring tidy in March, feeding April to September. **The prompt
  gives no hemisphere for these at all**, so the model assumes one silently.

For a southern-hemisphere garden both would be six months out. The app already knows
roughly where it is: `PLANTATION_TZ` is always set, and `PLANTATION_LATITUDE` is set
whenever weather is enabled.

**Options**

- **A. Keep northern, say it everywhere (recommended for now).** One user, in the UK.
  Make the assumption explicit rather than half-stated: one sentence in the prompt that
  *all* months, rest period and task months alike, are northern-hemisphere calendar months,
  and a line in `SPEC.md` §16. A few lines of change.
- **B. Derive it at boot.** Pass the hemisphere into `species.NewAnthropicDrafter` from
  `cmd/plantation`: the latitude sign when it is configured, otherwise "northern". The
  system prompt is built once per process, so it stays byte-stable and cacheable. Add a
  test that the prompt names the hemisphere it was given. Note that species already in the
  catalog keep whatever months they were written with; only new drafts change.
- **C. Store seasons, not months.** Model rest and task timing as seasons and convert to
  months at schedule time. This is correct for a shared catalog used in both hemispheres,
  but it is a data-model change across `domain`, `care`, the store and the form, and
  nothing here needs it yet.

**Done when** the option is chosen and the prompt states the hemisphere for every month
field it asks for. For B, that also means the drafter takes the hemisphere as a parameter
and has a test for it.
