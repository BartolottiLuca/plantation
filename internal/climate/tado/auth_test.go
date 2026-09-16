package tado

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/store"
)

// --- test doubles -----------------------------------------------------

// fakeClock is an injectable Clock so expiry/staleness math is deterministic.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(now time.Time) *fakeClock { return &fakeClock{now: now} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// fakeSleeper stands in for the real poll delay: it records what it was
// asked to sleep for and returns immediately, so tests never wait 300s.
type fakeSleeper struct {
	mu    sync.Mutex
	calls []time.Duration
}

func (s *fakeSleeper) sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.mu.Lock()
	s.calls = append(s.calls, d)
	s.mu.Unlock()
	return nil
}

func (s *fakeSleeper) durations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.calls))
	copy(out, s.calls)
	return out
}

// fakeWriter is the transaction-scoped writer WithLock hands to fn. Writes
// only become visible to fakeRepo if fn returns nil and the (simulated)
// commit succeeds — see fakeRepo.WithLock.
type fakeWriter struct {
	written bool
	value   store.TadoToken
}

func (w *fakeWriter) Write(_ context.Context, tok store.TadoToken) error {
	w.written = true
	w.value = tok
	return nil
}

// fakeRepo is an in-memory TokenRepo. Its mutex is held for the whole
// duration of WithLock's callback — including the HTTP round trip inside
// it — which is exactly what SELECT ... FOR UPDATE gives the real
// implementation: a second caller blocks until the first has committed.
type fakeRepo struct {
	mu         sync.Mutex
	row        store.TadoToken
	loadErr    error
	failCommit bool
}

func (r *fakeRepo) Load(context.Context) (store.TadoToken, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loadErr != nil {
		return store.TadoToken{}, r.loadErr
	}
	return r.row, nil
}

func (r *fakeRepo) WithLock(ctx context.Context, fn func(ctx context.Context, current store.TadoToken, w store.TadoTokenWriter) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.row
	w := &fakeWriter{}
	if err := fn(ctx, current, w); err != nil {
		return err
	}
	if r.failCommit {
		// The Postgres analogue: fn returned nil (it thinks it wrote), but
		// the transaction's COMMIT fails, so nothing it staged is visible.
		return errors.New("simulated commit failure")
	}
	if w.written {
		r.row = w.value
	}
	return nil
}

func (r *fakeRepo) snapshot() store.TadoToken {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.row
}

func strp(s string) *string        { return &s }
func timep(t time.Time) *time.Time { return &t }

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshalling test response: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		t.Fatalf("writing test response: %v", err)
	}
}

// --- device flow --------------------------------------------------------

func TestStartDeviceFlow(t *testing.T) {
	clk := newFakeClock(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/device_authorize" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if got := r.FormValue("client_id"); got != clientID {
			t.Errorf("client_id = %q, want %q", got, clientID)
		}
		if got := r.FormValue("scope"); got != "offline_access" {
			t.Errorf("scope = %q, want offline_access", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"device_code":               "dc-123",
			"user_code":                 "ABCD-EFGH",
			"verification_uri":          "https://login.tado.com/oauth2/device",
			"verification_uri_complete": "https://login.tado.com/oauth2/device?user_code=ABCD-EFGH",
			"expires_in":                300,
			"interval":                  5,
		})
	}))
	defer srv.Close()

	c := NewClient(&fakeRepo{}, WithClock(clk))
	c.baseURL = srv.URL

	dc, err := c.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceFlow: %v", err)
	}
	if dc.UserCode != "ABCD-EFGH" {
		t.Errorf("UserCode = %q, want ABCD-EFGH", dc.UserCode)
	}
	wantURI := "https://login.tado.com/oauth2/device?user_code=ABCD-EFGH"
	if dc.VerificationURI != wantURI {
		t.Errorf("VerificationURI = %q, want %q", dc.VerificationURI, wantURI)
	}
	wantExpiry := clk.Now().Add(300 * time.Second)
	if !dc.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt = %v, want %v", dc.ExpiresAt, wantExpiry)
	}
	if dc.deviceCode != "dc-123" {
		t.Errorf("deviceCode = %q, want dc-123", dc.deviceCode)
	}
	if dc.interval != 5*time.Second {
		t.Errorf("interval = %v, want 5s", dc.interval)
	}
}

