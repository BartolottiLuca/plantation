package species

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

const (
	// DefaultModel is what the drafter uses unless told otherwise.
	DefaultModel = "claude-opus-5"

	// A generous ceiling: the record is a couple of thousand tokens, but the
	// model reasons first and the reasoning counts against the same cap.
	maxTokens = 16000

	// Drafting thinks before it answers, so a call can legitimately run for a
	// minute or more. requestTimeout bounds one HTTP attempt; draftTimeout
	// bounds the whole draft including its one correction round.
	requestTimeout = 2 * time.Minute
	draftTimeout   = 4 * time.Minute
)

// AnthropicDrafter drafts species with the Claude API.
type AnthropicDrafter struct {
	client anthropic.Client
	model  string
	clock  care.Clock
	log    *slog.Logger
}

// NewAnthropicDrafter builds a drafter. opts are appended after the defaults, so
// a test can point it at an httptest server with option.WithBaseURL.
func NewAnthropicDrafter(apiKey, model string, clock care.Clock, log *slog.Logger, opts ...option.RequestOption) *AnthropicDrafter {
	if model == "" {
		model = DefaultModel
	}
	all := append([]option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(requestTimeout),
	}, opts...)
	return &AnthropicDrafter{client: anthropic.NewClient(all...), model: model, clock: clock, log: log}
}

var _ Drafter = (*AnthropicDrafter)(nil)

// tally accumulates token counts across the draft's calls for one log line.
type tally struct {
	input, output, cacheRead int64
}

func (t *tally) add(u anthropic.Usage) {
	t.input += u.InputTokens
	t.output += u.OutputTokens
	t.cacheRead += u.CacheReadInputTokens
}

// Draft asks the model for a species record, validates it, and if validation
// finds problems gives the model one chance to correct them. A second failure
// is returned as Result.Problems rather than retried again: a third attempt
// spends money to produce the same answer, and the review form can show the one
// bad field to a person.
func (d *AnthropicDrafter) Draft(ctx context.Context, req Request) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid species request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()

	start := d.clock.Now()
	var used tally
	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(userMessage(req))),
	}

	proposal, first, err := d.call(ctx, messages, &used)
	if err != nil {
		d.logDraft(start, used, false, -1, err)
		return Result{}, err
	}
	res := d.assemble(proposal)

	if len(res.Problems) > 0 {
		messages = append(messages, first.ToParam(),
			anthropic.NewUserMessage(anthropic.NewTextBlock(correctionMessage(res.Problems))))
		corrected, _, err := d.call(ctx, messages, &used)
		switch {
		case err != nil:
			// The first draft is still worth showing beside its errors, so a failed
			// correction is logged rather than discarding it.
			d.log.Warn("species correction failed", "model", d.model, "err", err)
			res.Retried = true
		default:
			res = d.assemble(corrected)
			res.Retried = true
		}
	}

	d.logDraft(start, used, res.Retried, len(res.Problems), nil)
	return res, nil
}

// call makes one request and decodes the structured response.
func (d *AnthropicDrafter) call(ctx context.Context, messages []anthropic.MessageParam, used *tally) (Proposal, *anthropic.Message, error) {
	resp, err := d.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     d.model,
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{{
			Text:         systemPrompt,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Messages:     messages,
		OutputConfig: anthropic.OutputConfigParam{Format: anthropic.JSONOutputFormatParam{Schema: outputSchema()}},
	})
	if err != nil {
		return Proposal{}, nil, classify(err)
	}
	used.add(resp.Usage)

	switch resp.StopReason {
	case anthropic.StopReasonRefusal:
		return Proposal{}, nil, ErrRefused
	case anthropic.StopReasonMaxTokens:
		return Proposal{}, nil, ErrTruncated
	}

	var text string
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			text = tb.Text
			break
		}
	}
	proposal, err := decodeProposal(text)
	if err != nil {
		return Proposal{}, nil, fmt.Errorf("decoding drafted species: %w", err)
	}
	return proposal, resp, nil
}

// assemble converts a proposal for storage, stamps its provenance and validates it.
func (d *AnthropicDrafter) assemble(p Proposal) Result {
	s := ToSpecies(p)
	drafted := d.clock.Now()
	s.Origin = domain.OriginAI
	s.AIModel = d.model
	s.AIDraftedAt = &drafted
	return Result{Proposal: p, Species: s, Problems: catalog.Validate(s), Model: d.model}
}

func (d *AnthropicDrafter) logDraft(start time.Time, used tally, retried bool, problems int, err error) {
	attrs := []any{
		"model", d.model,
		"latency_ms", d.clock.Now().Sub(start).Milliseconds(),
		"input_tokens", used.input,
		"output_tokens", used.output,
		"cache_read_tokens", used.cacheRead,
		"retried", retried,
	}
	if err != nil {
		d.log.Error("species draft failed", append(attrs, "err", err)...)
		return
	}
	d.log.Info("species drafted", append(attrs, "problems", problems)...)
}

// classify maps an SDK error onto the package's sentinels. The SDK already
// retries 408/409/429/5xx, so a 429 here means retries were exhausted.
func classify(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	}
	return fmt.Errorf("calling the Claude API: %w", err)
}

