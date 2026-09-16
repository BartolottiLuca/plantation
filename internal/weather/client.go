package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

const (
	defaultBaseURL = "https://api.open-meteo.com/v1/forecast"

	requestTimeout = 10 * time.Second
	maxAttempts    = 3
	backoffBase    = 50 * time.Millisecond
	backoffCap     = 500 * time.Millisecond

	// forecastDays is fixed at the API maximum for non-commercial use; SPEC.md
	// §10.1 pins the whole daily parameter set, this is not tunable per call.
	forecastDays = 16
	dailyParams  = "et0_fao_evapotranspiration,precipitation_sum,precipitation_probability_max," +
		"temperature_2m_min,temperature_2m_max"
)

// Client fetches daily weather from Open-Meteo's forecast endpoint. It
// implements WeatherProvider.
type Client struct {
	// Timezone is the IANA string sent as Open-Meteo's timezone parameter,
	// which controls where the API's notion of "today" falls.
	Timezone string
	// BaseURL overrides the public endpoint; tests point it at an httptest
	// server. Empty means the real Open-Meteo host.
	BaseURL string
	// HTTPClient overrides the transport; nil means http.DefaultClient. The
	// per-attempt 10s timeout is applied via context regardless.
	HTTPClient *http.Client
}

// NewClient returns a Client that talks to the real Open-Meteo endpoint.
func NewClient(timezone string) *Client {
	return &Client{Timezone: timezone}
}

var _ WeatherProvider = (*Client)(nil)

// Daily fetches past_days=pastDays of history plus the forecast horizon for
// one coordinate, ascending by date. Callers label their own semantics: the
// normal refresh path always passes 7 (SPEC.md §10.1's self-healing window),
// a one-off backfill after a long outage passes something wider.
func (c *Client) Daily(ctx context.Context, lat, lon float64, pastDays int) ([]DailyWeather, error) {
	reqURL := c.requestURL(lat, lon, pastDays)

	body, err := fetchWithRetry(ctx, c.httpClient(), reqURL)
	if err != nil {
		return nil, fmt.Errorf("fetching open-meteo forecast: %w", err)
	}

	var resp forecastResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decoding open-meteo forecast: %w", err)
	}

	return toDailyWeather(resp, pastDays)
}

func (c *Client) requestURL(lat, lon float64, pastDays int) string {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("timezone", c.Timezone)
	q.Set("past_days", strconv.Itoa(pastDays))
	q.Set("forecast_days", strconv.Itoa(forecastDays))
	q.Set("daily", dailyParams)
	return c.baseURL() + "?" + q.Encode()
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// forecastResponse mirrors only the fields this package reads. Numeric daily
// arrays use pointers because Open-Meteo returns null for entries it cannot
// compute (observed data pending ingestion, forecast horizon edge cases);
// decoding into float64 directly would silently turn "unknown" into "zero".
type forecastResponse struct {
	Daily struct {
		Time                        []string   `json:"time"`
		ET0FAOEvapotranspiration    []*float64 `json:"et0_fao_evapotranspiration"`
		PrecipitationSum            []*float64 `json:"precipitation_sum"`
		PrecipitationProbabilityMax []*float64 `json:"precipitation_probability_max"`
		Temperature2mMin            []*float64 `json:"temperature_2m_min"`
		Temperature2mMax            []*float64 `json:"temperature_2m_max"`
	} `json:"daily"`
}

// toDailyWeather converts the response positionally against daily.time.
// Open-Meteo's past_days=N contract guarantees the first N entries are
// history and the rest are today plus the forecast horizon, in ascending
// date order, so the split is purely index-based.
func toDailyWeather(resp forecastResponse, pastDays int) ([]DailyWeather, error) {
	n := len(resp.Daily.Time)
	out := make([]DailyWeather, 0, n)
	for i := 0; i < n; i++ {
		date, err := parseCivilDate(resp.Daily.Time[i])
		if err != nil {
			return nil, err
		}
		kind := KindForecast
		if i < pastDays {
			kind = KindObserved
		}
		out = append(out, DailyWeather{
			Date:       date,
			Kind:       kind,
			ET0MM:      indexOrNil(resp.Daily.ET0FAOEvapotranspiration, i),
			PrecipMM:   indexOrNil(resp.Daily.PrecipitationSum, i),
			PrecipProb: indexOrNil(resp.Daily.PrecipitationProbabilityMax, i),
			TMinC:      indexOrNil(resp.Daily.Temperature2mMin, i),
			TMaxC:      indexOrNil(resp.Daily.Temperature2mMax, i),
		})
	}
	return out, nil
}

func indexOrNil(vals []*float64, i int) *float64 {
	if i < 0 || i >= len(vals) {
		return nil
	}
	return vals[i]
}

func parseCivilDate(s string) (domain.Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return domain.Date{}, fmt.Errorf("parsing open-meteo date %q: %w", s, err)
	}
	return domain.Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}, nil
}

// fetchWithRetry retries transient failures (network errors and 5xx) up to
// maxAttempts times total, with exponential backoff plus jitter. A 4xx is a
// client-side mistake in the request itself; retrying it cannot help.
func fetchWithRetry(ctx context.Context, client *http.Client, reqURL string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, retryable, err := fetchOnce(ctx, client, reqURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retryable || attempt == maxAttempts {
			return nil, lastErr
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoffWithJitter(attempt)):
		}
	}
	return nil, lastErr
}

func fetchOnce(ctx context.Context, client *http.Client, reqURL string) (body []byte, retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("building request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// The caller's context, not our per-attempt timeout, is done:
			// retrying cannot help and the outer select would just bail
			// anyway, but return promptly instead of spending a slot.
			return nil, false, err
		}
		return nil, true, fmt.Errorf("requesting open-meteo: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("reading open-meteo response: %w", err)
	}

	switch {
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("open-meteo status %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return nil, false, fmt.Errorf("open-meteo status %d: %s", resp.StatusCode, string(data))
	}
	return data, false, nil
}

func backoffWithJitter(attempt int) time.Duration {
	backoff := backoffBase * time.Duration(1<<uint(attempt-1))
	if backoff > backoffCap {
		backoff = backoffCap
	}
	jitter := time.Duration(rand.Int64N(int64(backoff/2) + 1))
	return backoff + jitter
}
