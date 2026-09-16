package tado

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
)

const testAccess = "test-access-token"

func TestSamplerSatisfiesIndoorClimateProvider(t *testing.T) {
	var _ climate.IndoorClimateProvider = (*Sampler)(nil)
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	return body
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func instantSleep(_ context.Context, _ time.Duration) error { return nil }

func linkedToken(clk Clock, homeID *string) store.TadoToken {
	now := clk.Now()
	return store.TadoToken{
		AccessToken:       strp(testAccess),
		AccessExpiresAt:   timep(now.Add(24 * time.Hour)),
		RefreshToken:      strp("test-refresh"),
		RefreshObtainedAt: timep(now),
		HomeID:            homeID,
		State:             store.TadoLinked,
	}
}

type fakePlants struct {
	rows []store.PlantWithSpecies
	err  error
}

func (f *fakePlants) List(context.Context) ([]store.PlantWithSpecies, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func plantIn(room string, active bool) store.PlantWithSpecies {
	id := room
	return store.PlantWithSpecies{
		Plant: domain.Plant{Name: "plant-" + room, Active: active, TadoRoomID: &id},
	}
}

type recordingClimate struct {
	mu    sync.Mutex
	calls int
	rows  []store.ClimateSample
	err   error
}

func (r *recordingClimate) AddSamples(_ context.Context, samples []store.ClimateSample) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return r.err
	}
	for _, s := range samples {
		dup := false
		for _, existing := range r.rows {
			if existing.RoomID == s.RoomID && existing.ObservedAt.Equal(s.ObservedAt) {
				dup = true
				break
			}
		}
		if !dup {
			r.rows = append(r.rows, s)
		}
	}
	return nil
}

func (r *recordingClimate) snapshot() (calls int, rows []store.ClimateSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows = append([]store.ClimateSample(nil), r.rows...)
	return r.calls, rows
}

func requireBearer(t *testing.T, r *http.Request, want string) {
	t.Helper()
	got := r.Header.Get("Authorization")
	if got != "Bearer "+want {
		t.Errorf("Authorization header mismatch")
	}
}

// startTadoAPI serves /api/v2/me and /homes/{id}/rooms from one httptest server.
func startTadoAPI(t *testing.T, meBody, roomsBody []byte, meStatus, roomsStatus int) *httptest.Server {
	t.Helper()
	if meStatus == 0 {
		meStatus = http.StatusOK
	}
	if roomsStatus == 0 {
		roomsStatus = http.StatusOK
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/me", func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r, testAccess)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(meStatus)
		_, _ = w.Write(meBody)
	})
	mux.HandleFunc("/homes/", func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r, testAccess)
		if !strings.HasSuffix(r.URL.Path, "/rooms") {
			t.Errorf("unexpected hops path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(roomsStatus)
		_, _ = w.Write(roomsBody)
	})
	return httptest.NewServer(mux)
}

func newTestSampler(t *testing.T, repo *fakeRepo, srv *httptest.Server, climate ClimateStore, plants PlantRooms) (*Sampler, *fakeClock) {
	t.Helper()
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	c := NewClient(repo, WithClock(clk))
	c.baseURL = "http://127.0.0.1:1" // refresh must not be reached unless a test overrides
	s := NewSampler(c, climate, plants,
		WithMeURL(srv.URL+"/api/v2/me"),
		WithHopsBaseURL(srv.URL),
		WithSamplerClock(clk),
	)
	s.sleep = instantSleep
	return s, clk
}

