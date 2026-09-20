package notify

import (
	"fmt"
	"strings"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/google/uuid"
)

// DigestLine is one actionable task row for FormatDigest. It is notify's own
// small struct (not domain.Due or care.Explanation) because this package
// does not own those types and only needs the handful of fields the digest
// line renders: plant.Name, the task's label (care.ScheduledTask.Label — not
// its kind, since two tasks can share a kind and only the label tells them
// apart), and care.Explanation.Summary, computed by whoever calls
// FormatDigest (C09).
type DigestLine struct {
	PlantID   uuid.UUID
	PlantName string
	Task      string
	Summary   string
}

type FrostNight struct {
	Date  domain.Date
	TempC float64
}

type FrostAdvice string

const (
	FrostAdviceBringIndoors FrostAdvice = "bring_indoors"
	FrostAdviceFleece       FrostAdvice = "fleece"
	FrostAdviceLift         FrostAdvice = "lift"
)

// FormatDigest renders SPEC.md §8's daily digest: one embed, an "Overdue"
// section then a "Due today" section, one line per task as
// "<plant> — <task> — <summary>", each line linking to
// <baseURL>/plants/<id> because plain webhooks cannot carry buttons. An
// empty result (both slices empty) returns a zero Message, which SendOnce
// treats as empty and records as skipped without ever calling Discord.
func FormatDigest(baseURL string, overdue, dueToday []DigestLine) Message {
	var b strings.Builder
	writeSection := func(header string, lines []DigestLine) {
		if len(lines) == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("**")
		b.WriteString(header)
		b.WriteString("**\n")
		for i, line := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(digestLineText(baseURL, line))
		}
	}
	writeSection("Overdue", overdue)
	writeSection("Due today", dueToday)

	if b.Len() == 0 {
		return Message{}
	}
	return Message{
		Title:       "Plant care digest",
		Description: b.String(),
	}
}

func digestLineText(baseURL string, line DigestLine) string {
	return fmt.Sprintf("[%s — %s — %s](%s)", line.PlantName, line.Task, line.Summary, plantURL(baseURL, line.PlantID))
}

func plantURL(baseURL string, id uuid.UUID) string {
	return strings.TrimRight(baseURL, "/") + "/plants/" + id.String()
}

// FormatFrost renders one plant's qualifying forecast nights under the
// earliest night's dedupe key.
func FormatFrost(plantName string, nights []FrostNight, advice FrostAdvice, url string) Message {
	if len(nights) == 0 {
		return Message{}
	}
	var b strings.Builder
	b.WriteString("Cold nights forecast:")
	for _, night := range nights {
		fmt.Fprintf(&b, "\n- %s: %.1f°C", night.Date, night.TempC)
	}
	b.WriteString("\n\n")
	switch advice {
	case FrostAdviceFleece:
		fmt.Fprintf(&b, "Cover %s with horticultural fleece before the first cold night.", plantName)
	case FrostAdviceLift:
		fmt.Fprintf(&b, "Horticultural fleece is not enough protection for %s; pot it up and bring it indoors before the first cold night.", plantName)
	default:
		fmt.Fprintf(&b, "Bring %s indoors before the first cold night.", plantName)
	}
	return Message{
		Title:       fmt.Sprintf("Frost warning: %s", plantName),
		Description: b.String(),
		URL:         url,
	}
}

// FormatHeatwave renders SPEC.md §8's heatwave alert, aggregated across
// every affected plant in a single message — the dedupe key
// (heatwave:<date>, no plant id) is day-scoped rather than per-plant, unlike
// frost, so this takes the whole set at once.
func FormatHeatwave(baseURL string, plantNames []string, date domain.Date, tempC float64) Message {
	if len(plantNames) == 0 {
		return Message{}
	}
	return Message{
		Title: "Heatwave warning",
		Description: fmt.Sprintf("%.1f°C forecast on %s — check shade and watering for: %s.",
			tempC, date, strings.Join(plantNames, ", ")),
		URL: plantsURL(baseURL),
	}
}

// FormatRainSkip renders SPEC.md §8's rain-deferral alert, aggregated across
// every plant whose watering was deferred for a given date (dedupe key
// rain_skip:<date>, no plant id).
func FormatRainSkip(baseURL string, plantNames []string, date domain.Date) Message {
	if len(plantNames) == 0 {
		return Message{}
	}
	return Message{
		Title: "Watering deferred for rain",
		Description: fmt.Sprintf("Rain expected on %s — watering deferred for: %s.",
			date, strings.Join(plantNames, ", ")),
		URL: plantsURL(baseURL),
	}
}

// FormatOps renders SPEC.md §8's ops alert (Tado re-auth, stale weather, a
// send failure). It carries no plant deep link — ops alerts are about the
// app's own health, not a specific plant — but always has a reason so the
// dedupe key's ops:<reason>:<date> shape stays legible in Discord too.
func FormatOps(reason, detail string) Message {
	return Message{
		Title:       fmt.Sprintf("Plantation ops alert: %s", reason),
		Description: detail,
	}
}

func plantsURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + "/plants"
}
