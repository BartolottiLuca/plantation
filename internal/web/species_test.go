package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/species"
)

// fakeDrafter stands in for the model client. If gate is set, Draft blocks on
// it, which lets a test look at a draft while it is still pending.
type fakeDrafter struct {
	mu    sync.Mutex
	calls []species.Request
	gate  chan struct{}
	res   species.Result
	err   error
}

func (f *fakeDrafter) Draft(_ context.Context, req species.Request) (species.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	if f.gate != nil {
		<-f.gate
	}
	return f.res, f.err
}

func (f *fakeDrafter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func draftedResult(conf species.Confidence) species.Result {
	p := species.Proposal{
		CommonName:     "Wax plant",
		ScientificName: "Hoya carnosa",
		Description:    "A waxy climber from Asia and Australia.",
		CareAdvice:     "Let it dry between waterings.",
		Kc:             0.4,
		Substrate:      domain.Cactus,
		MAD:            0.7,
		DormantMonths:  []time.Month{11, 12, 1, 2},
		DormancyFactor: 0.5,
		MinTempC:       10,
		FrostTender:    true,
		Tasks: []domain.SpeciesTask{
			{Slug: "fertilize", Kind: domain.Fertilize, Label: "Feed", IntervalDays: 30, ActiveMonths: []time.Month{4, 5, 6, 7, 8, 9}},
		},
		Confidence:   conf,
		IdentifiedAs: "Hoya carnosa",
		Alternatives: []string{"Hoya kerrii"},
		Reasoning:    "A semi-succulent climber, so a low kc in gritty mix.",
	}
	sp := species.ToSpecies(p)
	at := testNow
	sp.Origin, sp.AIModel, sp.AIDraftedAt = domain.OriginAI, "gpt-6-astra", &at
	return species.Result{Proposal: p, Species: sp, Model: "gpt-6-astra", Problems: catalog.Validate(sp)}
}

func speciesUI(t *testing.T, d species.Drafter) (*Server, *memDB, *http.ServeMux) {
	t.Helper()
	return testUI(t, func(_ *memDB, s *Server) { s.Drafter = d })
}

// waitForDraft polls the draft page the way the browser would, until it is no
// longer pending.
func waitForDraft(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := doGET(t, mux, path)
		if !strings.Contains(html(rec), `hx-get="/drafts/`) {
			return rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("draft %s still pending after 5s", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startDraft(t *testing.T, mux http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return doPOST(t, mux, "/species/draft", form)
}

func askForm() url.Values {
	return url.Values{"name": {"wax plant"}}
}

func manualSpeciesForm() url.Values {
	base := catalog.ModelledIntervalDays(0.7, 0.5, domain.Peat)
	lo, hi := species.IntervalBounds(base)
	return url.Values{
		"common_name":            {"Hoya"},
		"scientific_name":        {"Hoya carnosa"},
		"kc":                     {"0.7"},
		"mad":                    {"0.5"},
		"substrate":              {"peat"},
		"base_interval_days":     {strconv.Itoa(base)},
		"min_interval_days":      {strconv.Itoa(lo)},
		"max_interval_days":      {strconv.Itoa(hi)},
		"dormant_months":         {"11, 12, 1, 2"},
		"dormancy_factor":        {"0.5"},
		"min_temp_c":             {"10"},
		"frost_tender":           {"1"},
		"description":            {"A waxy climber."},
		"care_advice":            {"Let it dry."},
		"tasks[0].slug":          {"prune"},
		"tasks[0].kind":          {"prune"},
		"tasks[0].label":         {"Prune"},
		"tasks[0].interval_days": {"120"},
		"tasks[0].active_months": {"3, 4, 5"},
	}
}

func location(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return rec.Header().Get("Location")
}

func TestNewSpeciesOffersDraftingWhenEnabled(t *testing.T) {
	_, _, mux := speciesUI(t, &fakeDrafter{})
	rec := doGET(t, mux, "/species/new")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec, "Draft with AI", `action="/species/draft"`, "/species/new?manual=1")
	assertNoExternalAssets(t, rec)
}

func TestNewSpeciesFallsBackToManualFormWhenDisabled(t *testing.T) {
	for name, d := range map[string]species.Drafter{"nil": nil, "noop": species.NoopDrafter{}} {
		t.Run(name, func(t *testing.T) {
			_, _, mux := speciesUI(t, d)
			rec := doGET(t, mux, "/species/new")
			assertStatus(t, rec, http.StatusOK)
			assertContains(t, rec, "not set up on this server", `action="/species/new"`, `name="kc"`)
			assertNotContains(t, rec, "Draft with AI")
		})
	}
}

func TestManualFormIsReachableWhenEnabled(t *testing.T) {
	_, _, mux := speciesUI(t, &fakeDrafter{})
	rec := doGET(t, mux, "/species/new?manual=1&name=Hoya")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, `name="kc"`, `value="Hoya"`)
	assertNotContains(t, rec, `name="placement"`)
	assertNotContains(t, rec, "not set up on this server")
}

func TestDraftLifecyclePendingThenSavedStraightAway(t *testing.T) {
	d := &fakeDrafter{gate: make(chan struct{}), res: draftedResult(species.ConfidenceHigh)}
	_, db, mux := speciesUI(t, d)

	rec := startDraft(t, mux, askForm())
	assertStatus(t, rec, http.StatusSeeOther)
	path := location(t, rec)
	if !strings.HasPrefix(path, "/drafts/") {
		t.Fatalf("redirect = %q, want /drafts/<id>", path)
	}

	// Still thinking: a full page that polls itself, and a bare fragment for htmx.
	pending := doGET(t, mux, path)
	assertStatus(t, pending, http.StatusOK)
	assertContains(t, pending, `hx-trigger="every 2s"`, "The AI is working on wax plant")
	assertNoExternalAssets(t, pending)
	frag := htmxGET(t, mux, path)
	assertStatus(t, frag, http.StatusOK)
	assertContains(t, frag, `id="draft-status"`)
	assertNotContains(t, frag, "<html")
	if _, saved := db.species["hoya-carnosa"]; saved {
		t.Fatal("saved before the draft finished")
	}

	close(d.gate)
	done := waitForDraft(t, mux, path)
	assertStatus(t, done, http.StatusSeeOther)
	if got, want := location(t, done), "/plants/new?species=hoya-carnosa&drafted=1"; got != want {
		t.Errorf("redirect = %q, want %q", got, want)
	}

	sp, ok := db.species["hoya-carnosa"]
	if !ok {
		t.Fatal("a clean draft was not saved")
	}
	if sp.Origin != domain.OriginAI || sp.AIModel != "gpt-6-astra" || sp.AIDraftedAt == nil || !sp.AIDraftedAt.Equal(testNow) {
		t.Errorf("provenance = %q/%q/%v, want ai/gpt-6-astra/%v", sp.Origin, sp.AIModel, sp.AIDraftedAt, testNow)
	}
	if sp.Kc != 0.4 || len(sp.Tasks) != 1 {
		t.Errorf("saved species is not the draft: %+v", sp)
	}

	// A finished job answers htmx's poll by sending the page to the same place.
	if got := htmxGET(t, mux, path).Header().Get("HX-Redirect"); got != "/plants/new?species=hoya-carnosa&drafted=1" {
		t.Errorf("HX-Redirect = %q", got)
	}
}

func htmxGET(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// There is no confirmation step, so a low-confidence identification is saved
// like any other clean draft; the plant form's note is the prompt to check it.
func TestLowConfidenceDraftIsSavedToo(t *testing.T) {
	d := &fakeDrafter{res: draftedResult(species.ConfidenceLow)}
	_, db, mux := speciesUI(t, d)
	rec := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
	assertStatus(t, rec, http.StatusSeeOther)
	if _, ok := db.species["hoya-carnosa"]; !ok {
		t.Fatal("a valid low-confidence draft was not saved")
	}
}

func TestReviewShowsProblemsThatSurvivedTheCorrection(t *testing.T) {
	res := draftedResult(species.ConfidenceMedium)
	res.Species.Kc = 3.5
	res.Species.Tasks = append(res.Species.Tasks, domain.SpeciesTask{Slug: "soak", Kind: domain.Water, Label: "Soak", IntervalDays: 7})
	res.Problems = catalog.Validate(res.Species)
	res.Retried = true
	d := &fakeDrafter{res: res}
	_, db, mux := speciesUI(t, d)

	rec := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec,
		"kc 3.5 out of range [0.1, 1.5]",
		"scheduled from the reservoir model",
		"corrected once",
	)
	// The rejected values are still in the form for a person to fix by hand,
	// and the page says why it is being shown at all.
	assertContains(t, rec, `value="3.5"`, "could not be saved automatically", `name="origin" value="ai"`)
	if _, saved := db.species["hoya-carnosa"]; saved {
		t.Error("a draft that failed validation was saved")
	}
}

func TestDraftFailureShowsAFriendlyMessageNotTheError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"rate limited", species.ErrRateLimited, "rate limiting"},
		{"out of credit", species.ErrNoQuota, "run out of credit"},
		{"refused", species.ErrRefused, "declined"},
		{"truncated", species.ErrTruncated, "cut off"},
		{"anything else", errors.New(`POST "https://api.openai.com/v1/responses": 401 sk-secret-key-value`), "could not be reached"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDrafter{err: tt.err}
			_, _, mux := speciesUI(t, d)
			rec := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
			assertStatus(t, rec, http.StatusOK)
			assertContains(t, rec, "Could not draft that", tt.want, "Fill it in by hand instead", "manual=1")
			assertNotContains(t, rec, "sk-secret", "api.openai.com", "401")
		})
	}
}

