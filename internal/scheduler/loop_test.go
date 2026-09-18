package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/google/uuid"
)

func london(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func plantRow(id uuid.UUID, acquired time.Time) store.PlantWithSpecies {
	return testPlant(id, acquired)
}

func TestThirtyDaysOneDigestPerActionableDayAndOneFrost(t *testing.T) {
	loc := london(t)
	clk := &testClock{t: time.Date(2026, 3, 20, 8, 0, 0, 0, loc)}
	id := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	acquired := time.Date(2026, 3, 20, 12, 0, 0, 0, loc)
	n := newFakeNotify()
	wx := &fakeWeather{series: care.EnvSeries{Days: []care.DayEnv{{
		Date:     domain.Date{Year: 2026, Month: time.March, Day: 25},
		TMinC:    -1,
		Observed: false,
	}}}}

	loop := New(Loop{
		Clock:      clk,
		Location:   loc,
		DigestHour: 9,
		BaseURL:    "https://plantation.example.invalid",
		Lock:       AlwaysLock{},
		Weather:    wx,
		Plants:     &fakePlants{rows: []store.PlantWithSpecies{plantRow(id, acquired)}},
		Events:     &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}},
		Notify:     n,
		Log:        discardLog(),
	})

	end := time.Date(2026, 4, 18, 23, 0, 0, 0, loc)
	for !clk.Now().After(end) {
		loop.Tick(context.Background())
		clk.add(time.Hour)
	}

	got := n.digestSent()
	if len(got) != 23 {
		t.Fatalf("digest count = %d (%v), want 23 (27 Mar–18 Apr)", len(got), got)
	}
	wantFirst := "digest:2026-03-27"
	wantLast := "digest:2026-04-18"
	if got[0] != wantFirst || got[len(got)-1] != wantLast {
		t.Fatalf("digest range %s..%s, want %s..%s", got[0], got[len(got)-1], wantFirst, wantLast)
	}

	frostKey := "frost:" + id.String() + ":2026-03-25"
	frost := 0
	for _, k := range n.keys("frost:") {
		if k == frostKey {
			frost++
		}
	}
	if frost != 1 {
		t.Fatalf("frost keys = %v, want exactly one %s", n.keys("frost:"), frostKey)
	}
	if !n.has("digest:2026-03-29") {
		t.Fatal("missing digest on DST transition day 2026-03-29")
	}
}

func TestFrostAlertsAggregateNightsAndChooseProtection(t *testing.T) {
	today := domain.Date{Year: 2026, Month: time.September, Day: 18}
	firstNight := domain.Date{Year: 2026, Month: time.September, Day: 20}
	secondNight := domain.Date{Year: 2026, Month: time.September, Day: 21}
	wx := &fakeWeather{series: care.EnvSeries{Days: []care.DayEnv{
		{Date: secondNight, TMinC: -8.5},
		{Date: firstNight, TMinC: -6},
	}}}
	n := &capturingNotifier{sent: map[string]notify.Message{}}
	loop := New(Loop{
		BaseURL: "https://plantation.example.invalid",
		Weather: wx,
		Notify:  n,
		Log:     discardLog(),
	})

	rows := []store.PlantWithSpecies{
		frostPlant("11111111-1111-1111-1111-111111111111", "Potted fig", false, 12),
		frostPlant("22222222-2222-2222-2222-222222222222", "Ground monstera", true, 12),
		frostPlant("33333333-3333-3333-3333-333333333333", "Protected hydrangea", true, -5),
		frostPlant("44444444-4444-4444-4444-444444444444", "Lifted shrub", true, -3),
	}
	loop.frostAlerts(context.Background(), today, rows)
	loop.frostAlerts(context.Background(), today, rows)

	cases := []struct {
		row        store.PlantWithSpecies
		wantAdvice string
	}{
		{row: rows[0], wantAdvice: "Bring Potted fig indoors"},
		{row: rows[1], wantAdvice: "pot it up and bring it indoors"},
		{row: rows[2], wantAdvice: "Cover Protected hydrangea with horticultural fleece"},
		{row: rows[3], wantAdvice: "pot it up and bring it indoors"},
	}
	if len(n.sent) != len(cases) {
		t.Fatalf("frost sends = %d, want %d: %v", len(n.sent), len(cases), n.sent)
	}
	for _, tc := range cases {
		key := "frost:" + tc.row.Plant.ID.String() + ":" + firstNight.String()
		msg, ok := n.sent[key]
		if !ok {
			t.Errorf("missing earliest-night key %s", key)
			continue
		}
		for _, want := range []string{firstNight.String(), secondNight.String(), tc.wantAdvice} {
			if !strings.Contains(msg.Description, want) {
				t.Errorf("%s description = %q, missing %q", tc.row.Plant.Name, msg.Description, want)
			}
		}
	}
}

