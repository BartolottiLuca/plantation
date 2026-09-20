package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
)

var testNow = time.Date(2026, time.September, 16, 12, 0, 0, 0, time.UTC)

func fixtureSpecies() domain.Species {
	return domain.Species{
		Slug:             "monstera-deliciosa",
		CommonName:       "Swiss cheese plant",
		ScientificName:   "Monstera deliciosa",
		Placement:        domain.Indoor,
		Kc:               0.7,
		Substrate:        domain.Peat,
		MAD:              0.5,
		BaseIntervalDays: 7,
		MinIntervalDays:  3,
		MaxIntervalDays:  21,
		DormancyFactor:   1,
		Tasks: []domain.SpeciesTask{
			{Slug: "prune", Kind: domain.Prune, Label: "Prune", IntervalDays: 90},
			{Slug: "fertilize", Kind: domain.Fertilize, Label: "Fertilize", IntervalDays: 30},
			{Slug: "repot", Kind: domain.Repot, Label: "Repot", IntervalDays: 730},
		},
	}
}

func testUI(t *testing.T, extra func(*memDB, *Server)) (*Server, *memDB, *http.ServeMux) {
	t.Helper()
	clock := &care.NoopClock{Instant: testNow}
	db := newMem(clock)
	db.addSpecies(fixtureSpecies())
	s := NewServer(Server{
		Plants:   db,
		Species:  memSpecies{db},
		Events:   db,
		Clock:    clock,
		Location: time.UTC,
	})
	if extra != nil {
		extra(db, s)
	}
	mux := http.NewServeMux()
	s.Register(mux)
	return s, db, mux
}

func validPlantForm(name string) url.Values {
	return url.Values{
		"name":            {name},
		"species_slug":    {"monstera-deliciosa"},
		"location":        {"indoor"},
		"place":           {"kitchen"},
		"pot_diameter_cm": {"18"},
		"f_exposure":      {"1.0"},
		"f_rain":          {"0.0"},
	}
}

func doPOST(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return doPOSTHdr(t, h, path, form, nil)
}

func doPOSTHdr(t *testing.T, h http.Handler, path string, form url.Values, hdr http.Header) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func html(rec *httptest.ResponseRecorder) string {
	return rec.Body.String()
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d\nbody:\n%s", rec.Code, want, html(rec))
	}
}

func assertContains(t *testing.T, rec *httptest.ResponseRecorder, needles ...string) {
	t.Helper()
	body := html(rec)
	for _, n := range needles {
		if !strings.Contains(body, n) {
			t.Fatalf("body missing %q\n%s", n, body)
		}
	}
}

func assertNotContains(t *testing.T, rec *httptest.ResponseRecorder, needles ...string) {
	t.Helper()
	body := html(rec)
	for _, n := range needles {
		if strings.Contains(body, n) {
			t.Fatalf("body unexpectedly contains %q\n%s", n, body)
		}
	}
}

func assertNoExternalAssets(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	body := strings.ToLower(html(rec))
	for _, n := range []string{"http://", "https://", "unpkg", "jsdelivr", "cdn."} {
		if strings.Contains(body, n) {
			t.Fatalf("rendered HTML has external asset ref %q", n)
		}
	}
}

