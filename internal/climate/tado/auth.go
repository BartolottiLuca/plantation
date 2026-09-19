// Package tado implements the Tado X device-code OAuth flow and the rotating
// refresh-token lifecycle described in SPEC.md §10.2.
//
// State machine (persisted in tado_token.state via internal/store):
//
//	unlinked --successful device flow (StartDeviceFlow + WaitForLink)--> linked
//	linked --refresh or poll response invalid_grant--> needs_reauth
//	needs_reauth --successful device flow--> linked
//
// There is no path back to unlinked once a device flow has completed once;
// only a human completing the device flow again can leave needs_reauth.
//
// This package never calls hops.tado.com or my.tado.com (that is C07's
// concern — it only takes a token) and never renders anything (that is
// C11's concern — it only exposes the data a settings page needs).
package tado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/store"
)

// clientID is Tado's public device-flow client id (SPEC.md §10.2). It is not
// a secret, but token and code values obtained through it are, and must
// never be logged.
const clientID = "1bb50063-6b0c-4d11-bd99-387f4a91cc46"

const (
	defaultBaseURL      = "https://login.tado.com"
	defaultPollInterval = 5 * time.Second
	slowDownIncrement   = 5 * time.Second
	refreshBeforeExpiry = 5 * time.Minute
	refreshMaxAge       = 7 * 24 * time.Hour
	maxResponseBytes    = 1 << 16
	httpTimeout         = 10 * time.Second
)

// Sentinel errors a caller can match on with errors.Is. None of them carry
// token material.
var (
	// ErrDeviceCodeExpired is returned by WaitForLink when the 300s device
	// code lifetime elapses before the user authorizes the app.
	ErrDeviceCodeExpired = errors.New("tado: device code expired before authorization")
	// ErrAccessDenied is returned by WaitForLink when the user declines
	// authorization at the verification URL.
	ErrAccessDenied = errors.New("tado: user denied authorization")
	// ErrNeedsReauth is returned by Refresh (and by WaitForLink, though that
	// path should be rare) when Tado reports invalid_grant. The state row
	// has already been committed as needs_reauth by the time this is
	// returned; a caller (C09/C11) should raise ops:tado_reauth_soon /
	// link to /settings/tado and stop retrying until a human re-links.
	ErrNeedsReauth = errors.New("tado: refresh token rejected, needs re-authentication")
	// ErrTokenLost is returned when the token endpoint returned a new,
	// valid token but committing it to Postgres failed. The old token may
	// still be usable for a short time, but the rotation is now unknown to
	// this process; a caller should raise ops:tado_token_lost immediately.
	ErrTokenLost = errors.New("tado: token exchange succeeded but commit failed (ops:tado_token_lost)")
)

// Clock is the seam for time in this package; nothing here calls time.Now
// directly. Production wiring (cmd) may pass any implementation that
// satisfies this, including internal/care.Clock via a trivial adapter.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// sleepFunc is the seam for the device-flow poll delay so tests never sleep
// for real. It must honour ctx cancellation.
type sleepFunc func(ctx context.Context, d time.Duration) error

func ctxSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// TokenRepo is the persistence seam this package depends on. Its method set
// matches *store.TadoTokenRepo exactly, so the concrete repo satisfies it
// with no adapter; tests fake it with an in-memory row and a real mutex to
// model Postgres's SELECT ... FOR UPDATE.
type TokenRepo interface {
	Load(ctx context.Context) (store.TadoToken, error)
	WithLock(ctx context.Context, fn func(ctx context.Context, current store.TadoToken, w store.TadoTokenWriter) error) error
}

// Client is the Tado OAuth client. Construct with NewClient; the zero value
// is not usable.
type Client struct {
	repo       TokenRepo
	httpClient *http.Client
	clock      Clock
	sleep      sleepFunc
	baseURL    string
}

// Option configures a Client constructed by NewClient.
type Option func(*Client)

// WithHTTPClient overrides the default HTTP client (10s timeout). Callers
// supplying their own client are still responsible for setting a timeout.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithClock overrides the default (real) clock. Tests use this to make
// expiry and staleness checks deterministic.
func WithClock(clk Clock) Option {
	return func(c *Client) { c.clock = clk }
}

