package weather

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/store"
)

// fixedClock is a care.Clock that always returns the same instant.
type fixedClock struct{ instant time.Time }

func (c fixedClock) Now() time.Time { return c.instant }

// fakeProvider is an in-memory WeatherProvider for service tests.
type fakeProvider struct {
	days        []DailyWeather
	err         error
	gotPastDays int
	calls       int
}

func (f *fakeProvider) Daily(_ context.Context, _, _ float64, pastDays int) ([]DailyWeather, error) {
	f.calls++
	f.gotPastDays = pastDays
	if f.err != nil {
		return nil, f.err
	}
	return f.days, nil
}

// fakeStore is an in-memory Store. UpsertDays enforces the same invariant
// C02's real WeatherRepo enforces: a forecast row never replaces a stored
// observed row for the same date. Series and Refresh both depend on that
// invariant already holding at the store level, so the fake must honour it
// for the tests exercising it to mean anything.
type fakeStore struct {
	rows        map[string]store.WeatherDay // key: date.String()+"|"+kind
	lastFetched time.Time
	rangeErr    error
	upsertErr   error
	lastErr     error
	upsertCalls [][]store.WeatherDay
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]store.WeatherDay{}}
}

func rowKey(d domain.Date, kind string) string {
	return d.String() + "|" + kind
}

func (f *fakeStore) put(d store.WeatherDay) {
	f.rows[rowKey(d.Date, d.Kind)] = d
}

func (f *fakeStore) UpsertDays(_ context.Context, days []store.WeatherDay) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.upsertCalls = append(f.upsertCalls, days)
	for _, d := range days {
		if d.Kind == store.WeatherForecast {
			if _, exists := f.rows[rowKey(d.Date, store.WeatherObserved)]; exists {
				continue
			}
		}
		f.put(d)
	}
	return nil
}

func (f *fakeStore) Range(_ context.Context, _ string, from, to domain.Date) ([]store.WeatherDay, error) {
	if f.rangeErr != nil {
		return nil, f.rangeErr
	}
	var out []store.WeatherDay
	for d := from; !to.Before(d); d = d.AddDays(1) {
		if row, ok := f.rows[rowKey(d, store.WeatherObserved)]; ok {
			out = append(out, row)
			continue
		}
		if row, ok := f.rows[rowKey(d, store.WeatherForecast)]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (f *fakeStore) LastFetchedAt(_ context.Context, _ string) (time.Time, error) {
	if f.lastErr != nil {
		return time.Time{}, f.lastErr
	}
	return f.lastFetched, nil
}

var _ Store = (*fakeStore)(nil)

func TestFakeStoreForecastNeverOverwritesObserved(t *testing.T) {
	st := newFakeStore()
	date := domain.Date{Year: 2026, Month: time.September, Day: 10}
	obs := 3.1
	fc := 9.9

	if err := st.UpsertDays(context.Background(), []store.WeatherDay{
		{LocationKey: "k", Date: date, Kind: store.WeatherObserved, ET0MM: &obs},
	}); err != nil {
		t.Fatalf("upsert observed: %v", err)
	}
	if err := st.UpsertDays(context.Background(), []store.WeatherDay{
		{LocationKey: "k", Date: date, Kind: store.WeatherForecast, ET0MM: &fc},
	}); err != nil {
		t.Fatalf("upsert forecast: %v", err)
	}

	got, err := st.Range(context.Background(), "k", date, date)
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].Kind != store.WeatherObserved || got[0].ET0MM == nil || *got[0].ET0MM != obs {
		t.Fatalf("got = %+v, want observed row with ET0 %v unchanged", got[0], obs)
	}
}

func TestServiceRefreshUpsertsExpectedRows(t *testing.T) {
	obsET0 := 2.0
	fcET0 := 1.5
	date1 := domain.Date{Year: 2026, Month: time.September, Day: 8}
	date2 := domain.Date{Year: 2026, Month: time.September, Day: 9}

	provider := &fakeProvider{days: []DailyWeather{
		{Date: date1, Kind: KindObserved, ET0MM: &obsET0},
		{Date: date2, Kind: KindForecast, ET0MM: &fcET0},
	}}
	st := newFakeStore()
	svc := &Service{Provider: provider, Store: st, Lat: 51.5, Lon: -0.12}

	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if provider.gotPastDays != refreshPastDays {
		t.Errorf("gotPastDays = %d, want %d", provider.gotPastDays, refreshPastDays)
	}
	if len(st.upsertCalls) != 1 {
		t.Fatalf("upsert calls = %d, want 1", len(st.upsertCalls))
	}
	rows := st.upsertCalls[0]
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	wantKey := "51.500,-0.120"
	for _, row := range rows {
		if row.LocationKey != wantKey {
			t.Errorf("LocationKey = %q, want %q", row.LocationKey, wantKey)
		}
	}
	if rows[0].Date != date1 || rows[0].Kind != store.WeatherObserved || *rows[0].ET0MM != obsET0 {
		t.Errorf("rows[0] = %+v, want observed %s ET0 %v", rows[0], date1, obsET0)
	}
	if rows[1].Date != date2 || rows[1].Kind != store.WeatherForecast || *rows[1].ET0MM != fcET0 {
		t.Errorf("rows[1] = %+v, want forecast %s ET0 %v", rows[1], date2, fcET0)
	}
}

func TestServiceRefreshWrapsProviderError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	st := newFakeStore()
	svc := &Service{Provider: provider, Store: st, Lat: 1, Lon: 2}

	if err := svc.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh: want error when provider fails")
	}
	if len(st.upsertCalls) != 0 {
		t.Errorf("upsert calls = %d, want 0 after a fetch failure", len(st.upsertCalls))
	}
}