func mustCreate(t *testing.T, db *memDB, p domain.Plant) domain.Plant {
	t.Helper()
	got, err := db.Create(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDashboardEmptyState(t *testing.T) {
	_, _, mux := testUI(t, nil)
	rec := doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec, "No plants yet", "/plants/new", "Dashboard", "Plants", "Add")
	assertNoExternalAssets(t, rec)
}

func TestDashboardGroupsOverdueTodayUpcoming(t *testing.T) {
	acquired := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, db, mux := testUI(t, nil)
	overdue := mustCreate(t, db, domain.Plant{
		Name: "Overdue Fern", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true, AcquiredAt: &acquired,
	})
	due := mustCreate(t, db, domain.Plant{
		Name: "Due Today Ivy", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true,
	})
	up := mustCreate(t, db, domain.Plant{
		Name: "Upcoming Fig", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true,
	})
	if _, err := db.Add(context.Background(), domain.CareEvent{
		PlantID: due.ID, Kind: domain.Water, DoneAt: time.Date(2026, time.September, 9, 9, 0, 0, 0, time.UTC), Source: "web",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Add(context.Background(), domain.CareEvent{
		PlantID: up.ID, Kind: domain.Water, DoneAt: time.Date(2026, time.September, 14, 9, 0, 0, 0, time.UTC), Source: "web",
	}); err != nil {
		t.Fatal(err)
	}

	rec := doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)
	body := html(rec)
	overdueAt := strings.Index(body, "Overdue Fern")
	todayAt := strings.Index(body, "Due Today Ivy")
	upAt := strings.Index(body, "Upcoming Fig")
	if overdueAt < 0 || todayAt < 0 || upAt < 0 {
		t.Fatalf("missing grouped plants\n%s", body)
	}
	if overdueAt >= todayAt || todayAt >= upAt {
		t.Fatalf("expected overdue, due today, upcoming order")
	}
	assertContains(t, rec, "Overdue", "Due today", "Coming up", overdue.ID.String())
}

func TestPlantListAndDetailNoEnvironment(t *testing.T) {
	acquired := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, db, mux := testUI(t, nil)
	p := mustCreate(t, db, domain.Plant{
		Name: "Hallway Monstera", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		Place: "hall", PotDiameterCM: 18, FExposure: 1, Active: true, AcquiredAt: &acquired,
	})

	list := doGET(t, mux, "/plants")
	assertStatus(t, list, http.StatusOK)
	assertNoIndex(t, list)
	assertContains(t, list, "Hallway Monstera", "Swiss cheese plant", "Overdue")

	rec := doGET(t, mux, "/plants/"+p.ID.String())
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Hallway Monstera", "No environment data", "base_interval",
		"Capacity", "Deficit", "Threshold", "Mean ETc", "Effective rain",
		"Deferred", "Clamped by", "Outdoor stale", "Indoor stale", "Mode")
	assertNoExternalAssets(t, rec)
}

func TestRetiredSpeciesStillUsable(t *testing.T) {
	_, db, mux := testUI(t, func(db *memDB, _ *Server) {
		sp := fixtureSpecies()
		sp.Slug = "retired-fern"
		sp.CommonName = "Retired fern"
		sp.ScientificName = "Fernus retired"
		sp.Retired = true
		db.addSpecies(sp)
	})
	p := mustCreate(t, db, domain.Plant{
		Name: "Old Fern", SpeciesSlug: "retired-fern", Location: domain.Indoor,
		PotDiameterCM: 16, FExposure: 1, Active: true,
	})

	neu := doGET(t, mux, "/plants/new")
	assertStatus(t, neu, http.StatusOK)
	assertNotContains(t, neu, "retired-fern")

	detail := doGET(t, mux, "/plants/"+p.ID.String())
	assertStatus(t, detail, http.StatusOK)
	assertContains(t, detail, "Old Fern", "retired", "Retired fern")

	edit := doGET(t, mux, "/plants/"+p.ID.String()+"/edit")
	assertStatus(t, edit, http.StatusOK)
	assertContains(t, edit, "retired-fern", "Retired fern")
}

func TestActionQueryPreselectsWater(t *testing.T) {
	_, db, mux := testUI(t, nil)
	p := mustCreate(t, db, domain.Plant{
		Name: "Link Plant", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true,
	})
	rec := doGET(t, mux, "/plants/"+p.ID.String()+"?action=water")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, `data-preselected="water"`, "preselected")
}

func TestCreateEditDeletePlant(t *testing.T) {
	_, _, mux := testUI(t, nil)

	bad := doPOST(t, mux, "/plants/new", url.Values{"name": {""}, "location": {"indoor"}})
	assertStatus(t, bad, http.StatusBadRequest)
	assertContains(t, bad, "Name is required")

	created := doPOST(t, mux, "/plants/new", validPlantForm("Basil"))
	assertStatus(t, created, http.StatusSeeOther)
	loc := created.Header().Get("Location")
	if !strings.HasPrefix(loc, "/plants/") {
		t.Fatalf("create Location = %q", loc)
	}

	detail := doGET(t, mux, loc)
	assertStatus(t, detail, http.StatusOK)
	assertContains(t, detail, "Basil")

	editGET := doGET(t, mux, loc+"/edit")
	assertStatus(t, editGET, http.StatusOK)
	assertContains(t, editGET, "Basil", "Advanced", "Sheltered", "Indoor or under eaves")

	form := validPlantForm("Basil II")
	edited := doPOST(t, mux, loc+"/edit", form)
	assertStatus(t, edited, http.StatusSeeOther)
	assertContains(t, doGET(t, mux, loc), "Basil II")

	del := doPOST(t, mux, loc+"/delete", nil)
	assertStatus(t, del, http.StatusSeeOther)
	list := doGET(t, mux, "/plants")
	assertNotContains(t, list, "Basil II")
	gone := doGET(t, mux, loc)
	assertStatus(t, gone, http.StatusNotFound)
}

