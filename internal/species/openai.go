package species

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

const (
	// DefaultModel is what the drafter uses unless told otherwise.
	DefaultModel = "gpt-6-astra"

	// A generous ceiling: the record is a couple of thousand tokens, but the
	// model reasons first and the reasoning counts against the same cap.
	maxOutputTokens = 16000

	// Drafting reasons before it answers, so a call can legitimately run for a
	// minute or more. requestTimeout bounds one HTTP attempt; draftTimeout
	// bounds the whole draft including its one correction round.
	requestTimeout = 2 * time.Minute
	draftTimeout   = 4 * time.Minute

	// schemaName identifies the output schema to the API; it has no other use.
	schemaName = "species_record"
)

// OpenAIDrafter drafts species with the OpenAI Responses API.
type OpenAIDrafter struct {
	client openai.Client
	model  string
	clock  care.Clock
	log    *slog.Logger
}

// NewOpenAIDrafter builds a drafter. opts are appended after the defaults, so a
// test can point it at an httptest server with option.WithBaseURL.
func NewOpenAIDrafter(apiKey, model string, clock care.Clock, log *slog.Logger, opts ...option.RequestOption) *OpenAIDrafter {
	if model == "" {
		model = DefaultModel
	}
	all := append([]option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithRequestTimeout(requestTimeout),
	}, opts...)
	return &OpenAIDrafter{client: openai.NewClient(all...), model: model, clock: clock, log: log}
}

var _ Drafter = (*OpenAIDrafter)(nil)

// tally accumulates token counts across the draft's calls for one log line.
type tally struct {
	input, output, cached, reasoning int64
}

func (t *tally) add(u responses.ResponseUsage) {
	t.input += u.InputTokens
	t.output += u.OutputTokens
	t.cached += u.InputTokensDetails.CachedTokens
	t.reasoning += u.OutputTokensDetails.ReasoningTokens
}

// Draft asks the model for a species record, validates it, and if validation
// finds problems gives the model one chance to correct them. A second failure
// is returned as Result.Problems rather than retried again: a third attempt
// spends money to produce the same answer, and the review form can show the one
// bad field to a person.
func (d *OpenAIDrafter) Draft(ctx context.Context, req Request) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, fmt.Errorf("invalid species request: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()

	start := d.clock.Now()
	var used tally
	input := responses.ResponseInputParam{
		responses.ResponseInputItemParamOfMessage(userMessage(req), responses.EasyInputMessageRoleUser),
	}

	proposal, firstText, err := d.call(ctx, input, &used)
	if err != nil {
		d.logDraft(start, used, false, -1, err)
		return Result{}, err
	}
	res := d.assemble(proposal)

	if len(res.Problems) > 0 {
		input = append(input,
			responses.ResponseInputItemParamOfMessage(firstText, responses.EasyInputMessageRoleAssistant),
			responses.ResponseInputItemParamOfMessage(correctionMessage(res.Problems), responses.EasyInputMessageRoleUser),
		)
		corrected, _, err := d.call(ctx, input, &used)
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

// call makes one request and decodes the structured response. It returns the
// raw response text as well, so a correction round can replay it verbatim.
func (d *OpenAIDrafter) call(ctx context.Context, input responses.ResponseInputParam, used *tally) (Proposal, string, error) {
	resp, err := d.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        d.model,
		Instructions: openai.String(systemPrompt),
		Input:        responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   schemaName,
					Schema: outputSchema(),
					Strict: openai.Bool(true),
				},
			},
		},
		MaxOutputTokens: openai.Int(maxOutputTokens),
		// A draft is reviewed and stored here; there is no reason for the
		// provider to keep a copy of the conversation as well.
		Store: openai.Bool(false),
	})
	if err != nil {
		return Proposal{}, "", classify(err)
	}
	used.add(resp.Usage)

	if resp.Status == responses.ResponseStatusIncomplete {
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			return Proposal{}, "", ErrTruncated
		case "content_filter":
			return Proposal{}, "", ErrRefused
		default:
			return Proposal{}, "", fmt.Errorf("drafted species incomplete: %q", resp.IncompleteDetails.Reason)
		}
	}
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.Content {
			if c.Type == "refusal" {
				return Proposal{}, "", ErrRefused
			}
		}
	}

	text := resp.OutputText()
	proposal, err := decodeProposal(text)
	if err != nil {
		return Proposal{}, "", fmt.Errorf("decoding drafted species: %w", err)
	}
	return proposal, text, nil
}

// assemble converts a proposal for storage, stamps its provenance and validates it.
func (d *OpenAIDrafter) assemble(p Proposal) Result {
	s := ToSpecies(p)
	drafted := d.clock.Now()
	s.Origin = domain.OriginAI
	s.AIModel = d.model
	s.AIDraftedAt = &drafted
	return Result{Proposal: p, Species: s, Problems: catalog.Validate(s), Model: d.model}
}

func (d *OpenAIDrafter) logDraft(start time.Time, used tally, retried bool, problems int, err error) {
	attrs := []any{
		"model", d.model,
		"latency_ms", d.clock.Now().Sub(start).Milliseconds(),
		"input_tokens", used.input,
		"output_tokens", used.output,
		"cached_tokens", used.cached,
		"reasoning_tokens", used.reasoning,
		"retried", retried,
	}
	if err != nil {
		d.log.Error("species draft failed", append(attrs, "err", err)...)
		return
	}
	d.log.Info("species drafted", append(attrs, "problems", problems)...)
}

// classify maps an SDK error onto the package's sentinels. The SDK already
// retries 408/409/429/5xx, so a 429 here means retries were exhausted. OpenAI
// also answers 429 when the account has no credit left; that is not something
// waiting fixes, so it is not reported as a rate limit.
func classify(err error) error {
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		if apiErr.Code == "insufficient_quota" {
			return fmt.Errorf("%w: %w", ErrNoQuota, err)
		}
		return fmt.Errorf("%w: %w", ErrRateLimited, err)
	}
	return fmt.Errorf("calling the OpenAI API: %w", err)
}