func TestRooms_RecordedFixturesDoNotError(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	cases := []struct {
		name    string
		fixture string
		wantIDs []string
		wantLog string
	}{
		{name: "two rooms", fixture: "rooms.json", wantIDs: []string{"1", "2"}},
		{name: "room missing humidity", fixture: "rooms_missing_humidity.json", wantIDs: []string{"1"}, wantLog: "no usable sensor reading"},
		{name: "unexpected top-level shape", fixture: "rooms_unexpected.json", wantLog: "unexpected shape"},
		{name: "empty room list", fixture: "rooms_empty.json", wantLog: "rooms list empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, tc.fixture), 0, 0)
			defer srv.Close()

			repo := &fakeRepo{row: linkedToken(clk, strp("12345"))}
			s, _ := newTestSampler(t, repo, srv, nil, nil)

			got, err := s.Rooms(context.Background())
			if err != nil {
				t.Fatalf("Rooms: %v", err)
			}
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("len(Rooms) = %d, want %d (%v)", len(got), len(tc.wantIDs), got)
			}
			for i, id := range tc.wantIDs {
				if got[i].RoomID != id {
					t.Errorf("room[%d].RoomID = %q, want %q", i, got[i].RoomID, id)
				}
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("logs missing %q:\n%s", tc.wantLog, logs.String())
			}
			if strings.Contains(logs.String(), testAccess) || strings.Contains(logs.String(), "Authorization") {
				t.Errorf("logs leaked token or Authorization:\n%s", logs.String())
			}
		})
	}
}

func TestRooms_RecordedTwoRoomReadings(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	s, sclk := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)
	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "Living Room" || got[0].TempC != 21.4 || got[0].HumidityPct != 47.2 {
		t.Errorf("room 0 = %+v, want Living Room 21.4/47.2", got[0])
	}
	if got[1].Name != "Bedroom" || got[1].TempC != 19.1 || got[1].HumidityPct != 54.0 {
		t.Errorf("room 1 = %+v, want Bedroom 19.1/54.0", got[1])
	}
	if !got[0].ObservedAt.Equal(sclk.Now()) {
		t.Errorf("ObservedAt = %v, want clock now %v", got[0].ObservedAt, sclk.Now())
	}
}

func TestParseRooms_HostileShapes(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		body    string
		wantIDs []string
		wantLog string
	}{
		{
			name:    "top-level array with flat fields",
			body:    `[{"id":"kitchen","name":"Kitchen","temperature":20.5,"humidity":43}]`,
			wantIDs: []string{"kitchen"},
		},
		{
			name:    "string ids and nested humidity.percentage",
			body:    `{"rooms":[{"id":"abc","name":"Study","sensor":{"temperature":{"celsius":22},"humidity":{"percentage":50}}}]}`,
			wantIDs: []string{"abc"},
		},
		{
			name:    "not json",
			body:    `<<<not-json>>>`,
			wantLog: "not json",
		},
		{
			name:    "room is not an object",
			body:    `{"rooms":["nope"]}`,
			wantLog: "not an object",
		},
		{
			name:    "out of range temperature dropped",
			body:    `{"rooms":[{"id":"1","name":"Hot","sensor":{"temperature":{"celsius":50},"humidity":{"percentage":40}}}]}`,
			wantLog: "sensor fault",
		},
		{
			name:    "out of range humidity dropped",
			body:    `{"rooms":[{"id":"1","name":"Dry","sensor":{"temperature":{"celsius":21},"humidity":{"percentage":5}}}]}`,
			wantLog: "sensor fault",
		},
		{
			name:    "in-range edges kept",
			body:    `{"rooms":[{"id":"1","name":"Edge","temperature":0,"humidity":10},{"id":"2","name":"Edge2","temperature":45,"humidity":95}]}`,
			wantIDs: []string{"1", "2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLogs(t)
			got := parseRoomsBody([]byte(tc.body), now)
			if len(got) != len(tc.wantIDs) {
				t.Fatalf("len = %d, want %d (%v)", len(got), len(tc.wantIDs), got)
			}
			for i, id := range tc.wantIDs {
				if got[i].RoomID != id {
					t.Errorf("room[%d].RoomID = %q, want %q", i, got[i].RoomID, id)
				}
			}
			if tc.wantLog != "" && !strings.Contains(logs.String(), tc.wantLog) {
				t.Errorf("logs missing %q:\n%s", tc.wantLog, logs.String())
			}
		})
	}
}

