//go:build smoke

package species

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/catalog"
)

// TestDraftLiveSmoke calls the real OpenAI API and spends real money (a few
// cents). It is excluded from normal runs by the smoke build tag; run it with
//
//	PLANTATION_OPENAI_API_KEY=... go test -tags smoke -run Live -v ./internal/species/
//
// It is the only test that proves the request shape, the schema and the
// prompt are accepted by the live API.
func TestDraftLiveSmoke(t *testing.T) {
	key := os.Getenv("PLANTATION_OPENAI_API_KEY")
	if key == "" {
		t.Skip("PLANTATION_OPENAI_API_KEY unset")
	}
	model := os.Getenv("PLANTATION_OPENAI_MODEL")
	d := NewOpenAIDrafter(key, model, &care.NoopClock{Instant: time.Now()}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), draftTimeout)
	defer cancel()
	res, err := d.Draft(ctx, Request{Name: "Snake plant"})
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	for _, p := range res.Problems {
		t.Errorf("problem: %v", p)
	}
	if errs := catalog.Validate(res.Species); len(errs) != 0 {
		t.Errorf("drafted species does not validate: %v", errs)
	}
	t.Logf("identified %q (%s), kc=%.2f mad=%.2f %s, base=%d, %d tasks, retried=%v",
		res.Proposal.IdentifiedAs, res.Proposal.Confidence, res.Species.Kc, res.Species.MAD,
		res.Species.Substrate, res.Species.BaseIntervalDays, len(res.Species.Tasks), res.Retried)
}