// NewClient builds a Tado OAuth client backed by repo. Only cmd/plantation
// constructs the concrete *store.TadoTokenRepo passed in here; everything
// else should depend on the IndoorClimateProvider interface or on TokenRepo.
func NewClient(repo TokenRepo, opts ...Option) *Client {
	c := &Client{
		repo:       repo,
		httpClient: &http.Client{Timeout: httpTimeout},
		clock:      realClock{},
		sleep:      ctxSleep,
		baseURL:    defaultBaseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// DeviceCode is what StartDeviceFlow hands back to a caller (C11) so it can
// render UserCode and VerificationURI to a human, and then pass the same
// value to WaitForLink to poll for completion. Only the exported fields are
// safe to render or log; the raw device_code Tado issued is kept unexported
// and is never marshalled to JSON.
type DeviceCode struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time

	deviceCode string
	interval   time.Duration
}

type deviceAuthorizeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// StartDeviceFlow begins the Tado device-code flow by requesting a fresh
// device/user code pair. It does not block waiting for the user.
//
// Contract for C11: render DeviceCode.UserCode and DeviceCode.VerificationURI
// to the user immediately, then pass the returned DeviceCode to WaitForLink
// (from a goroutine or a follow-up request — WaitForLink blocks for up to
// ExpiresAt) to complete the link. Do not persist or log the DeviceCode
// across process restarts; if the process restarts mid-flow, start over.
func (c *Client) StartDeviceFlow(ctx context.Context) (DeviceCode, error) {
	var resp deviceAuthorizeResponse
	if err := c.postForm(ctx, "/oauth2/device_authorize", url.Values{
		"client_id": {clientID},
		"scope":     {"offline_access"},
	}, &resp); err != nil {
		return DeviceCode{}, fmt.Errorf("starting tado device flow: %w", err)
	}
	if resp.DeviceCode == "" || resp.UserCode == "" {
		return DeviceCode{}, errors.New("starting tado device flow: empty device_authorize response")
	}

	verificationURI := resp.VerificationURIComplete
	if verificationURI == "" {
		verificationURI = resp.VerificationURI
	}
	interval := time.Duration(resp.Interval) * time.Second
	if interval <= 0 {
		interval = defaultPollInterval
	}
	expiresIn := time.Duration(resp.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 300 * time.Second
	}

	return DeviceCode{
		UserCode:        resp.UserCode,
		VerificationURI: verificationURI,
		ExpiresAt:       c.clock.Now().Add(expiresIn),
		deviceCode:      resp.DeviceCode,
		interval:        interval,
	}, nil
}

// WaitForLink polls Tado's token endpoint with the device code from
// StartDeviceFlow until the user authorizes the app, the code expires, the
// user denies access, or ctx is cancelled.
//
// Contract for C11: call this once, after StartDeviceFlow, from a background
// goroutine or a request that can afford to block — it does not return until
// one of those four outcomes occurs. On success the token is already
// committed (state=linked) by the time this returns nil; the caller only
// needs to tell the user "linked" and stop rendering the code.
//
// Returned errors: ErrDeviceCodeExpired, ErrAccessDenied, ctx.Err(), or a
// wrapped ErrTokenLost if the exchange succeeded but the commit did not.
func (c *Client) WaitForLink(ctx context.Context, dc DeviceCode) error {
	interval := dc.interval
	if interval <= 0 {
		interval = defaultPollInterval
	}

	for {
		if err := c.sleep(ctx, interval); err != nil {
			return err
		}
		if !dc.ExpiresAt.IsZero() && c.clock.Now().After(dc.ExpiresAt) {
			return ErrDeviceCodeExpired
		}

		var tr tokenResponse
		if err := c.postForm(ctx, "/oauth2/token", url.Values{
			"client_id":   {clientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dc.deviceCode},
		}, &tr); err != nil {
			return fmt.Errorf("polling tado device flow: %w", err)
		}

		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return errors.New("polling tado device flow: empty token response")
			}
			// Any failure here — whether w.Write itself returns an error, or
			// (as with a real Postgres tx) the commit fails after fn already
			// returned nil — means the exchange succeeded but the rotation
			// did not land, which is exactly ErrTokenLost regardless of
			// which half failed.
			if err := c.repo.WithLock(ctx, func(ctx context.Context, current store.TadoToken, w store.TadoTokenWriter) error {
				return c.writeNewToken(ctx, w, current, tr)
			}); err != nil {
				slog.Error("tado token commit failed after successful device-flow exchange", "op", "tado_device_flow_commit")
				return fmt.Errorf("%w: %w", ErrTokenLost, err)
			}
			return nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += slowDownIncrement
			continue
		case "expired_token":
			return ErrDeviceCodeExpired
		case "access_denied":
			return ErrAccessDenied
		default:
			return fmt.Errorf("tado device flow error: %s", tr.Error)
		}
	}
}

// NeedsRefresh reports whether current should be refreshed now: proactively
// when the access token is within 5 minutes of expiry, and unconditionally
// at least once every 7 days regardless of access token state, so a holiday
// with nobody asking for indoor data never runs into Tado's 30-day
// inactivity expiry on the refresh token.
func (c *Client) NeedsRefresh(current store.TadoToken, now time.Time) bool {
	expiringSoon := current.AccessExpiresAt == nil || !current.AccessExpiresAt.After(now.Add(refreshBeforeExpiry))
	stale := current.RefreshObtainedAt == nil || now.Sub(*current.RefreshObtainedAt) >= refreshMaxAge
	return expiringSoon || stale
}