func frostPlant(id, name string, inGround bool, minTempC float64) store.PlantWithSpecies {
	return store.PlantWithSpecies{
		Plant: domain.Plant{
			ID:       uuid.MustParse(id),
			Name:     name,
			Location: domain.Outdoor,
			InGround: inGround,
			Active:   true,
		},
		Species: domain.Species{MinTempC: minTempC, FrostTender: true},
	}
}

type capturingNotifier struct {
	sent map[string]notify.Message
}

func (n *capturingNotifier) SendOnce(_ context.Context, key, _ string, msg notify.Message) error {
	if _, exists := n.sent[key]; !exists {
		n.sent[key] = msg
	}
	return nil
}

func TestRestartAt907Sends920AfterSendDoesNot(t *testing.T) {
	loc := london(t)
	id := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	acquired := time.Date(2026, 3, 13, 12, 0, 0, 0, loc)
	plants := &fakePlants{rows: []store.PlantWithSpecies{plantRow(id, acquired)}}
	events := &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}}
	n := newFakeNotify()

	clk := &testClock{t: time.Date(2026, 3, 20, 9, 7, 0, 0, loc)}
	first := New(Loop{
		Clock: clk, Location: loc, DigestHour: 9, BaseURL: "https://x.invalid",
		Lock: AlwaysLock{}, Plants: plants, Events: events, Notify: n, Log: discardLog(),
	})
	first.Tick(context.Background())
	if len(n.digestSent()) != 1 {
		t.Fatalf("09:07 digest = %v, want 1", n.digestSent())
	}

	clk.set(time.Date(2026, 3, 20, 9, 20, 0, 0, loc))
	second := New(Loop{
		Clock: clk, Location: loc, DigestHour: 9, BaseURL: "https://x.invalid",
		Lock: AlwaysLock{}, Plants: plants, Events: events, Notify: n, Log: discardLog(),
	})
	second.Tick(context.Background())
	if len(n.digestSent()) != 1 {
		t.Fatalf("09:20 resend = %v, want still 1", n.digestSent())
	}
}

func TestFailuresDoNotStopOtherActivities(t *testing.T) {
	loc := london(t)
	id := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	acquired := time.Date(2026, 3, 13, 12, 0, 0, 0, loc)
	n := newFakeNotify()
	wx := &fakeWeather{err: errors.New("open-meteo down")}
	samp := &fakeSampler{err: errors.New("tado down")}
	sweep := &fakeSweep{err: errors.New("sweep failed")}
	retain := &fakeRetain{err: errors.New("purge failed")}

	loop := New(Loop{
		Clock:          &testClock{t: time.Date(2026, 3, 20, 10, 0, 0, 0, loc)},
		Location:       loc,
		DigestHour:     9,
		BaseURL:        "https://x.invalid",
		WeatherEnabled: true,
		TadoEnabled:    true,
		LocationKey:    "0.000,0.000",
		Lock:           AlwaysLock{},
		Weather:        wx,
		WAge:           fakeAge{},
		Sampler:        samp,
		Plants:         &fakePlants{rows: []store.PlantWithSpecies{plantRow(id, acquired)}},
		Events:         &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}},
		Notify:         n,
		Sweep:          sweep,
		Retain:         retain,
		Log:            discardLog(),
	})
	loop.Tick(context.Background())

	if wx.refreshN != 1 {
		t.Errorf("weather refresh attempts = %d, want 1", wx.refreshN)
	}
	if samp.n != 1 {
		t.Errorf("tado sample attempts = %d, want 1", samp.n)
	}
	if retain.n != 1 {
		t.Errorf("purge attempts = %d, want 1", retain.n)
	}
	if len(n.digestSent()) != 1 {
		t.Fatalf("digest still sent after provider failures, got %v", n.digestSent())
	}
}