func TestDraftRejectsAnInvalidRequestWithoutCallingTheDrafter(t *testing.T) {
	d := &fakeDrafter{}
	_, _, mux := speciesUI(t, d)
	rec := startDraft(t, mux, url.Values{"name": {"  "}})
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "Name is required")
	if d.callCount() != 0 {
		t.Errorf("drafter called %d times for an invalid request", d.callCount())
	}
}

func TestDraftIsRateLimited(t *testing.T) {
	d := &fakeDrafter{res: draftedResult(species.ConfidenceHigh)}
	_, _, mux := speciesUI(t, d)
	for i := 0; i < draftsPerHour; i++ {
		assertStatus(t, startDraft(t, mux, askForm()), http.StatusSeeOther)
	}
	rec := startDraft(t, mux, askForm())
	assertStatus(t, rec, http.StatusTooManyRequests)
	assertContains(t, rec, "a lot of drafts", "/species/new?manual=1")
}

func TestDraftWhenDisabledSendsYouToTheManualForm(t *testing.T) {
	_, _, mux := speciesUI(t, species.NoopDrafter{})
	rec := startDraft(t, mux, askForm())
	assertStatus(t, rec, http.StatusSeeOther)
	if got := location(t, rec); got != "/species/new?manual=1" {
		t.Errorf("redirect = %q", got)
	}
}