func TestCreateOutdoorInGroundPlant(t *testing.T) {
	// fixtureSpecies() already declares a repot task; this test's whole point
	// is proving an in-ground plant never gets it scheduled (SPEC §7.1).
	_, db, mux := testUI(t, nil)
	form := validPlantForm("Lavender")
	form.Set("location", "outdoor")
	form.Set("container", "ground")
	form.Del("pot_diameter_cm")
	form.Set("f_rain", "0.9")
	form.Set("f_exposure", "1.3")
	created := doPOST(t, mux, "/plants/new", form)
	assertStatus(t, created, http.StatusSeeOther)
	rows, err := db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var p domain.Plant
	for _, row := range rows {
		if row.Plant.Name == "Lavender" {
			p = row.Plant
		}
	}
	if !p.InGround || p.PotDiameterCM != 0 || p.Location != domain.Outdoor {
		t.Fatalf("plant = %+v", p)
	}
	loc := created.Header().Get("Location")
	detail := doGET(t, mux, loc)
	assertContains(t, detail, "Lavender", "in the ground", "90.0 mm")
	assertNotContains(t, detail, "Repot")
	edit := doGET(t, mux, loc+"/edit")
	assertContains(t, edit, `value="ground" checked`)
}

func TestInGroundRejectedIndoors(t *testing.T) {
	_, _, mux := testUI(t, nil)
	form := validPlantForm("Nope")
	form.Set("container", "ground")
	rec := doPOST(t, mux, "/plants/new", form)
	assertStatus(t, rec, http.StatusBadRequest)
	assertContains(t, rec, "only for outdoor")
}

func TestCarePostHTMXFragmentAndVoid(t *testing.T) {
	acquired := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, db, mux := testUI(t, nil)
	p := mustCreate(t, db, domain.Plant{
		Name: "Water Me", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true, AcquiredAt: &acquired,
	})

	full := doPOST(t, mux, "/plants/"+p.ID.String()+"/care/water", nil)
	assertStatus(t, full, http.StatusSeeOther)
	assertNoIndex(t, full)

	detail := doGET(t, mux, "/plants/"+p.ID.String())
	assertContains(t, detail, "Water · 2026-09-16 · web", "Undo")

	hx := doPOSTHdr(t, mux, "/plants/"+p.ID.String()+"/care/water", nil, http.Header{"HX-Request": {"true"}})
	assertStatus(t, hx, http.StatusOK)
	assertNoIndex(t, hx)
	if strings.Contains(html(hx), "<html") || strings.TrimSpace(html(hx)) != "" {
		t.Fatalf("HTMX care POST should return an empty fragment, got %q", html(hx))
	}

	events, err := db.SinceDate(context.Background(), p.ID, domain.Water, historyFrom)
	if err != nil || len(events) < 2 {
		t.Fatalf("events = %+v err=%v", events, err)
	}
	voidID := events[len(events)-1].ID
	voidHX := doPOSTHdr(t, mux, "/events/"+itoa(voidID)+"/void", url.Values{"plant_id": {p.ID.String()}}, http.Header{"HX-Request": {"true"}})
	assertStatus(t, voidHX, http.StatusOK)
	if strings.TrimSpace(html(voidHX)) != "" {
		t.Fatalf("void fragment = %q", html(voidHX))
	}

	voidFull := doPOST(t, mux, "/events/"+itoa(events[0].ID)+"/void", url.Values{"plant_id": {p.ID.String()}})
	assertStatus(t, voidFull, http.StatusSeeOther)

	after := doGET(t, mux, "/plants/"+p.ID.String())
	assertNotContains(t, after, "Undo")
}

func TestMutationsArePOSTOnly(t *testing.T) {
	_, db, mux := testUI(t, nil)
	p := mustCreate(t, db, domain.Plant{
		Name: "Stay", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true,
	})
	ev, err := db.Add(context.Background(), domain.CareEvent{
		PlantID: p.ID, Kind: domain.Water, DoneAt: testNow, Source: "web",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/plants/" + p.ID.String() + "/delete",
		"/plants/" + p.ID.String() + "/care/water",
		"/events/" + itoa(ev.ID) + "/void",
	} {
		rec := doGET(t, mux, path)
		if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
			t.Fatalf("GET %s mutated or rendered success, status %d", path, rec.Code)
		}
	}
	got, err := db.Get(context.Background(), p.ID)
	if err != nil || !got.Active {
		t.Fatalf("GET delete must not deactivate plant: %+v %v", got, err)
	}
	latest, err := db.LatestByKind(context.Background(), p.ID)
	if err != nil || len(latest) != 1 {
		t.Fatalf("GET care/void must not change events: %+v %v", latest, err)
	}
}