// Refresh rotates the stored refresh token via Tado's token endpoint. It is
// safe to call concurrently — including from a scheduler tick and an HTTP
// handler at the same moment — because the whole exchange-then-commit
// sequence runs inside TokenRepo.WithLock, which holds Postgres's
// SELECT ... FOR UPDATE for its duration; a second caller blocks until the
// first has committed and then rotates from the token the first one just
// wrote, so a given refresh token is spent at most once.
//
// The access token committed by a successful call is not usable by anyone
// until this function returns nil: the token endpoint is called first, but
// the commit (via WithLock) happens before Refresh returns.
//
// Returned errors: ErrNeedsReauth (state has already been committed as
// needs_reauth), a wrapped ErrTokenLost (exchange succeeded, commit did
// not — the old token is still the one on record), or a plain error for any
// other failure, in which case nothing was committed.
func (c *Client) Refresh(ctx context.Context) error {
	var needsReauth bool
	// tokenExchanged is set once the token endpoint has handed back a fresh,
	// usable token. From that point on, any error out of WithLock — whether
	// from w.Write itself or from the transaction failing to commit after
	// fn returned nil — means the exchange succeeded but the rotation did
	// not land, which is ErrTokenLost regardless of which half failed.
	var tokenExchanged bool

	err := c.repo.WithLock(ctx, func(ctx context.Context, current store.TadoToken, w store.TadoTokenWriter) error {
		if current.RefreshToken == nil || *current.RefreshToken == "" {
			return fmt.Errorf("tado: cannot refresh: no refresh token on record (state=%s)", current.State)
		}

		var tr tokenResponse
		if err := c.postForm(ctx, "/oauth2/token", url.Values{
			"client_id":     {clientID},
			"grant_type":    {"refresh_token"},
			"refresh_token": {*current.RefreshToken},
		}, &tr); err != nil {
			return fmt.Errorf("calling tado token endpoint: %w", err)
		}

		if tr.Error == "invalid_grant" {
			reauth := current
			reauth.State = store.TadoNeedsReauth
			if err := w.Write(ctx, reauth); err != nil {
				return fmt.Errorf("recording tado needs_reauth state: %w", err)
			}
			needsReauth = true
			return nil
		}
		if tr.Error != "" {
			return fmt.Errorf("tado refresh error: %s", tr.Error)
		}
		if tr.AccessToken == "" {
			return errors.New("tado refresh: empty token response")
		}

		tokenExchanged = true
		return c.writeNewToken(ctx, w, current, tr)
	})

	if tokenExchanged && err != nil {
		slog.Error("tado token commit failed after successful refresh exchange", "op", "tado_refresh_commit")
		return fmt.Errorf("%w: %w", ErrTokenLost, err)
	}
	if err != nil {
		return err
	}
	if needsReauth {
		return ErrNeedsReauth
	}
	return nil
}

// writeNewToken stages a successful token exchange (device flow or refresh)
// through w. current.PreviousRefreshToken is deliberately not preserved
// further back than one generation — current.RefreshToken (the value now
// being replaced) becomes the new PreviousRefreshToken, and whatever was
// there before is dropped. Callers are responsible for turning a non-nil
// return from the enclosing WithLock call into ErrTokenLost: this function
// cannot see a commit failure that happens after it returns nil.
func (c *Client) writeNewToken(ctx context.Context, w store.TadoTokenWriter, current store.TadoToken, tr tokenResponse) error {
	now := c.clock.Now()
	accessToken := tr.AccessToken
	refreshToken := tr.RefreshToken
	expiresAt := now.Add(time.Duration(tr.ExpiresIn) * time.Second)

	newTok := store.TadoToken{
		AccessToken:          &accessToken,
		AccessExpiresAt:      &expiresAt,
		RefreshToken:         &refreshToken,
		PreviousRefreshToken: current.RefreshToken,
		RefreshObtainedAt:    &now,
		HomeID:               current.HomeID,
		State:                store.TadoLinked,
	}
	return w.Write(ctx, newTok)
}

// Status reports link state for the diagnostics page. It never returns an
// error and never includes token material: on a load failure it logs (no
// token, no error detail that could contain one) and reports unlinked.
func (c *Client) Status(ctx context.Context) climate.LinkStatus {
	tok, err := c.repo.Load(ctx)
	if err != nil {
		slog.Error("loading tado token status failed", "op", "tado_status")
		return climate.LinkStatus{State: climate.StateUnlinked}
	}

	status := climate.LinkStatus{State: tok.State}
	if tok.HomeID != nil {
		status.HomeID = *tok.HomeID
	}
	if tok.AccessExpiresAt != nil {
		status.AccessExpiresAt = *tok.AccessExpiresAt
	}
	if tok.RefreshObtainedAt != nil {
		status.RefreshObtainedAt = *tok.RefreshObtainedAt
	}
	return status
}

// postForm POSTs a form-encoded body to path (relative to c.baseURL) and
// decodes a JSON response body into out. Tado's token endpoint returns JSON
// error bodies (e.g. {"error":"authorization_pending"}) on non-2xx statuses
// too, so this does not treat a non-2xx status as an error by itself —
// callers inspect the decoded body's Error field.
func (c *Client) postForm(ctx context.Context, path string, form url.Values, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling tado: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading tado response: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("empty tado response (status %d)", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding tado response (status %d): %w", resp.StatusCode, err)
	}
	return nil
}