func TestUnknownDraftIs404(t *testing.T) {
	_, _, mux := speciesUI(t, &fakeDrafter{})
	assertStatus(t, doGET(t, mux, "/drafts/deadbeef"), http.StatusNotFound)
}

func TestDraftingRoutesRequireTheWriteToken(t *testing.T) {
	_, _, mux := testUI(t, func(_ *memDB, s *Server) {
		s.WriteToken = "secret"
		s.Drafter = &fakeDrafter{}
	})
	for _, path := range []string{"/species/draft", "/species/new", "/species/monstera-deliciosa/edit"} {
		rec := doPOST(t, mux, path, askForm())
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s without a token = %d, want 401", path, rec.Code)
		}
	}
}

func TestCreateSpeciesManually(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	rec := doPOST(t, mux, "/species/new", manualSpeciesForm())
	assertStatus(t, rec, http.StatusSeeOther)
	if got := location(t, rec); got != "/plants/new?species=hoya-carnosa" {
		t.Errorf("redirect = %q, want the plant form with the new species selected", got)
	}
	sp, ok := db.species["hoya-carnosa"]
	if !ok {
		t.Fatal("species was not stored under the slug derived from its scientific name")
	}
	if sp.Origin != domain.OriginManual || sp.AIModel != "" || sp.AIDraftedAt != nil {
		t.Errorf("provenance = %q/%q/%v, want manual", sp.Origin, sp.AIModel, sp.AIDraftedAt)
	}
	if len(sp.Tasks) != 1 || sp.Tasks[0].Slug != "prune" || len(sp.Tasks[0].ActiveMonths) != 3 {
		t.Errorf("tasks not stored: %+v", sp.Tasks)
	}
	if len(sp.DormantMonths) != 4 || sp.DormantMonths[0] != time.November {
		t.Errorf("dormant months = %v", sp.DormantMonths)
	}
}