func TestWaitForLink_PendingThenSuccess(t *testing.T) {
	var pollCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if got := r.FormValue("device_code"); got != "dc-1" {
			t.Errorf("device_code = %q, want dc-1", got)
		}
		if got := r.FormValue("grant_type"); got != "urn:ietf:params:oauth:grant-type:device_code" {
			t.Errorf("grant_type = %q", got)
		}
		n := atomic.AddInt32(&pollCount, 1)
		if n == 1 {
			writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "authorization_pending"})
			return
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 600,
		})
	}))
	defer srv.Close()

	repo := &fakeRepo{}
	sleeper := &fakeSleeper{}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL
	c.sleep = sleeper.sleep

	dc := DeviceCode{
		UserCode:        "code",
		VerificationURI: "https://example.test",
		ExpiresAt:       c.clock.Now().Add(300 * time.Second),
		deviceCode:      "dc-1",
		interval:        5 * time.Second,
	}

	if err := c.WaitForLink(context.Background(), dc); err != nil {
		t.Fatalf("WaitForLink: %v", err)
	}
	if got := atomic.LoadInt32(&pollCount); got != 2 {
		t.Fatalf("poll count = %d, want 2", got)
	}

	row := repo.snapshot()
	if row.AccessToken == nil || *row.AccessToken != "at-1" {
		t.Errorf("AccessToken = %v, want at-1", row.AccessToken)
	}
	if row.RefreshToken == nil || *row.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %v, want rt-1", row.RefreshToken)
	}
	if row.State != store.TadoLinked {
		t.Errorf("State = %q, want %q", row.State, store.TadoLinked)
	}

	durations := sleeper.durations()
	if len(durations) != 2 {
		t.Fatalf("sleep calls = %v, want 2 calls", durations)
	}
	for i, d := range durations {
		if d != 5*time.Second {
			t.Errorf("sleep[%d] = %v, want 5s", i, d)
		}
	}
}

func TestWaitForLink_SlowDownWidensInterval(t *testing.T) {
	var seq int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch atomic.AddInt32(&seq, 1) {
		case 1:
			writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "slow_down"})
		case 2:
			writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "authorization_pending"})
		default:
			writeJSON(t, w, http.StatusOK, map[string]interface{}{
				"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 600,
			})
		}
	}))
	defer srv.Close()

	repo := &fakeRepo{}
	sleeper := &fakeSleeper{}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL
	c.sleep = sleeper.sleep

	dc := DeviceCode{
		ExpiresAt:  c.clock.Now().Add(300 * time.Second),
		deviceCode: "dc-2",
		interval:   5 * time.Second,
	}

	if err := c.WaitForLink(context.Background(), dc); err != nil {
		t.Fatalf("WaitForLink: %v", err)
	}

	want := []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}
	got := sleeper.durations()
	if len(got) != len(want) {
		t.Fatalf("sleep durations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestWaitForLink_ExpiredToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "expired_token"})
	}))
	defer srv.Close()

	c := NewClient(&fakeRepo{}, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL
	c.sleep = (&fakeSleeper{}).sleep

	dc := DeviceCode{ExpiresAt: c.clock.Now().Add(300 * time.Second), deviceCode: "dc-3", interval: 5 * time.Second}
	err := c.WaitForLink(context.Background(), dc)
	if !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("err = %v, want ErrDeviceCodeExpired", err)
	}
}

func TestWaitForLink_AccessDenied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "access_denied"})
	}))
	defer srv.Close()

	c := NewClient(&fakeRepo{}, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL
	c.sleep = (&fakeSleeper{}).sleep

	dc := DeviceCode{ExpiresAt: c.clock.Now().Add(300 * time.Second), deviceCode: "dc-4", interval: 5 * time.Second}
	err := c.WaitForLink(context.Background(), dc)
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("err = %v, want ErrAccessDenied", err)
	}
}