func TestServiceSeriesEmptyCacheIsStaleWithNoDaysAndNoPanic(t *testing.T) {
	st := newFakeStore() // LastFetchedAt is the zero value: never fetched.
	svc := &Service{Store: st, Lat: 1, Lon: 2, Clock: fixedClock{time.Now()}}

	from := domain.Date{Year: 2026, Month: time.September, Day: 1}
	to := from.AddDays(2)

	series, err := svc.Series(context.Background(), from, to)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if !series.OutdoorStale {
		t.Error("OutdoorStale = false, want true for a never-fetched cache")
	}
	if len(series.Days) != 0 {
		t.Errorf("len(Days) = %d, want 0", len(series.Days))
	}
}

func TestServiceSeriesFreshCacheKeepsRealDataUnchanged(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	st.lastFetched = now.Add(-12 * time.Hour)

	day := domain.Date{Year: 2026, Month: time.September, Day: 15}
	et0 := 2.5
	precip := 1.1
	st.put(store.WeatherDay{LocationKey: "k", Date: day, Kind: store.WeatherObserved, ET0MM: &et0, PrecipMM: &precip})

	svc := &Service{Store: st, Lat: 1, Lon: 2, Clock: fixedClock{now}}
	series, err := svc.Series(context.Background(), day, day)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if series.OutdoorStale {
		t.Error("OutdoorStale = true, want false for a 12h-old cache")
	}
	if len(series.Days) != 1 {
		t.Fatalf("len(Days) = %d, want 1", len(series.Days))
	}
	got := series.Days[0]
	if got.Estimated {
		t.Error("Estimated = true, want false for real cached data")
	}
	if !got.Observed {
		t.Error("Observed = false, want true")
	}
	if got.ET0MM != et0 {
		t.Errorf("ET0MM = %v, want %v", got.ET0MM, et0)
	}
	if got.PrecipMM != precip {
		t.Errorf("PrecipMM = %v, want %v", got.PrecipMM, precip)
	}
}