func TestCreateSpeciesFillsBlankIntervalsFromTheModel(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("base_interval_days", "")
	form.Set("min_interval_days", "")
	form.Set("max_interval_days", "")
	assertStatus(t, doPOST(t, mux, "/species/new", form), http.StatusSeeOther)

	sp := db.species["hoya-carnosa"]
	want := catalog.ModelledIntervalDays(0.7, 0.5, domain.Peat)
	lo, hi := species.IntervalBounds(want)
	if sp.BaseIntervalDays != want || sp.MinIntervalDays != lo || sp.MaxIntervalDays != hi {
		t.Errorf("intervals = %d/%d/%d, want %d/%d/%d", sp.BaseIntervalDays, sp.MinIntervalDays, sp.MaxIntervalDays, want, lo, hi)
	}
}

var hiddenInput = regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`)

// bookkeeping returns the hidden fields a page carries for the server's own use
// (what was rendered, provenance), leaving the per-task markers out so a test
// can choose which task rows it submits.
func bookkeeping(rec *httptest.ResponseRecorder) url.Values {
	v := url.Values{}
	for _, m := range hiddenInput.FindAllStringSubmatch(html(rec), -1) {
		if !strings.HasPrefix(m[1], "tasks[") {
			v.Set(m[1], m[2])
		}
	}
	return v
}

// TestFixedDraftIsSavedAsAI drives the one path that still has a form: a draft
// whose problems survived the correction round. The page's own hidden fields go
// back with the person's fixed values, and the species is stored as AI-drafted.
func TestFixedDraftIsSavedAsAI(t *testing.T) {
	res := draftedResult(species.ConfidenceHigh)
	res.Species.Kc = 3.5
	res.Problems = catalog.Validate(res.Species)
	d := &fakeDrafter{res: res}
	_, db, mux := speciesUI(t, d)
	review := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
	assertStatus(t, review, http.StatusOK)

	form := manualSpeciesForm()
	form.Set("scientific_name", "Hoya carnosa")
	for _, m := range hiddenInput.FindAllStringSubmatch(html(review), -1) {
		form.Set(m[1], html2(m[2]))
	}
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusSeeOther)

	sp := db.species["hoya-carnosa"]
	if sp.Origin != domain.OriginAI || sp.AIModel != "gpt-6-astra" || sp.AIDraftedAt == nil || !sp.AIDraftedAt.Equal(testNow) {
		t.Errorf("provenance = %q/%q/%v, want ai/gpt-6-astra/%v", sp.Origin, sp.AIModel, sp.AIDraftedAt, testNow)
	}
}

func TestDraftOfAnExistingSpeciesChangesNothing(t *testing.T) {
	res := draftedResult(species.ConfidenceHigh)
	res.Species.Slug = "monstera-deliciosa" // the fixture species
	res.Species.CommonName = "Impostor"
	d := &fakeDrafter{res: res}
	_, db, mux := speciesUI(t, d)

	rec := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "You already have", "Swiss cheese plant", "nothing was added or changed",
		`href="/plants/new?species=monstera-deliciosa"`, `href="/species/monstera-deliciosa/edit"`)
	if got := db.species["monstera-deliciosa"].CommonName; got != "Swiss cheese plant" {
		t.Errorf("an existing species was overwritten: %q", got)
	}
}

// failingCreate is a catalog whose inserts fail, to prove a storage error is
// reported as one and not blamed on the drafting service.
type failingCreate struct{ memSpecies }

func (failingCreate) Create(context.Context, domain.Species) error {
	return errors.New("connection reset by peer")
}

func TestDraftThatCannotBeSavedSaysSo(t *testing.T) {
	d := &fakeDrafter{res: draftedResult(species.ConfidenceHigh)}
	_, _, mux := testUI(t, func(db *memDB, s *Server) {
		s.Drafter = d
		s.Species = failingCreate{memSpecies{db}}
	})
	rec := waitForDraft(t, mux, location(t, startDraft(t, mux, askForm())))
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "drafted but could not be saved", "Fill it in by hand instead")
	assertNotContains(t, rec, "connection reset", "could not be reached")
}

func TestPlantFormNotesASpeciesSavedWithoutReview(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	db.addSpecies(draftedResult(species.ConfidenceHigh).Species)

	rec := doGET(t, mux, "/plants/new?species=hoya-carnosa&drafted=1")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "saved without review", "Wax plant", "gpt-6-astra",
		`href="/species/hoya-carnosa/edit"`, `value="hoya-carnosa" selected`)

	// Only when arriving from a draft, and only for a species an AI actually wrote.
	assertNotContains(t, doGET(t, mux, "/plants/new?species=hoya-carnosa"), "saved without review")
	assertNotContains(t, doGET(t, mux, "/plants/new?species=monstera-deliciosa&drafted=1"), "saved without review")
}

// html2 undoes the one escaping html/template applies to the values we round-trip.
func html2(s string) string {
	return strings.NewReplacer("&#43;", "+", "&amp;", "&", "&#34;", `"`).Replace(s)
}