func TestWaitForLink_ClientSideExpiryStopsPollingWithoutACall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("token endpoint must not be called once the device code has expired")
	}))
	defer srv.Close()

	clk := newFakeClock(time.Now())
	c := NewClient(&fakeRepo{}, WithClock(clk))
	c.baseURL = srv.URL
	c.sleep = (&fakeSleeper{}).sleep

	dc := DeviceCode{ExpiresAt: clk.Now().Add(-time.Second), deviceCode: "dc-5", interval: 5 * time.Second}
	err := c.WaitForLink(context.Background(), dc)
	if !errors.Is(err, ErrDeviceCodeExpired) {
		t.Fatalf("err = %v, want ErrDeviceCodeExpired", err)
	}
}

func TestWaitForLink_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("token endpoint must not be called after ctx cancellation")
	}))
	defer srv.Close()

	c := NewClient(&fakeRepo{}, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL // default sleep (ctxSleep) is used deliberately here

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dc := DeviceCode{ExpiresAt: c.clock.Now().Add(300 * time.Second), deviceCode: "dc-6", interval: 5 * time.Second}
	err := c.WaitForLink(ctx, dc)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestWaitForLink_CommitFailureReturnsErrTokenLost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": "at-7", "refresh_token": "rt-7", "expires_in": 600,
		})
	}))
	defer srv.Close()

	repo := &fakeRepo{failCommit: true}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL
	c.sleep = (&fakeSleeper{}).sleep

	dc := DeviceCode{ExpiresAt: c.clock.Now().Add(300 * time.Second), deviceCode: "dc-7", interval: 5 * time.Second}
	err := c.WaitForLink(context.Background(), dc)
	if !errors.Is(err, ErrTokenLost) {
		t.Fatalf("err = %v, want ErrTokenLost", err)
	}
	if row := repo.snapshot(); row.AccessToken != nil {
		t.Errorf("AccessToken = %v, want unchanged (nil, never linked)", row.AccessToken)
	}
}

// --- refresh --------------------------------------------------------------

func TestRefresh_SuccessRotatesToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		if got := r.FormValue("grant_type"); got != "refresh_token" {
			t.Fatalf("grant_type = %q, want refresh_token", got)
		}
		if got := r.FormValue("refresh_token"); got != "old-refresh" {
			t.Fatalf("refresh_token = %q, want old-refresh", got)
		}
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 600,
		})
	}))
	defer srv.Close()

	home := "home-1"
	repo := &fakeRepo{row: store.TadoToken{
		AccessToken:       strp("old-access"),
		AccessExpiresAt:   timep(time.Now()),
		RefreshToken:      strp("old-refresh"),
		RefreshObtainedAt: timep(time.Now().Add(-24 * time.Hour)),
		HomeID:            &home,
		State:             store.TadoLinked,
	}}
	clk := newFakeClock(time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC))
	c := NewClient(repo, WithClock(clk))
	c.baseURL = srv.URL

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	row := repo.snapshot()
	if row.PreviousRefreshToken == nil || *row.PreviousRefreshToken != "old-refresh" {
		t.Errorf("PreviousRefreshToken = %v, want old-refresh", row.PreviousRefreshToken)
	}
	if row.RefreshToken == nil || *row.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %v, want new-refresh", row.RefreshToken)
	}
	if row.AccessToken == nil || *row.AccessToken != "new-access" {
		t.Errorf("AccessToken = %v, want new-access", row.AccessToken)
	}
	if row.HomeID == nil || *row.HomeID != "home-1" {
		t.Errorf("HomeID = %v, want preserved home-1", row.HomeID)
	}
	if row.State != store.TadoLinked {
		t.Errorf("State = %q, want linked", row.State)
	}
	if row.RefreshObtainedAt == nil || !row.RefreshObtainedAt.Equal(clk.Now()) {
		t.Errorf("RefreshObtainedAt = %v, want %v", row.RefreshObtainedAt, clk.Now())
	}
}

func TestRefresh_InvalidGrantDrivesNeedsReauth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "invalid_grant"})
	}))
	defer srv.Close()

	repo := &fakeRepo{row: store.TadoToken{
		RefreshToken: strp("old-refresh"),
		State:        store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL

	err := c.Refresh(context.Background())
	if !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("err = %v, want ErrNeedsReauth", err)
	}
	if got := repo.snapshot().State; got != store.TadoNeedsReauth {
		t.Errorf("State = %q, want needs_reauth", got)
	}
}

