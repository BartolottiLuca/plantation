package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	// discordMaxAttempts is the total number of POST attempts per send call
	// (including the first), per SPEC.md §8.
	discordMaxAttempts = 3
	discordTimeout     = 10 * time.Second
	// maxDiscordBodyRead caps how much of an error response we read, so a
	// misbehaving endpoint cannot make us buffer an unbounded body.
	maxDiscordBodyRead = 64 * 1024
)

// discordEmbed mirrors the subset of Discord's embed schema this client
// uses. Embed.URL turns the title into a clickable link; that is how a
// Message's URL becomes actionable, since plain incoming webhooks cannot
// carry interactive components.
type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

// discordClient posts webhook payloads with the retry rules from SPEC.md §8:
// 429 honours retry_after from the JSON body, 5xx retries, 404 is permanent.
// At most discordMaxAttempts attempts total. sleep is a plain func field
// (not internal/care's Clock — this package has no mandate to depend on it)
// so tests can assert on sleep duration without a real wait.
type discordClient struct {
	webhookURL string
	httpClient *http.Client
	sleep      func(time.Duration)
}

func newDiscordClient(webhookURL string) *discordClient {
	return &discordClient{
		webhookURL: webhookURL,
		httpClient: &http.Client{Timeout: discordTimeout},
		sleep:      time.Sleep,
	}
}

// permanentDiscordError marks a failure that must not be retried: the
// webhook was deleted (404), or Discord rejected the request in a way a
// retry cannot fix (any other non-2xx, non-429, non-5xx status).
type permanentDiscordError struct {
	status int
}

func (e *permanentDiscordError) Error() string {
	return fmt.Sprintf("discord webhook rejected the request permanently (status %d)", e.status)
}

// rateLimitError carries the retry_after Discord asked for.
type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("discord rate limited the request, retry after %s", e.retryAfter)
}

// discordStatusError is a retryable HTTP-level failure (5xx).
type discordStatusError struct {
	status int
}

func (e *discordStatusError) Error() string {
	return fmt.Sprintf("discord returned status %d", e.status)
}

// send posts msg as a single embed, retrying per SPEC.md §8.
//
// The returned error never contains c.webhookURL. net/http wraps every
// transport-level failure (DNS, dial, TLS, timeout, and even a malformed
// URL) in *url.Error, whose Error() interpolates the request URL verbatim —
// that is the "easy thing to get wrong" the card calls out twice. Every
// return path here goes through a value we constructed ourselves or through
// sanitizeTransportError, never through a bare %w/%v of the http.Client
// error or the request-construction error.
func (c *discordClient) send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(discordPayload{Embeds: []discordEmbed{{
		Title:       msg.Title,
		Description: msg.Description,
		URL:         msg.URL,
	}}})
	if err != nil {
		return fmt.Errorf("encoding discord payload: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= discordMaxAttempts; attempt++ {
		err := c.postOnce(ctx, body)
		if err == nil {
			return nil
		}
		lastErr = err

		var perm *permanentDiscordError
		if errors.As(err, &perm) {
			return err
		}

		if attempt == discordMaxAttempts {
			break
		}

		var rateLimited *rateLimitError
		if errors.As(err, &rateLimited) {
			c.sleep(rateLimited.retryAfter)
		}
		// 5xx and sanitized transport errors: retry immediately, no
		// additional backoff beyond what 429's retry_after already gives us.
	}
	return lastErr
}

func (c *discordClient) postOnce(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.webhookURL, bytes.NewReader(body))
	if err != nil {
		// A malformed c.webhookURL surfaces here as a *url.Error whose
		// Error() includes the URL text; never let that escape.
		return errors.New("posting to discord: invalid webhook configuration")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return sanitizeTransportError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDiscordBodyRead))
		return nil
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxDiscordBodyRead))

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return &rateLimitError{retryAfter: parseRetryAfter(respBody)}
	case resp.StatusCode == http.StatusNotFound:
		return &permanentDiscordError{status: resp.StatusCode}
	case resp.StatusCode >= 500:
		return &discordStatusError{status: resp.StatusCode}
	default:
		// Any other 4xx (400 bad payload, 401 unauthorized, ...): retrying
		// the identical payload will not help.
		return &permanentDiscordError{status: resp.StatusCode}
	}
}

// parseRetryAfter reads retry_after as a float number of seconds from the
// JSON body per SPEC.md §8 — Discord's rate-limit body, not the standard
// Retry-After header.
func parseRetryAfter(body []byte) time.Duration {
	var payload struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.RetryAfter <= 0 {
		return time.Second
	}
	return time.Duration(payload.RetryAfter * float64(time.Second))
}

// sanitizeTransportError collapses a transport-level failure to a fixed set
// of generic messages that cannot contain c.webhookURL.
func sanitizeTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		switch {
		case errors.Is(urlErr.Err, context.DeadlineExceeded):
			return errors.New("posting to discord: request timed out")
		case errors.Is(urlErr.Err, context.Canceled):
			return errors.New("posting to discord: request canceled")
		default:
			return errors.New("posting to discord: network error")
		}
	}
	return errors.New("posting to discord: unexpected error")
}