func TestWriteTokenRequiredOnPOST(t *testing.T) {
	clock := &care.NoopClock{Instant: testNow}
	db := newMem(clock)
	db.addSpecies(fixtureSpecies())
	s := NewServer(Server{
		Plants:     db,
		Species:    memSpecies{db},
		Events:     db,
		Clock:      clock,
		Location:   time.UTC,
		WriteToken: "secret",
	})
	mux := http.NewServeMux()
	s.Register(mux)

	open := doGET(t, mux, "/")
	assertStatus(t, open, http.StatusOK)

	denied := doPOST(t, mux, "/plants/new", validPlantForm("Nope"))
	assertStatus(t, denied, http.StatusUnauthorized)
	assertNoIndex(t, denied)

	ok := doPOSTHdr(t, mux, "/plants/new", validPlantForm("Authed"), http.Header{"Authorization": {"Bearer secret"}})
	assertStatus(t, ok, http.StatusSeeOther)
}

func TestDigestFailedBanner(t *testing.T) {
	_, _, mux := testUI(t, func(_ *memDB, s *Server) {
		s.LastDigestFailed = func(context.Context) bool { return true }
	})
	rec := doGET(t, mux, "/")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Last digest failed")
}

func TestRoomPickerHiddenWhenOnlyOneRoom(t *testing.T) {
	_, _, mux := testUI(t, func(_ *memDB, s *Server) {
		s.Rooms = roomList{{RoomID: "living", Name: "Living room", TempC: 21, HumidityPct: 50, ObservedAt: testNow}}
	})
	rec := doGET(t, mux, "/plants/new")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, `type="hidden"`, `name="tado_room_id"`, `value="living"`)
	assertNotContains(t, rec, "Tado room", "Living room")
}

func TestRoomPickerWhenMultipleRooms(t *testing.T) {
	_, _, mux := testUI(t, func(_ *memDB, s *Server) {
		s.Rooms = roomList{
			{RoomID: "living", Name: "Living room", TempC: 21, HumidityPct: 50, ObservedAt: testNow},
			{RoomID: "hall", Name: "Hall", TempC: 19, HumidityPct: 48, ObservedAt: testNow},
		}
	})
	rec := doGET(t, mux, "/plants/new")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "<select", "Living room", "Hall", "id=\"tado_room_id\"")
}

func TestCreateAssignsSoleTadoRoom(t *testing.T) {
	_, db, mux := testUI(t, func(_ *memDB, s *Server) {
		s.Rooms = roomList{{RoomID: "living", Name: "Living room", TempC: 21, HumidityPct: 50, ObservedAt: testNow}}
	})
	created := doPOST(t, mux, "/plants/new", validPlantForm("Basil"))
	assertStatus(t, created, http.StatusSeeOther)
	rows, err := db.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Plant.TadoRoomID == nil || *rows[0].Plant.TadoRoomID != "living" {
		t.Fatalf("plant = %+v", rows)
	}
}

func TestTadoRoomTextFieldWithoutLister(t *testing.T) {
	_, _, mux := testUI(t, nil)
	rec := doGET(t, mux, "/plants/new")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, `name="tado_room_id"`)
	if strings.Contains(html(rec), "<select") && strings.Contains(html(rec), "tado_room_id") {
		// species picker is a select; tado should be an input when no rooms
		if !strings.Contains(html(rec), `<input id="tado_room_id"`) {
			t.Fatalf("expected tado text input\n%s", html(rec))
		}
	}
}

func TestStaticAssetsAreLocalAndNoIndex(t *testing.T) {
	_, _, mux := testUI(t, nil)
	css := doGET(t, mux, "/static/style.css")
	assertStatus(t, css, http.StatusOK)
	assertNoIndex(t, css)
	assertContains(t, css, "color-scheme: dark")
	js := doGET(t, mux, "/static/htmx.min.js")
	assertStatus(t, js, http.StatusOK)
	assertContains(t, js, "htmx")
	page := doGET(t, mux, "/plants/new")
	assertContains(t, page, `src="/static/htmx.min.js?v=dev"`, `href="/static/style.css?v=dev"`, `name="color-scheme"`, "In the ground")
	assertNoExternalAssets(t, page)
}

func TestRegisterDoesNotShadowHealth(t *testing.T) {
	_, _, mux := testUI(t, nil)
	RegisterHealth(mux, nil)
	rec := doGET(t, mux, "/healthz")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
}

func TestUnknownPlantIs404(t *testing.T) {
	_, _, mux := testUI(t, nil)
	rec := doGET(t, mux, "/plants/"+uuid.New().String())
	assertStatus(t, rec, http.StatusNotFound)
	assertNoIndex(t, rec)
}

func TestInvalidCareKind(t *testing.T) {
	_, db, mux := testUI(t, nil)
	p := mustCreate(t, db, domain.Plant{
		Name: "X", SpeciesSlug: "monstera-deliciosa", Location: domain.Indoor,
		PotDiameterCM: 18, FExposure: 1, Active: true,
	})
	rec := doPOST(t, mux, "/plants/"+p.ID.String()+"/care/dance", nil)
	assertStatus(t, rec, http.StatusBadRequest)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