func TestRooms_FetchesHomeIDFromMeAndCachesIt(t *testing.T) {
	var meHits, hopsHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/me", func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r, testAccess)
		meHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readTestdata(t, "me.json"))
	})
	mux.HandleFunc("/homes/", func(w http.ResponseWriter, r *http.Request) {
		requireBearer(t, r, testAccess)
		hopsHits.Add(1)
		if r.URL.Path != "/homes/12345/rooms" {
			t.Errorf("hops path = %q, want /homes/12345/rooms", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readTestdata(t, "rooms.json"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	prev := strp("previous-refresh")
	repo := &fakeRepo{row: store.TadoToken{
		AccessToken:          strp(testAccess),
		AccessExpiresAt:      timep(clk.Now().Add(10 * time.Minute)),
		RefreshToken:         strp("keep-refresh"),
		PreviousRefreshToken: prev,
		RefreshObtainedAt:    timep(clk.Now()),
		State:                store.TadoLinked,
	}}
	s, _ := newTestSampler(t, repo, srv, nil, nil)

	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if meHits.Load() != 1 || hopsHits.Load() != 1 {
		t.Fatalf("first call: me=%d hops=%d, want 1/1", meHits.Load(), hopsHits.Load())
	}

	row := repo.snapshot()
	if row.HomeID == nil || *row.HomeID != "12345" {
		t.Errorf("HomeID = %v, want 12345", row.HomeID)
	}
	if row.RefreshToken == nil || *row.RefreshToken != "keep-refresh" {
		t.Errorf("RefreshToken clobbered: %v", row.RefreshToken)
	}
	if row.PreviousRefreshToken == nil || *row.PreviousRefreshToken != "previous-refresh" {
		t.Errorf("PreviousRefreshToken clobbered: %v", row.PreviousRefreshToken)
	}
	if row.AccessToken == nil || *row.AccessToken != testAccess {
		t.Errorf("AccessToken clobbered: %v", row.AccessToken)
	}
	if row.State != store.TadoLinked {
		t.Errorf("State = %q, want linked", row.State)
	}

	if _, err := s.Rooms(context.Background()); err != nil {
		t.Fatalf("second Rooms: %v", err)
	}
	if meHits.Load() != 1 {
		t.Errorf("me called again after HomeID cached: hits=%d", meHits.Load())
	}
	if hopsHits.Load() != 2 {
		t.Errorf("hops hits = %d, want 2", hopsHits.Load())
	}
}

func TestParseHomeID_NumberAndString(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
		ok   bool
	}{
		{name: "numeric id", body: `{"homes":[{"id":12345,"name":"H"}]}`, want: "12345", ok: true},
		{name: "string id", body: `{"homes":[{"id":"abc-home"}]}`, want: "abc-home", ok: true},
		{name: "nested home object", body: `{"home":{"id":99}}`, want: "99", ok: true},
		{name: "user id must not win", body: `{"id":"user-uuid","email":"x@example.invalid"}`, ok: false},
		{name: "garbage", body: `{"foo":1}`, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseHomeID([]byte(tc.body))
			if ok != tc.ok || got != tc.want {
				t.Errorf("parseHomeID() = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSample_TwiceWithinBucketInsertsOneRow(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	clim := &recordingClimate{}
	plants := &fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true)}}
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, clim, plants)

	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample: %v", err)
	}
	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("second Sample: %v", err)
	}

	calls, rows := clim.snapshot()
	if calls != 2 {
		t.Errorf("AddSamples calls = %d, want 2", calls)
	}
	if len(rows) != 1 {
		t.Fatalf("persisted rows = %d, want 1", len(rows))
	}
	wantAt := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if rows[0].RoomID != "1" || !rows[0].ObservedAt.Equal(wantAt) {
		t.Errorf("row = %+v, want room 1 at %v", rows[0], wantAt)
	}
	if rows[0].TempC != 21.4 || rows[0].HumidityPct != 47.2 {
		t.Errorf("values = %v/%v, want 21.4/47.2", rows[0].TempC, rows[0].HumidityPct)
	}
}

func TestSample_NewBucketInsertsSecondRow(t *testing.T) {
	fixed := time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC)
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	clim := &recordingClimate{}
	plants := &fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true)}}
	repo := &fakeRepo{row: linkedToken(newFakeClock(fixed), strp("12345"))}
	s, clk := newTestSampler(t, repo, srv, clim, plants)

	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("first Sample: %v", err)
	}
	clk.mu.Lock()
	clk.now = time.Date(2026, 9, 16, 12, 31, 0, 0, time.UTC)
	clk.mu.Unlock()
	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("second Sample: %v", err)
	}
	_, rows := clim.snapshot()
	if len(rows) != 2 {
		t.Fatalf("persisted rows = %d, want 2 (new bucket)", len(rows))
	}
}