func TestProvenanceCannotBeForged(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"claims seed", func(v url.Values) { v.Set("origin", "seed") }},
		{"claims ai without a model", func(v url.Values) { v.Set("origin", "ai"); v.Set("ai_drafted_at", "2026-03-03T10:00:00Z") }},
		{"claims ai without a timestamp", func(v url.Values) { v.Set("origin", "ai"); v.Set("ai_model", "gpt-6-astra") }},
		{"claims ai with a garbled timestamp", func(v url.Values) {
			v.Set("origin", "ai")
			v.Set("ai_model", "gpt-6-astra")
			v.Set("ai_drafted_at", "yesterday")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, db, mux := speciesUI(t, nil)
			form := manualSpeciesForm()
			tt.mutate(form)
			assertStatus(t, doPOST(t, mux, "/species/new", form), http.StatusSeeOther)
			if got := db.species["hoya-carnosa"].Origin; got != domain.OriginManual {
				t.Errorf("origin = %q, want manual", got)
			}
		})
	}
}

func TestCreateSpeciesReportsErrorsBesideTheirInputs(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("kc", "9")
	form.Set("common_name", "")
	// Row 0 is left blank, so the bad task sits at row 1: the error must land there.
	for _, k := range []string{"slug", "kind", "label", "interval_days", "active_months"} {
		form.Del("tasks[0]." + k)
	}
	form.Set("tasks[1].slug", "Bad_Slug")
	form.Set("tasks[1].kind", "misting")
	form.Set("tasks[1].label", "Mist")
	form.Set("tasks[1].interval_days", "7")

	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec,
		"kc 9 out of range [0.1, 1.5]",
		"common_name is required",
		"must be kebab-case",
		`kind &#34;misting&#34; is not in the care vocabulary`,
	)
	if _, stored := db.species["hoya-carnosa"]; stored {
		t.Error("an invalid species was stored")
	}
	// Each message appears once, next to the right row.
	body := html(rec)
	row1 := body[strings.Index(body, `name="tasks[1].slug"`):]
	if !strings.Contains(row1[:strings.Index(row1, `name="tasks[2].slug"`)], "must be kebab-case") {
		t.Errorf("the slug error is not attached to row 1")
	}
	// What was typed is shown back, not lost.
	assertContains(t, rec, `value="Bad_Slug"`, `value="9"`)
}

func TestCreateSpeciesRejectsUnreadableNumbers(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("kc", "lots")
	form.Set("dormant_months", "winter")
	form.Set("tasks[0].interval_days", "often")
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "Kc must be a number", "is not a month number", "Interval must be a whole number of days")
	if len(db.species) != 1 {
		t.Error("an unreadable form was stored")
	}
}

func TestCreateSpeciesRejectsAnIntervalTheModelContradicts(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	base := catalog.ModelledIntervalDays(0.7, 0.5, domain.Peat)
	form.Set("base_interval_days", strconv.Itoa(base*4))
	form.Set("max_interval_days", strconv.Itoa(base*20))
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "away from the", "model produces at ET0=3mm/day")
	if len(db.species) != 1 {
		t.Error("a physics-inconsistent species was stored")
	}
}

func TestSlugCollisionOffersTheExistingSpecies(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("scientific_name", "Monstera deliciosa") // the fixture species' slug
	form.Set("common_name", "Impostor")
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusConflict)
	assertContains(t, rec, "already a species with this slug", "Swiss cheese plant",
		"/plants/new?species=monstera-deliciosa", "/species/monstera-deliciosa/edit")
	if got := db.species["monstera-deliciosa"].CommonName; got != "Swiss cheese plant" {
		t.Errorf("collision overwrote the existing species: %q", got)
	}
}

