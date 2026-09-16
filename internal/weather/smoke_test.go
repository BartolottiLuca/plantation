//go:build smoke

package weather

import (
	"context"
	"testing"
	"time"
)

// TestClientDailyLiveSmoke hits the real api.open-meteo.com endpoint. It is
// excluded from normal test runs by the smoke build tag; run explicitly with
// `go test -tags smoke ./internal/weather/...`.
func TestClientDailyLiveSmoke(t *testing.T) {
	c := NewClient("Europe/London")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	got, err := c.Daily(ctx, 51.5074, -0.1278, 7)
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("got 0 days from live Open-Meteo")
	}
	sawObserved, sawForecast := false, false
	for _, d := range got {
		switch d.Kind {
		case KindObserved:
			sawObserved = true
		case KindForecast:
			sawForecast = true
		}
	}
	if !sawObserved || !sawForecast {
		t.Errorf("sawObserved=%v sawForecast=%v, want both", sawObserved, sawForecast)
	}
}