func TestRefresh_NoRefreshTokenOnRecord(t *testing.T) {
	repo := &fakeRepo{row: store.TadoToken{State: store.TadoUnlinked}}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = "http://unused.invalid"

	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh: want error when there is no refresh token, got nil")
	}
}

// TestRefresh_CommitFailureLeavesOldTokenInPlace pins the commit-ordering
// contract: the token endpoint succeeds, but the WithLock commit fails, so
// the caller must get an error and the previously stored token must be
// exactly what it was before the call (the failed transaction must not
// partially apply).
func TestRefresh_CommitFailureLeavesOldTokenInPlace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 600,
		})
	}))
	defer srv.Close()

	repo := &fakeRepo{
		row: store.TadoToken{
			AccessToken:  strp("old-access"),
			RefreshToken: strp("old-refresh"),
			State:        store.TadoLinked,
		},
		failCommit: true,
	}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL

	err := c.Refresh(context.Background())
	if !errors.Is(err, ErrTokenLost) {
		t.Fatalf("err = %v, want ErrTokenLost", err)
	}

	row := repo.snapshot()
	if row.AccessToken == nil || *row.AccessToken != "old-access" {
		t.Errorf("AccessToken = %v, want unchanged old-access", row.AccessToken)
	}
	if row.RefreshToken == nil || *row.RefreshToken != "old-refresh" {
		t.Errorf("RefreshToken = %v, want unchanged old-refresh", row.RefreshToken)
	}
}

// TestRefresh_ConcurrentNoDoubleSpend proves that two goroutines refreshing
// at once cannot spend the same rotating refresh token twice, and that both
// converge on the same final token. The fake repo's mutex is held for the
// whole WithLock callback (HTTP call included), modelling Postgres's
// SELECT ... FOR UPDATE.
func TestRefresh_ConcurrentNoDoubleSpend(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	seq := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parsing form: %v", err)
		}
		rt := r.FormValue("refresh_token")

		mu.Lock()
		seen[rt]++
		count := seen[rt]
		if count > 1 {
			mu.Unlock()
			writeJSON(t, w, http.StatusBadRequest, map[string]interface{}{"error": "invalid_grant"})
			return
		}
		seq++
		access := fmt.Sprintf("access-%d", seq)
		refresh := fmt.Sprintf("refresh-%d", seq)
		mu.Unlock()

		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": access, "refresh_token": refresh, "expires_in": 600,
		})
	}))
	defer srv.Close()

	repo := &fakeRepo{row: store.TadoToken{
		RefreshToken: strp("refresh-0"),
		State:        store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = c.Refresh(context.Background())
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: Refresh: %v", i, err)
		}
	}

	mu.Lock()
	for token, count := range seen {
		if count != 1 {
			t.Errorf("refresh token %q spent %d times, want exactly 1", token, count)
		}
	}
	distinct := len(seen)
	mu.Unlock()
	if distinct != 2 {
		t.Fatalf("distinct refresh tokens spent = %d, want 2 (refresh-0, then whatever it rotated to)", distinct)
	}

	final := repo.snapshot()
	if final.AccessToken == nil || *final.AccessToken != "access-2" {
		t.Errorf("final AccessToken = %v, want access-2 (both goroutines converge on the second rotation)", final.AccessToken)
	}
}

// --- refresh policy ---------------------------------------------------