func TestChangingConstantsRecomputesUntouchedIntervalsBeforeSaving(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	sp := fixtureSpecies() // monstera: kc 0.7, intervals deliberately not the modelled ones
	sp.BaseIntervalDays, sp.MinIntervalDays, sp.MaxIntervalDays = 6, 1, 21
	db.addSpecies(sp)

	edit := doGET(t, mux, "/species/monstera-deliciosa/edit")
	assertStatus(t, edit, http.StatusOK)
	form := bookkeeping(edit)
	for k, v := range map[string]string{
		"common_name": "Swiss cheese plant", "scientific_name": "Monstera deliciosa",
		"kc": "0.25", "mad": "0.8", "substrate": "cactus", // a very different water model
		"base_interval_days": "6", "min_interval_days": "1", "max_interval_days": "21", // left as rendered
		"dormancy_factor": "1", "min_temp_c": "10",
	} {
		form.Set(k, v)
	}

	rec := doPOST(t, mux, "/species/monstera-deliciosa/edit", form)
	assertStatus(t, rec, http.StatusOK) // shown again, not saved
	assertContains(t, rec, "recomputed for your changes", "Check them and save again")
	if got := db.species["monstera-deliciosa"]; got.Kc != 0.7 {
		t.Fatalf("recomputed intervals were saved without being seen: kc = %v", got.Kc)
	}
	want := catalog.ModelledIntervalDays(0.25, 0.8, domain.Cactus)
	assertContains(t, rec, `name="base_interval_days" type="number" min="1" value="`+strconv.Itoa(want)+`"`)

	// Submitting what was just shown now saves.
	again := bookkeeping(rec)
	lo, hi := species.IntervalBounds(want)
	for k, v := range map[string]string{
		"common_name": "Swiss cheese plant", "scientific_name": "Monstera deliciosa",
		"kc": "0.25", "mad": "0.8", "substrate": "cactus", "dormancy_factor": "1", "min_temp_c": "10",
		"base_interval_days": strconv.Itoa(want), "min_interval_days": strconv.Itoa(lo), "max_interval_days": strconv.Itoa(hi),
	} {
		again.Set(k, v)
	}
	assertStatus(t, doPOST(t, mux, "/species/monstera-deliciosa/edit", again), http.StatusSeeOther)
	if got := db.species["monstera-deliciosa"]; got.Kc != 0.25 || got.BaseIntervalDays != want {
		t.Errorf("after confirming: kc=%v base=%d, want 0.25/%d", got.Kc, got.BaseIntervalDays, want)
	}
}

func TestIntervalsThePersonSetAreKeptWhenConstantsChange(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	db.addSpecies(fixtureSpecies())
	base := catalog.ModelledIntervalDays(0.4, 0.7, domain.Cactus)
	lo, hi := species.IntervalBounds(base)
	form := url.Values{
		"common_name": {"Swiss cheese plant"}, "scientific_name": {"Monstera deliciosa"},
		"kc": {"0.4"}, "mad": {"0.7"}, "substrate": {"cactus"}, "dormancy_factor": {"1"}, "min_temp_c": {"10"},
		"rendered_from": {"0.7|0.5|peat"}, "rendered_base": {"7"}, "rendered_min": {"3"}, "rendered_max": {"21"},
		// The person chose these deliberately, so they are theirs.
		"base_interval_days": {strconv.Itoa(base)}, "min_interval_days": {strconv.Itoa(lo + 0)}, "max_interval_days": {strconv.Itoa(hi + 1)},
	}
	assertStatus(t, doPOST(t, mux, "/species/monstera-deliciosa/edit", form), http.StatusSeeOther)
	if got := db.species["monstera-deliciosa"]; got.MaxIntervalDays != hi+1 {
		t.Errorf("max interval = %d, want the person's %d", got.MaxIntervalDays, hi+1)
	}
}