func TestDatabaseListFailureLeavesLoopRunning(t *testing.T) {
	loc := london(t)
	n := newFakeNotify()
	wx := &fakeWeather{}
	loop := New(Loop{
		Clock:          &testClock{t: time.Date(2026, 3, 20, 10, 0, 0, 0, loc)},
		Location:       loc,
		DigestHour:     9,
		WeatherEnabled: true,
		LocationKey:    "1.000,2.000",
		Lock:           AlwaysLock{},
		Weather:        wx,
		WAge:           fakeAge{},
		Plants:         &fakePlants{err: errors.New("db down")},
		Events:         &fakeEvents{},
		Notify:         n,
		Log:            discardLog(),
	})
	loop.Tick(context.Background())
	if wx.refreshN != 1 {
		t.Errorf("weather refresh = %d, want 1 (db list failed independently)", wx.refreshN)
	}
	if len(n.digestSent()) != 0 {
		t.Errorf("digest = %v, want none", n.digestSent())
	}
	loop.Tick(context.Background())
}

func TestLockNotHeldDoesNotTick(t *testing.T) {
	loc := london(t)
	n := newFakeNotify()
	loop := New(Loop{
		Clock:    &testClock{t: time.Date(2026, 3, 20, 10, 0, 0, 0, loc)},
		Location: loc, DigestHour: 9, Lock: NeverLock{},
		Plants: &fakePlants{rows: []store.PlantWithSpecies{plantRow(uuid.Nil, time.Time{})}},
		Events: &fakeEvents{}, Notify: n, Ticker: time.Hour, Log: discardLog(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	loop.Run(ctx)
	if loop.HoldsLock() {
		t.Fatal("HoldsLock true")
	}
	if len(n.digestSent()) != 0 {
		t.Fatalf("sent without lock: %v", n.digestSent())
	}
}

func TestWeatherFreshnessSkipsRefresh(t *testing.T) {
	loc := london(t)
	now := time.Date(2026, 3, 20, 10, 0, 0, 0, loc)
	wx := &fakeWeather{}
	loop := New(Loop{
		Clock:          &testClock{t: now},
		Location:       loc,
		WeatherEnabled: true,
		LocationKey:    "k",
		Weather:        wx,
		WAge:           fakeAge{at: now.Add(-20 * time.Minute)},
		Log:            discardLog(),
	})
	if err := loop.refreshWeather(context.Background()); err != nil {
		t.Fatal(err)
	}
	if wx.refreshN != 0 {
		t.Fatalf("refreshN = %d, want 0", wx.refreshN)
	}
}

func TestTadoNeedsReauthOps(t *testing.T) {
	loc := london(t)
	n := newFakeNotify()
	loop := New(Loop{
		Clock:       &testClock{t: time.Date(2026, 3, 20, 10, 0, 0, 0, loc)},
		Location:    loc,
		DigestHour:  9,
		TadoEnabled: true,
		Sampler:     &fakeSampler{status: climate.LinkStatus{State: climate.StateNeedsReauth}},
		Plants:      &fakePlants{},
		Events:      &fakeEvents{},
		Notify:      n,
		Log:         discardLog(),
	})
	loop.Tick(context.Background())
	if !n.has("ops:tado_needs_reauth:2026-03-20") {
		t.Fatalf("missing needs_reauth ops, sent %v", n.sent)
	}
}

func TestEmptyDayRecordsSkippedDigest(t *testing.T) {
	loc := london(t)
	id := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	acquired := time.Date(2026, 3, 20, 12, 0, 0, 0, loc)
	n := newFakeNotify()
	loop := New(Loop{
		Clock:      &testClock{t: time.Date(2026, 3, 21, 10, 0, 0, 0, loc)},
		Location:   loc,
		DigestHour: 9,
		BaseURL:    "https://x.invalid",
		Plants:     &fakePlants{rows: []store.PlantWithSpecies{plantRow(id, acquired)}},
		Events:     &fakeEvents{byPlant: map[uuid.UUID][]domain.CareEvent{}},
		Notify:     n,
		Log:        discardLog(),
	})
	loop.Tick(context.Background())
	if len(n.digestSent()) != 0 {
		t.Fatalf("non-empty digest on a quiet day: %v", n.digestSent())
	}
	if !n.has("digest:2026-03-21") {
		t.Fatal("quiet day must still record digest:today as skipped")
	}
}

func TestPurgeTelemetryCutoffIsSixMonths(t *testing.T) {
	loc := london(t)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, loc)
	retain := &fakeRetain{}
	loop := New(Loop{
		Clock:    &testClock{t: now},
		Location: loc,
		Retain:   retain,
		Log:      discardLog(),
	})
	loop.Tick(context.Background())
	if retain.n != 1 {
		t.Fatalf("purge n = %d, want 1", retain.n)
	}
	want := now.AddDate(0, -6, 0)
	if !retain.before.Equal(want) {
		t.Fatalf("cutoff = %s, want %s", retain.before, want)
	}
}