func TestSample_UnmappedAndInactiveNotPersisted(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	clim := &recordingClimate{}
	plants := &fakePlants{rows: []store.PlantWithSpecies{
		plantIn("2", false),
		{Plant: domain.Plant{Name: "no-room", Active: true}},
		plantIn("1", true),
	}}
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, clim, plants)

	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	calls, rows := clim.snapshot()
	if calls != 1 || len(rows) != 1 || rows[0].RoomID != "1" {
		t.Errorf("calls=%d rows=%+v, want one row for room 1", calls, rows)
	}
}

func TestSample_OutOfRangeDropped(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	faulty := []byte(`{"rooms":[
		{"id":"1","name":"Hot","sensor":{"temperature":{"celsius":50},"humidity":{"percentage":40}}},
		{"id":"2","name":"Ok","sensor":{"temperature":{"celsius":21},"humidity":{"percentage":45}}}
	]}`)
	srv := startTadoAPI(t, readTestdata(t, "me.json"), faulty, 0, 0)
	defer srv.Close()

	clim := &recordingClimate{}
	plants := &fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true), plantIn("2", true)}}
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, clim, plants)

	rooms, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(rooms) != 1 || rooms[0].RoomID != "2" {
		t.Fatalf("Rooms = %+v, want only room 2", rooms)
	}
	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	_, rows := clim.snapshot()
	if len(rows) != 1 || rows[0].RoomID != "2" {
		t.Errorf("persisted %+v, want only room 2", rows)
	}
}

func TestSampler_UnlinkedAndNeedsReauthDegrade(t *testing.T) {
	cases := []struct {
		name  string
		state string
	}{
		{name: "unlinked", state: store.TadoUnlinked},
		{name: "needs_reauth", state: store.TadoNeedsReauth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := atomic.Int32{}
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				hits.Add(1)
				t.Error("Tado HTTP must not be called when not linked")
			}))
			defer srv.Close()

			clim := &recordingClimate{}
			repo := &fakeRepo{row: store.TadoToken{State: tc.state, AccessToken: strp(testAccess)}}
			s, _ := newTestSampler(t, repo, srv, clim, &fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true)}})

			rooms, err := s.Rooms(context.Background())
			if err != nil {
				t.Fatalf("Rooms: %v", err)
			}
			if len(rooms) != 0 {
				t.Errorf("Rooms = %+v, want empty", rooms)
			}
			if err := s.Sample(context.Background()); err != nil {
				t.Fatalf("Sample: %v", err)
			}
			calls, rows := clim.snapshot()
			if calls != 0 || len(rows) != 0 {
				t.Errorf("Sample persisted calls=%d rows=%+v", calls, rows)
			}
			st := s.Status(context.Background())
			if st.State != tc.state {
				t.Errorf("Status.State = %q, want %q", st.State, tc.state)
			}
			if hits.Load() != 0 {
				t.Errorf("HTTP hits = %d, want 0", hits.Load())
			}
		})
	}
}