func TestEditSpeciesScreen(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	rec := doGET(t, mux, "/species/monstera-deliciosa/edit")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Edit Swiss cheese plant", "readonly", "permanent identity",
		`name="tasks[0].existing" value="1"`, "Remove this task", "Retired")
	assertNoExternalAssets(t, rec)

	form := manualSpeciesForm()
	form.Set("slug", "hijacked-slug") // identity comes from the path
	form.Set("scientific_name", "Monstera deliciosa")
	form.Set("common_name", "Renamed")
	form.Set("retired", "1")
	form.Set("origin", "ai")
	form.Set("ai_model", "forged")
	// Keep prune (existing), drop nothing else, add one new task in a blank row.
	form.Set("tasks[0].existing", "1")
	form.Set("tasks[0].slug", "prune")
	form.Set("tasks[1].slug", "pinch-tips")
	form.Set("tasks[1].kind", "pinch")
	form.Set("tasks[1].label", "Pinch tips")
	form.Set("tasks[1].interval_days", "21")
	assertStatus(t, doPOST(t, mux, "/species/monstera-deliciosa/edit", form), http.StatusSeeOther)

	got := db.species["monstera-deliciosa"]
	if _, moved := db.species["hijacked-slug"]; moved {
		t.Error("the form changed the species' slug")
	}
	if got.CommonName != "Renamed" || !got.Retired {
		t.Errorf("edit not applied: %+v", got)
	}
	if got.Origin == domain.OriginAI || got.AIModel == "forged" {
		t.Errorf("an edit rewrote provenance: %q %q", got.Origin, got.AIModel)
	}
	if len(got.Tasks) != 2 || got.Tasks[1].Slug != "pinch-tips" {
		t.Errorf("tasks = %+v, want prune + pinch-tips", got.Tasks)
	}
}

func TestEditCanRemoveAnExistingTask(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("scientific_name", "Monstera deliciosa")
	form.Set("tasks[0].existing", "1")
	form.Set("tasks[0].remove", "1")
	assertStatus(t, doPOST(t, mux, "/species/monstera-deliciosa/edit", form), http.StatusSeeOther)
	if n := len(db.species["monstera-deliciosa"].Tasks); n != 0 {
		t.Errorf("tasks = %d after removing the only one, want 0", n)
	}
}

func TestEditMissingSpeciesIs404(t *testing.T) {
	_, _, mux := speciesUI(t, nil)
	assertStatus(t, doGET(t, mux, "/species/nope/edit"), http.StatusNotFound)
	assertStatus(t, doPOST(t, mux, "/species/nope/edit", manualSpeciesForm()), http.StatusNotFound)
}

func TestSpeciesListShowsOrigins(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	ai := draftedResult(species.ConfidenceHigh).Species
	db.addSpecies(ai)
	old := fixtureSpecies()
	old.Slug, old.CommonName, old.Retired, old.Origin = "old-plant", "Old plant", true, domain.OriginManual
	db.addSpecies(old)

	rec := doGET(t, mux, "/species")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec, "Drafted by AI", "Added by hand", "Retired", "/species/hoya-carnosa/edit", "3 care tasks")
	assertContains(t, doGET(t, mux, "/"), `href="/species"`)
}

func TestPlantFormPreselectsTheNewSpecies(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	lav := fixtureSpecies()
	lav.Slug, lav.CommonName = "lavandula-angustifolia", "Lavender"
	db.addSpecies(lav)

	rec := doGET(t, mux, "/plants/new?species=lavandula-angustifolia")
	assertStatus(t, rec, http.StatusOK)
	// The species is chosen; where the plant lives is still the plant's own question.
	assertContains(t, rec, `value="lavandula-angustifolia" selected`, `value="indoor" checked`, "Add a species")

	// An unknown or retired slug is ignored, not an error.
	assertStatus(t, doGET(t, mux, "/plants/new?species=nope"), http.StatusOK)
}

func TestSpeciesTextIsEscaped(t *testing.T) {
	_, _, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("common_name", `<script>alert(1)</script>`)
	form.Set("kc", "9") // force a re-render that echoes the input
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertNotContains(t, rec, "<script>alert(1)</script>")
	assertContains(t, rec, "&lt;script&gt;")
}

