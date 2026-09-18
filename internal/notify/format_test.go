package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
)

func TestFormatDigest(t *testing.T) {
	plantID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	cases := []struct {
		name              string
		overdue, dueToday []DigestLine
		wantEmpty         bool
		wantContains      []string
	}{
		{
			name:      "both empty is an empty message",
			wantEmpty: true,
		},
		{
			name: "overdue then due today, each line links to the plant",
			overdue: []DigestLine{
				{PlantID: plantID, PlantName: "Fig", Task: "water", Summary: "5 days overdue"},
			},
			dueToday: []DigestLine{
				{PlantID: plantID, PlantName: "Monstera", Task: "fertilize", Summary: "due today"},
			},
			wantContains: []string{
				"Overdue", "Due today",
				"Fig — water — 5 days overdue",
				"Monstera — fertilize — due today",
				"https://example.com/plants/" + plantID.String(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := FormatDigest("https://example.com", tc.overdue, tc.dueToday)
			if tc.wantEmpty {
				if !isEmptyMessage(msg) {
					t.Fatalf("FormatDigest = %+v, want empty", msg)
				}
				return
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(msg.Description, want) {
					t.Errorf("Description missing %q, got %q", want, msg.Description)
				}
			}
		})
	}
}

func TestFormatDigestTrimsTrailingSlashInBaseURL(t *testing.T) {
	plantID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	msg := FormatDigest("https://example.com/", nil, []DigestLine{
		{PlantID: plantID, PlantName: "Fig", Task: "water", Summary: "due"},
	})
	want := "https://example.com/plants/" + plantID.String()
	if !strings.Contains(msg.Description, want) {
		t.Errorf("Description = %q, want it to contain %q", msg.Description, want)
	}
	if strings.Contains(msg.Description, "com//plants") {
		t.Errorf("Description = %q, has a doubled slash", msg.Description)
	}
}

func TestFormatFrost(t *testing.T) {
	nights := []FrostNight{
		{Date: domain.Date{Year: 2026, Month: time.September, Day: 13}, TempC: -1.5},
		{Date: domain.Date{Year: 2026, Month: time.September, Day: 15}, TempC: -3},
	}
	cases := []struct {
		name       string
		advice     FrostAdvice
		wantAdvice string
	}{
		{name: "potted", advice: FrostAdviceBringIndoors, wantAdvice: "Bring Fig indoors"},
		{name: "in-ground fleece", advice: FrostAdviceFleece, wantAdvice: "Cover Fig with horticultural fleece"},
		{name: "in-ground lift", advice: FrostAdviceLift, wantAdvice: "pot it up and bring it indoors"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := FormatFrost("Fig", nights, tc.advice, "https://example.com/plants/x")
			if isEmptyMessage(msg) {
				t.Fatal("FormatFrost produced an empty message")
			}
			if msg.URL != "https://example.com/plants/x" {
				t.Errorf("URL = %q", msg.URL)
			}
			for _, want := range []string{"2026-09-13: -1.5°C", "2026-09-15: -3.0°C", tc.wantAdvice} {
				if !strings.Contains(msg.Description, want) {
					t.Errorf("Description = %q, missing %q", msg.Description, want)
				}
			}
		})
	}

	if !isEmptyMessage(FormatFrost("Fig", nil, FrostAdviceBringIndoors, "https://example.com/plants/x")) {
		t.Error("FormatFrost with no nights should be empty")
	}
}

func TestFormatHeatwaveAndRainSkipAggregateAndEmptyOnNoPlants(t *testing.T) {
	date := domain.Date{Year: 2026, Month: time.July, Day: 2}

	if !isEmptyMessage(FormatHeatwave("https://example.com", nil, date, 33)) {
		t.Error("FormatHeatwave with no plants should be empty")
	}
	if !isEmptyMessage(FormatRainSkip("https://example.com", nil, date)) {
		t.Error("FormatRainSkip with no plants should be empty")
	}

	hw := FormatHeatwave("https://example.com", []string{"Fig", "Basil"}, date, 33)
	if !strings.Contains(hw.Description, "Fig") || !strings.Contains(hw.Description, "Basil") {
		t.Errorf("heatwave Description = %q, missing a plant name", hw.Description)
	}

	rs := FormatRainSkip("https://example.com", []string{"Fig", "Basil"}, date)
	if !strings.Contains(rs.Description, "Fig") || !strings.Contains(rs.Description, "Basil") {
		t.Errorf("rain skip Description = %q, missing a plant name", rs.Description)
	}
}

func TestFormatOps(t *testing.T) {
	msg := FormatOps("weather_stale", "last successful fetch was 30h ago")
	if isEmptyMessage(msg) {
		t.Fatal("FormatOps produced an empty message")
	}
	if !strings.Contains(msg.Title, "weather_stale") {
		t.Errorf("Title = %q, missing reason", msg.Title)
	}
	if msg.URL != "" {
		t.Errorf("URL = %q, want empty for an ops alert", msg.URL)
	}
}