func TestSampler_RefreshNeedsReauthDegrades(t *testing.T) {
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "invalid_grant"})
	}))
	defer oauth.Close()
	hops := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("hops must not be called after needs_reauth")
	}))
	defer hops.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	repo := &fakeRepo{row: store.TadoToken{
		AccessToken:       strp("stale-access"),
		AccessExpiresAt:   timep(clk.Now().Add(time.Minute)),
		RefreshToken:      strp("dead-refresh"),
		RefreshObtainedAt: timep(clk.Now()),
		HomeID:            strp("12345"),
		State:             store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(clk))
	c.baseURL = oauth.URL
	s := NewSampler(c, &recordingClimate{}, &fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true)}},
		WithHopsBaseURL(hops.URL),
		WithMeURL(hops.URL+"/api/v2/me"),
		WithSamplerClock(clk),
	)

	rooms, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(rooms) != 0 {
		t.Errorf("Rooms = %+v, want empty", rooms)
	}
	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("Sample: %v", err)
	}
	st := s.Status(context.Background())
	if st.State != climate.StateNeedsReauth {
		t.Errorf("Status.State = %q, want needs_reauth", st.State)
	}
}

func TestRooms_RefreshesStaleTokenBeforeHops(t *testing.T) {
	var hopsAuth string
	oauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.FormValue("refresh_token") != "old-refresh" {
			t.Errorf("refresh_token mismatch")
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": "rotated-access", "refresh_token": "rotated-refresh", "expires_in": 600,
		})
	}))
	defer oauth.Close()
	hops := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hopsAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readTestdata(t, "rooms.json"))
	}))
	defer hops.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	repo := &fakeRepo{row: store.TadoToken{
		AccessToken:       strp("old-access"),
		AccessExpiresAt:   timep(clk.Now().Add(2 * time.Minute)),
		RefreshToken:      strp("old-refresh"),
		RefreshObtainedAt: timep(clk.Now()),
		HomeID:            strp("12345"),
		State:             store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(clk))
	c.baseURL = oauth.URL
	s := NewSampler(c, nil, nil,
		WithHopsBaseURL(hops.URL),
		WithMeURL(hops.URL+"/unused"),
		WithSamplerClock(clk),
	)

	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if hopsAuth != "Bearer rotated-access" {
		t.Errorf("hops Authorization = %q, want Bearer rotated-access", hopsAuth)
	}
}

func TestRooms_Retries5xxThenSucceeds(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if strings.Contains(r.URL.Path, "/me") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(readTestdata(t, "me.json"))
			return
		}
		if n < 3 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"err":"upstream"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(readTestdata(t, "rooms.json"))
	}))
	defer srv.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)

	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 after retries", len(got))
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3 (two 502s then success)", hits.Load())
	}
}

func TestRooms_5xxExhaustedReturnsEmpty(t *testing.T) {
	logs := captureLogs(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"err":"upstream"}`))
	}))
	defer srv.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)

	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Rooms = %+v, want empty", got)
	}
	if hits.Load() != 3 {
		t.Errorf("hits = %d, want 3", hits.Load())
	}
	if !strings.Contains(logs.String(), "tado_http") {
		t.Errorf("expected failure log, got:\n%s", logs.String())
	}
}

func TestRooms_4xxNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"err":"nope"}`))
	}))
	defer srv.Close()

	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)

	got, err := s.Rooms(context.Background())
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Rooms = %+v, want empty", got)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1 (no retry on 4xx)", hits.Load())
	}
}

func TestLastDebugBodies_RingAndCap(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		w.WriteHeader(http.StatusOK)
		if i == 1 {
			_, _ = w.Write(bytes.Repeat([]byte("x"), debugBodyCap+100))
			return
		}
		_, _ = w.Write([]byte(`{"rooms":[]}`))
	}))
	defer srv.Close()

	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)
	if _, err := s.Rooms(context.Background()); err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	bodies := s.LastDebugBodies()
	if len(bodies) != 1 {
		t.Fatalf("LastDebugBodies len = %d, want 1 after first call", len(bodies))
	}
	if len(bodies[0]) != debugBodyCap {
		t.Errorf("capped body len = %d, want %d", len(bodies[0]), debugBodyCap)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Rooms(context.Background()); err != nil {
			t.Fatalf("Rooms: %v", err)
		}
	}
	bodies = s.LastDebugBodies()
	if len(bodies) != debugRingSize {
		t.Fatalf("LastDebugBodies len = %d, want %d", len(bodies), debugRingSize)
	}
}