// userMessage carries only the form's fields, delimited, and escapes angle
// brackets so nothing a user types can close a tag or open one of its own.
func userMessage(r Request) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	var b strings.Builder
	b.WriteString("<plant>\n")
	fmt.Fprintf(&b, "<name>%s</name>\n", esc(strings.TrimSpace(r.Name)))
	if s := strings.TrimSpace(r.LabelText); s != "" {
		fmt.Fprintf(&b, "<label_text>%s</label_text>\n", esc(s))
	}
	if s := strings.TrimSpace(r.Notes); s != "" {
		fmt.Fprintf(&b, "<notes>%s</notes>\n", esc(s))
	}
	b.WriteString("</plant>\n\nDraft the species record for this plant.")
	return b.String()
}

// correctionMessage turns validation errors into the second-round user turn.
// Interval and slug problems are not fields the model returns, so they are
// explained in terms of the fields it does control.
func correctionMessage(problems []error) string {
	var b strings.Builder
	b.WriteString("The record you returned was checked and these problems were found:\n")
	for _, p := range problems {
		var fe *catalog.FieldError
		if errors.As(p, &fe) {
			switch fe.Field {
			case "base_interval_days", "min_interval_days", "max_interval_days":
				fmt.Fprintf(&b, "- %s (the app derives the watering interval from kc, mad and substrate; reconsider those three values)\n", fe.Message)
				continue
			case "slug":
				fmt.Fprintf(&b, "- %s (the slug is derived from scientific_name; give the plain binomial)\n", fe.Message)
				continue
			}
		}
		fmt.Fprintf(&b, "- %s\n", p.Error())
	}
	b.WriteString("\nReturn a corrected record. Change only what these problems require.")
	return b.String()
}

// wireProposal is the JSON shape of the response, kept separate from Proposal so
// the domain types carry no serialisation concerns.
type wireProposal struct {
	CommonName     string     `json:"common_name"`
	ScientificName string     `json:"scientific_name"`
	Description    string     `json:"description"`
	CareAdvice     string     `json:"care_advice"`
	Kc             float64    `json:"kc"`
	Substrate      string     `json:"substrate"`
	MAD            float64    `json:"mad"`
	DormantMonths  []int      `json:"dormant_months"`
	DormancyFactor float64    `json:"dormancy_factor"`
	MinTempC       float64    `json:"min_temp_c"`
	FrostTender    bool       `json:"frost_tender"`
	Tasks          []wireTask `json:"tasks"`
	Confidence     string     `json:"confidence"`
	IdentifiedAs   string     `json:"identified_as"`
	Alternatives   []string   `json:"alternatives"`
	Reasoning      string     `json:"reasoning"`
}

type wireTask struct {
	Slug         string `json:"slug"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	IntervalDays int    `json:"interval_days"`
	ActiveMonths []int  `json:"active_months"`
	OnlyIn       string `json:"only_in"`
}

// decodeProposal parses the response text strictly: an unexpected field means
// the schema and the decoder have drifted, which should be loud.
func decodeProposal(text string) (Proposal, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var w wireProposal
	if err := dec.Decode(&w); err != nil {
		return Proposal{}, err
	}
	conf := Confidence(w.Confidence)
	if !conf.valid() {
		return Proposal{}, fmt.Errorf("confidence %q is not high, medium or low", w.Confidence)
	}

	p := Proposal{
		CommonName:     w.CommonName,
		ScientificName: w.ScientificName,
		Description:    w.Description,
		CareAdvice:     w.CareAdvice,
		Kc:             w.Kc,
		Substrate:      domain.SubstrateKind(w.Substrate),
		MAD:            w.MAD,
		DormantMonths:  toMonths(w.DormantMonths),
		DormancyFactor: w.DormancyFactor,
		MinTempC:       w.MinTempC,
		FrostTender:    w.FrostTender,
		Confidence:     conf,
		IdentifiedAs:   w.IdentifiedAs,
		Alternatives:   w.Alternatives,
		Reasoning:      w.Reasoning,
	}
	for _, t := range w.Tasks {
		task := domain.SpeciesTask{
			Slug:         t.Slug,
			Kind:         domain.TaskKind(t.Kind),
			Label:        t.Label,
			IntervalDays: t.IntervalDays,
			ActiveMonths: toMonths(t.ActiveMonths),
		}
		// The schema makes the field required, so "anywhere" is spelt out on the
		// wire and stored as the empty value.
		switch t.OnlyIn {
		case onlyInAnywhere:
		case string(domain.Indoor), string(domain.Outdoor):
			task.OnlyIn = domain.Location(t.OnlyIn)
		default:
			return Proposal{}, fmt.Errorf("task %q: only_in %q is not anywhere, indoor or outdoor", t.Slug, t.OnlyIn)
		}
		p.Tasks = append(p.Tasks, task)
	}
	return p, nil
}

func toMonths(in []int) []time.Month {
	if len(in) == 0 {
		return nil
	}
	out := make([]time.Month, len(in))
	for i, v := range in {
		out[i] = time.Month(v)
	}
	return out
}
