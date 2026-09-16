package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

func readFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/forecast.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	return body
}

// newFixtureServer serves the recorded fixture for every request and hands
// the caller the last request's query so tests can assert on it.
func newFixtureServer(t *testing.T, lastQuery *url.Values) *httptest.Server {
	t.Helper()
	body := readFixture(t)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastQuery != nil {
			*lastQuery = r.URL.Query()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func TestClientDailySendsExpectedQueryParams(t *testing.T) {
	var got url.Values
	srv := newFixtureServer(t, &got)
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	if _, err := c.Daily(context.Background(), 51.5, -0.12, 7); err != nil {
		t.Fatalf("Daily: %v", err)
	}

	want := map[string]string{
		"latitude":      "51.5",
		"longitude":     "-0.12",
		"timezone":      "Europe/London",
		"past_days":     "7",
		"forecast_days": "16",
		"daily":         dailyParams,
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query %s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestClientDailyCountAndObservedForecastSplit(t *testing.T) {
	srv := newFixtureServer(t, nil)
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	got, err := c.Daily(context.Background(), 51.5, -0.12, 7)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if len(got) != 13 {
		t.Fatalf("len(got) = %d, want 13", len(got))
	}
	for i, d := range got {
		wantKind := KindForecast
		if i < 7 {
			wantKind = KindObserved
		}
		if d.Kind != wantKind {
			t.Errorf("day %d (%s) kind = %s, want %s", i, d.Date, d.Kind, wantKind)
		}
	}
	wantFirst := domain.Date{Year: 2026, Month: time.September, Day: 8}
	if got[0].Date != wantFirst {
		t.Errorf("first date = %s, want %s", got[0].Date, wantFirst)
	}
	wantLast := domain.Date{Year: 2026, Month: time.September, Day: 20}
	if got[len(got)-1].Date != wantLast {
		t.Errorf("last date = %s, want %s", got[len(got)-1].Date, wantLast)
	}
}

func TestClientDailyDifferentPastDaysMovesTheSplit(t *testing.T) {
	srv := newFixtureServer(t, nil)
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	got, err := c.Daily(context.Background(), 51.5, -0.12, 3)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	for i, d := range got {
		wantKind := KindForecast
		if i < 3 {
			wantKind = KindObserved
		}
		if d.Kind != wantKind {
			t.Errorf("day %d kind = %s, want %s", i, d.Kind, wantKind)
		}
	}
}

func TestClientDailyNullEntriesBecomeNilNotZero(t *testing.T) {
	srv := newFixtureServer(t, nil)
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	got, err := c.Daily(context.Background(), 51.5, -0.12, 7)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}

	// The last fixture day (the forecast horizon edge) is null across every
	// field. None of them may decode to a fabricated 0.0.
	last := got[len(got)-1]
	if last.ET0MM != nil {
		t.Errorf("last.ET0MM = %v, want nil", *last.ET0MM)
	}
	if last.PrecipMM != nil {
		t.Errorf("last.PrecipMM = %v, want nil", *last.PrecipMM)
	}
	if last.PrecipProb != nil {
		t.Errorf("last.PrecipProb = %v, want nil", *last.PrecipProb)
	}
	if last.TMinC != nil {
		t.Errorf("last.TMinC = %v, want nil", *last.TMinC)
	}
	if last.TMaxC != nil {
		t.Errorf("last.TMaxC = %v, want nil", *last.TMaxC)
	}

	// A day where only one field (precip probability) is null must still
	// decode its other fields normally: nulls are per-field, not per-day.
	mid := got[9] // 2026-09-17
	if mid.PrecipProb != nil {
		t.Errorf("mid.PrecipProb = %v, want nil", *mid.PrecipProb)
	}
	if mid.ET0MM == nil || *mid.ET0MM != 1.7 {
		t.Errorf("mid.ET0MM = %v, want 1.7", mid.ET0MM)
	}
	if mid.PrecipMM == nil || *mid.PrecipMM != 2.0 {
		t.Errorf("mid.PrecipMM = %v, want 2.0", mid.PrecipMM)
	}
}

func TestClientDailyRetriesOn5xxThenSucceeds(t *testing.T) {
	body := readFixture(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	got, err := c.Daily(context.Background(), 51.5, -0.12, 7)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if len(got) != 13 {
		t.Errorf("len(got) = %d, want 13", len(got))
	}
}

func TestClientDailyDoesNotRetryOn4xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true,"reason":"bad params"}`))
	}))
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	_, err := c.Daily(context.Background(), 51.5, -0.12, 7)
	if err == nil {
		t.Fatal("Daily: want error for 4xx response")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (4xx must not be retried)", calls)
	}
}

func TestClientDailyGivesUpAfterMaxAttemptsOn5xx(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := &Client{Timezone: "Europe/London", BaseURL: srv.URL}
	_, err := c.Daily(context.Background(), 51.5, -0.12, 7)
	if err == nil {
		t.Fatal("Daily: want error after exhausting retries")
	}
	if calls != maxAttempts {
		t.Errorf("calls = %d, want %d", calls, maxAttempts)
	}
}