func TestRooms_UnexpectedBodyInDebugRing(t *testing.T) {
	logs := captureLogs(t)
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms_unexpected.json"), 0, 0)
	defer srv.Close()

	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)
	got, err := s.Rooms(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("Rooms = (%+v, %v), want empty/nil", got, err)
	}
	found := false
	for _, b := range s.LastDebugBodies() {
		if strings.Contains(b, `"foo"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("LastDebugBodies missing unexpected payload: %v", s.LastDebugBodies())
	}
	if !strings.Contains(logs.String(), "body_bytes") {
		t.Errorf("debug log missing body_bytes:\n%s", logs.String())
	}
}

func TestSample_StoreErrorDoesNotPropagate(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	clim := &recordingClimate{err: errors.New("db down")}
	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, clim,
		&fakePlants{rows: []store.PlantWithSpecies{plantIn("1", true)}})

	if err := s.Sample(context.Background()); err != nil {
		t.Fatalf("Sample: %v", err)
	}
}

func TestStatus_DelegatesToAuth(t *testing.T) {
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	home := "home-9"
	exp := clk.Now().Add(10 * time.Minute)
	obt := clk.Now()
	repo := &fakeRepo{row: store.TadoToken{
		HomeID:            &home,
		AccessExpiresAt:   &exp,
		RefreshObtainedAt: &obt,
		State:             store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(clk))
	s := NewSampler(c, nil, nil)

	got := s.Status(context.Background())
	want := climate.LinkStatus{
		State:             climate.StateLinked,
		HomeID:            "home-9",
		AccessExpiresAt:   exp,
		RefreshObtainedAt: obt,
	}
	if got != want {
		t.Errorf("Status() = %+v, want %+v", got, want)
	}
}

func TestBucketUTC(t *testing.T) {
	cases := []struct {
		in, want time.Time
	}{
		{
			in:   time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
			want: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 9, 16, 12, 17, 45, 0, time.UTC),
			want: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 9, 16, 12, 29, 59, 0, time.UTC),
			want: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 9, 16, 12, 30, 0, 0, time.UTC),
			want: time.Date(2026, 9, 16, 12, 30, 0, 0, time.UTC),
		},
		{
			in:   time.Date(2026, 9, 16, 12, 44, 0, 0, time.FixedZone("CEST", 2*3600)),
			want: time.Date(2026, 9, 16, 10, 30, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		if got := bucketUTC(tc.in); !got.Equal(tc.want) {
			t.Errorf("bucketUTC(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNoTokenInRoomLogs(t *testing.T) {
	logs := captureLogs(t)
	clk := newFakeClock(time.Date(2026, 9, 16, 12, 17, 0, 0, time.UTC))
	srv := startTadoAPI(t, readTestdata(t, "me.json"), readTestdata(t, "rooms.json"), 0, 0)
	defer srv.Close()

	s, _ := newTestSampler(t, &fakeRepo{row: linkedToken(clk, strp("12345"))}, srv, nil, nil)
	if _, err := s.Rooms(context.Background()); err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	out := logs.String()
	if strings.Contains(out, testAccess) || strings.Contains(out, "Bearer") {
		t.Errorf("logs leaked credential material:\n%s", out)
	}
}

func TestParseRooms_UsesTimestampWhenPresent(t *testing.T) {
	body := `{"rooms":[{"id":"1","name":"X","temperature":21,"humidity":40,"timestamp":"2026-01-02T03:04:05Z"}]}`
	got := parseRoomsBody([]byte(body), time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC))
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !got[0].ObservedAt.Equal(want) {
		t.Errorf("ObservedAt = %v, want %v", got[0].ObservedAt, want)
	}
}

func TestMeJSONFixtureDecodes(t *testing.T) {
	id, ok := parseHomeID(readTestdata(t, "me.json"))
	if !ok || id != "12345" {
		t.Fatalf("parseHomeID(me.json) = (%q, %v), want 12345", id, ok)
	}
	var raw map[string]any
	if err := json.Unmarshal(readTestdata(t, "me.json"), &raw); err != nil {
		t.Fatalf("me.json: %v", err)
	}
}