func TestNeedsRefresh(t *testing.T) {
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	c := NewClient(&fakeRepo{})

	cases := []struct {
		name string
		tok  store.TadoToken
		want bool
	}{
		{
			name: "no access expiry recorded",
			tok:  store.TadoToken{RefreshObtainedAt: timep(now)},
			want: true,
		},
		{
			name: "access token expiring in 4 minutes",
			tok:  store.TadoToken{AccessExpiresAt: timep(now.Add(4 * time.Minute)), RefreshObtainedAt: timep(now)},
			want: true,
		},
		{
			name: "access token expiring in exactly 5 minutes",
			tok:  store.TadoToken{AccessExpiresAt: timep(now.Add(5 * time.Minute)), RefreshObtainedAt: timep(now)},
			want: true,
		},
		{
			name: "access token fresh, refresh token fresh",
			tok:  store.TadoToken{AccessExpiresAt: timep(now.Add(10 * time.Minute)), RefreshObtainedAt: timep(now)},
			want: false,
		},
		{
			name: "no refresh_obtained_at recorded",
			tok:  store.TadoToken{AccessExpiresAt: timep(now.Add(10 * time.Minute))},
			want: true,
		},
		{
			name: "refresh obtained exactly 7 days ago",
			tok: store.TadoToken{
				AccessExpiresAt:   timep(now.Add(10 * time.Minute)),
				RefreshObtainedAt: timep(now.Add(-7 * 24 * time.Hour)),
			},
			want: true,
		},
		{
			name: "refresh obtained 6 days ago",
			tok: store.TadoToken{
				AccessExpiresAt:   timep(now.Add(10 * time.Minute)),
				RefreshObtainedAt: timep(now.Add(-6 * 24 * time.Hour)),
			},
			want: false,
		},
		{
			name: "refresh obtained 8 days ago",
			tok: store.TadoToken{
				AccessExpiresAt:   timep(now.Add(10 * time.Minute)),
				RefreshObtainedAt: timep(now.Add(-8 * 24 * time.Hour)),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.NeedsRefresh(tc.tok, now); got != tc.want {
				t.Errorf("NeedsRefresh() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- status -----------------------------------------------------------

func TestStatus_MapsStoredToken(t *testing.T) {
	home := "home-42"
	accessExp := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	refreshObtained := time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC)
	repo := &fakeRepo{row: store.TadoToken{
		HomeID:            &home,
		AccessExpiresAt:   &accessExp,
		RefreshObtainedAt: &refreshObtained,
		State:             store.TadoLinked,
	}}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))

	got := c.Status(context.Background())
	want := climate.LinkStatus{
		State:             climate.StateLinked,
		HomeID:            "home-42",
		AccessExpiresAt:   accessExp,
		RefreshObtainedAt: refreshObtained,
	}
	if got != want {
		t.Errorf("Status() = %+v, want %+v", got, want)
	}
}

func TestStatus_NilFieldsBecomeZeroValues(t *testing.T) {
	repo := &fakeRepo{row: store.TadoToken{State: store.TadoUnlinked}}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))

	got := c.Status(context.Background())
	want := climate.LinkStatus{State: climate.StateUnlinked}
	if got != want {
		t.Errorf("Status() = %+v, want %+v", got, want)
	}
}

func TestStatus_LoadErrorReportsUnlinkedWithoutPanicOrError(t *testing.T) {
	repo := &fakeRepo{loadErr: errors.New("db down")}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))

	got := c.Status(context.Background())
	want := climate.LinkStatus{State: climate.StateUnlinked}
	if got != want {
		t.Errorf("Status() = %+v, want %+v", got, want)
	}
}

// --- no token leaks -----------------------------------------------------

// TestNoTokenLeakInLogsOrErrors seeds a distinctive token value through both
// the fake server and the stored row, forces the one error/log path that
// exists in this package (a post-exchange commit failure), and asserts the
// marker never appears in the returned error string, in Status(), or in any
// log line.
func TestNoTokenLeakInLogsOrErrors(t *testing.T) {
	const secretMarker = "SEKRIT-TOKEN-VALUE-DO-NOT-LEAK"

	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	defer slog.SetDefault(prevLogger)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]interface{}{
			"access_token": secretMarker, "refresh_token": secretMarker, "expires_in": 600,
		})
	}))
	defer srv.Close()

	repo := &fakeRepo{
		row: store.TadoToken{
			RefreshToken: strp(secretMarker),
			State:        store.TadoLinked,
		},
		failCommit: true,
	}
	c := NewClient(repo, WithClock(newFakeClock(time.Now())))
	c.baseURL = srv.URL

	err := c.Refresh(context.Background())
	if err == nil {
		t.Fatal("Refresh: want an error from the forced commit failure, got nil")
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Errorf("error string contains token material: %v", err)
	}
	if strings.Contains(logBuf.String(), secretMarker) {
		t.Errorf("log output contains token material:\n%s", logBuf.String())
	}

	status := c.Status(context.Background())
	if strings.Contains(fmt.Sprintf("%+v", status), secretMarker) {
		t.Errorf("Status() contains token material: %+v", status)
	}
}