func TestParseMonths(t *testing.T) {
	tests := []struct {
		in      string
		want    []time.Month
		wantErr bool
	}{
		{"", nil, false},
		{"  ", nil, false},
		{"3", []time.Month{3}, false},
		{"11, 12, 1, 2", []time.Month{11, 12, 1, 2}, false},
		{"3;4 5", []time.Month{3, 4, 5}, false},
		{"3,,4", []time.Month{3, 4}, false},
		{"march", nil, true},
		{"3.5", nil, true},
	}
	for _, tt := range tests {
		got, err := parseMonths(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseMonths(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("parseMonths(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("parseMonths(%q) = %v, want %v", tt.in, got, tt.want)
			}
		}
	}
}

func TestRateLimiterRefills(t *testing.T) {
	l := newRateLimiter(2)
	now := testNow
	first, second := l.allow(now), l.allow(now)
	if !first || !second {
		t.Fatal("the first two should be allowed")
	}
	if l.allow(now) {
		t.Fatal("the third within the hour should be refused")
	}
	if !l.allow(now.Add(31 * time.Minute)) {
		t.Fatal("half an hour refills one token at 2/hour")
	}
	if l.allow(now.Add(31 * time.Minute)) {
		t.Fatal("but not two")
	}
	later := now.Add(10 * time.Hour)
	granted := 0
	for granted < 10 && l.allow(later) {
		granted++
	}
	if granted != 2 {
		t.Fatalf("a long gap should refill to capacity (2), not beyond; granted %d", granted)
	}
	if l.allow(now) {
		t.Fatal("time going backwards must not mint tokens")
	}
}

func TestDraftJobsExpire(t *testing.T) {
	store := newDraftStore()
	run := func(context.Context) draftOutcome { return draftOutcome{State: draftFailed, Err: errors.New("x")} }
	old, _ := store.start(testNow, species.Request{Name: "a"}, run)
	if _, ok := store.get(old); !ok {
		t.Fatal("a fresh job should be findable")
	}
	if _, err := store.start(testNow.Add(draftTTL+time.Minute), species.Request{Name: "b"}, run); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get(old); ok {
		t.Error("a job older than the TTL should have been dropped")
	}
}

func TestTaskOnlyInIsSavedFromTheForm(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("tasks[1].slug", "mulch-bed")
	form.Set("tasks[1].kind", "mulch")
	form.Set("tasks[1].label", "Mulch the bed")
	form.Set("tasks[1].interval_days", "365")
	form.Set("tasks[1].only_in", "outdoor")
	assertStatus(t, doPOST(t, mux, "/species/new", form), http.StatusSeeOther)

	tasks := db.species["hoya-carnosa"].Tasks
	if len(tasks) != 2 || tasks[0].OnlyIn != "" || tasks[1].OnlyIn != domain.Outdoor {
		t.Fatalf("tasks = %+v, want prune anywhere and mulch-bed outdoors only", tasks)
	}

	edit := doGET(t, mux, "/species/hoya-carnosa/edit")
	assertContains(t, edit, `<option value="outdoor" selected>outdoors only</option>`)
}

func TestTaskOnlyInRejectsAnUnknownPlacement(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	form := manualSpeciesForm()
	form.Set("tasks[0].only_in", "greenhouse")
	rec := doPOST(t, mux, "/species/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec, `only_in &#34;greenhouse&#34; not one of`)
	if _, stored := db.species["hoya-carnosa"]; stored {
		t.Error("an invalid only_in was stored")
	}
}

// The controls on a plant's page and its schedule come from the same rule, so an
// indoor plant is never offered an outdoor-only task and an outdoor plant is.
func TestPlantPageOnlyOffersTasksForWhereThePlantIs(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	sp := fixtureSpecies()
	sp.Tasks = append(sp.Tasks, domain.SpeciesTask{Slug: "mulch-bed", Kind: domain.Mulch, Label: "Mulch the bed", IntervalDays: 365, OnlyIn: domain.Outdoor})
	db.addSpecies(sp)

	indoor := mustCreate(t, db, domain.Plant{Name: "Sill", SpeciesSlug: sp.Slug, Location: domain.Indoor, PotDiameterCM: 18, FExposure: 1, Active: true})
	outdoor := mustCreate(t, db, domain.Plant{Name: "Bed", SpeciesSlug: sp.Slug, Location: domain.Outdoor, PotDiameterCM: 18, FExposure: 1, FRain: 0.9, Active: true})

	assertNotContains(t, doGET(t, mux, "/plants/"+indoor.ID.String()), "Mulch the bed")
	assertContains(t, doGET(t, mux, "/plants/"+outdoor.ID.String()), "Mulch the bed")
}

// Nothing is seeded, so the first visit to "add a plant" has an empty species
// picker. It must still render and send you to add a species, not dead-end.
func TestAddPlantWithAnEmptyCatalogPointsToAddingASpecies(t *testing.T) {
	_, db, mux := speciesUI(t, nil)
	db.mu.Lock()
	db.species = map[string]domain.Species{}
	db.mu.Unlock()

	rec := doGET(t, mux, "/plants/new")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Choose a species", `href="/species/new"`)

	assertContains(t, doGET(t, mux, "/species"), "No species yet", `href="/species/new"`)

	sub := doPOST(t, mux, "/plants/new", validPlantForm("Gerald"))
	assertStatus(t, sub, http.StatusBadRequest)
	assertContains(t, sub, "Unknown species", `href="/species/new"`)
}