func TestServiceSeriesStaleCacheFillsMissingDaysWithTrailingMean(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	today := domain.TodayIn(now, time.UTC) // 2026-09-16

	st := newFakeStore()
	st.lastFetched = now.Add(-72 * time.Hour)

	// Trailing 14-day observed mean: today-2 and today-1, mean = 3.0.
	obs1, obs2 := 2.0, 4.0
	d1, d2 := today.AddDays(-2), today.AddDays(-1)
	st.put(store.WeatherDay{LocationKey: "k", Date: d1, Kind: store.WeatherObserved, ET0MM: &obs1})
	st.put(store.WeatherDay{LocationKey: "k", Date: d2, Kind: store.WeatherObserved, ET0MM: &obs2})

	// Requested range: d2 (cached, real) through today (not cached, missing).
	series, err := svc(st, fixedClock{now}).Series(context.Background(), d2, today)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if !series.OutdoorStale {
		t.Error("OutdoorStale = false, want true for a 72h-old cache")
	}
	if len(series.Days) != 2 {
		t.Fatalf("len(Days) = %d, want 2", len(series.Days))
	}

	real := series.Days[0]
	if real.Date != d2 || real.Estimated || real.ET0MM != obs2 {
		t.Errorf("Days[0] = %+v, want real cached day %s ET0 %v not estimated", real, d2, obs2)
	}

	filled := series.Days[1]
	wantMean := (obs1 + obs2) / 2
	if filled.Date != today {
		t.Errorf("Days[1].Date = %s, want %s", filled.Date, today)
	}
	if !filled.Estimated {
		t.Error("Days[1].Estimated = false, want true for a filled gap")
	}
	if filled.ET0MM != wantMean {
		t.Errorf("Days[1].ET0MM = %v, want trailing mean %v", filled.ET0MM, wantMean)
	}
	if filled.PrecipMM != 0 {
		t.Errorf("Days[1].PrecipMM = %v, want 0", filled.PrecipMM)
	}
}

func TestServiceSeriesSkipsDaysWithNilET0InsteadOfFabricatingZero(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	st := newFakeStore()
	st.lastFetched = now.Add(-1 * time.Hour) // fresh: no mean-filling in play

	day := domain.Date{Year: 2026, Month: time.September, Day: 15}
	st.put(store.WeatherDay{LocationKey: "k", Date: day, Kind: store.WeatherForecast, ET0MM: nil})

	series, err := svc(st, fixedClock{now}).Series(context.Background(), day, day)
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(series.Days) != 0 {
		t.Fatalf("len(Days) = %d, want 0 (nil-ET0 day must be dropped, not zeroed)", len(series.Days))
	}
}

func TestServiceSeriesWrapsStoreErrors(t *testing.T) {
	st := newFakeStore()
	st.rangeErr = errors.New("boom")
	if _, err := svc(st, fixedClock{time.Now()}).Series(context.Background(), domain.Date{}, domain.Date{}); err == nil {
		t.Fatal("Series: want error on Range failure")
	}

	st2 := newFakeStore()
	st2.lastErr = errors.New("boom")
	if _, err := svc(st2, fixedClock{time.Now()}).Series(context.Background(), domain.Date{}, domain.Date{}); err == nil {
		t.Fatal("Series: want error on LastFetchedAt failure")
	}
}

func TestRunBackfillFetchesWiderWindowAndUpserts(t *testing.T) {
	et0 := 1.2
	date := domain.Date{Year: 2026, Month: time.September, Day: 1}
	provider := &fakeProvider{days: []DailyWeather{{Date: date, Kind: KindObserved, ET0MM: &et0}}}
	st := newFakeStore()

	if err := RunBackfill(context.Background(), provider, st, 51.5, -0.12, 30); err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if provider.gotPastDays != 30 {
		t.Errorf("gotPastDays = %d, want 30", provider.gotPastDays)
	}
	if len(st.upsertCalls) != 1 {
		t.Fatalf("upsert calls = %d, want 1", len(st.upsertCalls))
	}
	if len(st.upsertCalls[0]) != 1 || st.upsertCalls[0][0].LocationKey != "51.500,-0.120" {
		t.Errorf("rows = %+v, want one row keyed 51.500,-0.120", st.upsertCalls[0])
	}
}

func TestRunBackfillWrapsProviderError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	st := newFakeStore()
	if err := RunBackfill(context.Background(), provider, st, 1, 2, 30); err == nil {
		t.Fatal("RunBackfill: want error when provider fails")
	}
}

func svc(st Store, clock fixedClock) *Service {
	return &Service{Store: st, Lat: 1, Lon: 2, Clock: clock}
}
