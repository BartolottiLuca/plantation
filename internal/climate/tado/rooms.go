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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/store"
)

const (
	defaultMeURL       = "https://my.tado.com/api/v2/me"
	defaultHopsBaseURL = "https://hops.tado.com"

	sampleBucket     = 30 * time.Minute
	debugRingSize    = 5
	debugBodyCap     = 8 << 10
	maxFetchAttempts = 3
	retryBackoff     = 50 * time.Millisecond

	minTempC    = 0.0
	maxTempC    = 45.0
	minHumidity = 10.0
	maxHumidity = 95.0
)

// ClimateStore is the persistence seam Sample writes through.
// *store.ClimateRepo satisfies it with no adapter.
type ClimateStore interface {
	AddSamples(ctx context.Context, samples []store.ClimateSample) error
}

// PlantRooms is the plant list Sample uses to decide which rooms to persist.
// *store.PlantRepo satisfies it with no adapter.
type PlantRooms interface {
	List(ctx context.Context) ([]store.PlantWithSpecies, error)
}

// Sampler turns a linked Tado X account into per-room climate readings.
//
// hops.tado.com is undocumented and unversioned, so every response is treated
// as hostile: unexpected shapes, missing sensors, and HTTP failures log and
// degrade to "no data" — Rooms and Sample never return a non-nil error for
// those. Sensor faults (T ∉ [0, 45] °C or RH ∉ [10, 95] %) are dropped: they
// are not returned by Rooms and are not persisted. The care engine already
// treats missing indoor data as f_dry=1.0 with IndoorDataStale (SPEC.md §7.3).
type Sampler struct {
	auth       *Client
	climate    ClimateStore
	plants     PlantRooms
	httpClient *http.Client
	clock      Clock
	meURL      string
	hopsBase   string
	sleep      sleepFunc
	debug      *debugRing
}

// SamplerOption configures a Sampler constructed by NewSampler.
type SamplerOption func(*Sampler)

// WithSamplerHTTPClient overrides the default HTTP client (10s timeout).
func WithSamplerHTTPClient(hc *http.Client) SamplerOption {
	return func(s *Sampler) { s.httpClient = hc }
}

// WithSamplerClock overrides the clock inherited from the auth Client.
func WithSamplerClock(clk Clock) SamplerOption {
	return func(s *Sampler) { s.clock = clk }
}

// WithMeURL overrides GET /api/v2/me. Tests point this at httptest.
func WithMeURL(raw string) SamplerOption {
	return func(s *Sampler) { s.meURL = raw }
}

// WithHopsBaseURL overrides the hops.tado.com origin. Rooms are fetched from
// {base}/homes/{homeId}/rooms.
func WithHopsBaseURL(raw string) SamplerOption {
	return func(s *Sampler) { s.hopsBase = raw }
}

// NewSampler builds a rooms/climate sampler that borrows tokens from auth.
// climate and plants may be nil when the caller only needs Rooms/Status
// (the plant-form picker); Sample is then a no-op.
func NewSampler(auth *Client, climate ClimateStore, plants PlantRooms, opts ...SamplerOption) *Sampler {
	s := &Sampler{
		auth:       auth,
		climate:    climate,
		plants:     plants,
		httpClient: &http.Client{Timeout: httpTimeout},
		meURL:      defaultMeURL,
		hopsBase:   defaultHopsBaseURL,
		sleep:      ctxSleep,
		debug:      newDebugRing(debugRingSize),
	}
	if auth != nil {
		s.clock = auth.clock
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: httpTimeout}
	}
	if s.sleep == nil {
		s.sleep = ctxSleep
	}
	if s.debug == nil {
		s.debug = newDebugRing(debugRingSize)
	}
	if s.meURL == "" {
		s.meURL = defaultMeURL
	}
	if s.hopsBase == "" {
		s.hopsBase = defaultHopsBaseURL
	}
	return s
}

var (
	_ climate.IndoorClimateProvider = (*Sampler)(nil)
	_ ClimateStore                  = (*store.ClimateRepo)(nil)
	_ PlantRooms                    = (*store.PlantRepo)(nil)
)

// Rooms returns one current reading per room with a usable in-range sensor.
// Unlinked, needs_reauth, and any Tado/JSON failure yield an empty slice and
// a nil error.
func (s *Sampler) Rooms(ctx context.Context) ([]climate.RoomClimate, error) {
	empty := []climate.RoomClimate{}
	if s == nil || s.auth == nil {
		return empty, nil
	}
	tok, ok := s.ensureReady(ctx)
	if !ok {
		return empty, nil
	}
	homeID, ok := s.ensureHomeID(ctx, tok)
	if !ok {
		return empty, nil
	}
	tok, err := s.auth.repo.Load(ctx)
	if err != nil {
		slog.Error("loading tado token failed", "op", "tado_rooms")
		return empty, nil
	}
	if tok.State != store.TadoLinked || tok.AccessToken == nil || *tok.AccessToken == "" {
		return empty, nil
	}
	body, ok := s.getAuthed(ctx, s.roomsURL(homeID), tok)
	if !ok {
		slog.Warn("tado rooms request failed", "op", "tado_rooms")
		return empty, nil
	}
	rooms := parseRoomsBody(body, s.now())
	if rooms == nil {
		return empty, nil
	}
	return rooms, nil
}

// Sample persists one ClimateSample per room that has at least one active
// plant mapped to it. ObservedAt is truncated to a 30-minute UTC bucket so a
// double tick cannot double-insert (the store PK is (tado_room_id, observed_at)).
func (s *Sampler) Sample(ctx context.Context) error {
	if s == nil {
		return nil
	}
	rooms, err := s.Rooms(ctx)
	if err != nil || len(rooms) == 0 {
		return nil
	}
	if s.plants == nil || s.climate == nil {
		return nil
	}
	mapped, ok := s.mappedRoomIDs(ctx)
	if !ok {
		return nil
	}
	samples := make([]store.ClimateSample, 0, len(rooms))
	for _, r := range rooms {
		if _, yes := mapped[r.RoomID]; !yes {
			continue
		}
		samples = append(samples, store.ClimateSample{
			RoomID:      r.RoomID,
			ObservedAt:  bucketUTC(r.ObservedAt),
			TempC:       r.TempC,
			HumidityPct: r.HumidityPct,
		})
	}
	if len(samples) == 0 {
		return nil
	}
	if err := s.climate.AddSamples(ctx, samples); err != nil {
		slog.Error("persisting climate samples failed", "op", "tado_sample")
		return nil
	}
	return nil
}

// Status delegates to the auth client so diagnostics stay honest when the
// account is unlinked or needs_reauth.
func (s *Sampler) Status(ctx context.Context) climate.LinkStatus {
	if s == nil || s.auth == nil {
		return climate.LinkStatus{State: climate.StateUnlinked}
	}
	return s.auth.Status(ctx)
}

// LastDebugBodies returns the most recent raw Tado response bodies (oldest
// first), each capped at 8KiB. Intended for tests and shape-change debugging.
func (s *Sampler) LastDebugBodies() []string {
	if s == nil || s.debug == nil {
		return nil
	}
	return s.debug.bodies()
}

func (s *Sampler) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	if s.auth != nil && s.auth.clock != nil {
		return s.auth.clock.Now()
	}
	return realClock{}.Now()
}

func (s *Sampler) ensureReady(ctx context.Context) (store.TadoToken, bool) {
	tok, err := s.auth.repo.Load(ctx)
	if err != nil {
		slog.Error("loading tado token failed", "op", "tado_rooms")
		return store.TadoToken{}, false
	}
	if tok.State != store.TadoLinked {
		return tok, false
	}
	if s.auth.NeedsRefresh(tok, s.now()) {
		if err := s.auth.Refresh(ctx); err != nil {
			if errors.Is(err, ErrNeedsReauth) {
				return tok, false
			}
			slog.Error("tado token refresh failed", "op", "tado_rooms")
			return tok, false
		}
		tok, err = s.auth.repo.Load(ctx)
		if err != nil {
			slog.Error("loading tado token failed", "op", "tado_rooms")
			return store.TadoToken{}, false
		}
		if tok.State != store.TadoLinked {
			return tok, false
		}
	}
	if tok.AccessToken == nil || *tok.AccessToken == "" {
		slog.Warn("tado linked but no access token", "op", "tado_rooms")
		return tok, false
	}
	return tok, true
}

func (s *Sampler) ensureHomeID(ctx context.Context, tok store.TadoToken) (string, bool) {
	if tok.HomeID != nil && *tok.HomeID != "" {
		return *tok.HomeID, true
	}
	body, ok := s.getAuthed(ctx, s.meURL, tok)
	if !ok {
		slog.Warn("tado me request failed", "op", "tado_me")
		return "", false
	}
	id, ok := parseHomeID(body)
	if !ok {
		slog.Warn("tado me response had no home id", "op", "tado_me")
		return "", false
	}
	if err := s.persistHomeID(ctx, id); err != nil {
		slog.Error("persisting tado home_id failed", "op", "tado_home_id")
	}
	return id, true
}

func (s *Sampler) persistHomeID(ctx context.Context, homeID string) error {
	return s.auth.repo.WithLock(ctx, func(ctx context.Context, current store.TadoToken, w store.TadoTokenWriter) error {
		if current.HomeID != nil && *current.HomeID != "" {
			return nil
		}
		next := current
		id := homeID
		next.HomeID = &id
		return w.Write(ctx, next)
	})
}

func (s *Sampler) mappedRoomIDs(ctx context.Context) (map[string]struct{}, bool) {
	plants, err := s.plants.List(ctx)
	if err != nil {
		slog.Error("listing plants for climate sample failed", "op", "tado_sample")
		return nil, false
	}
	mapped := make(map[string]struct{})
	for _, row := range plants {
		if !row.Plant.Active {
			continue
		}
		if row.Plant.TadoRoomID == nil || *row.Plant.TadoRoomID == "" {
			continue
		}
		mapped[*row.Plant.TadoRoomID] = struct{}{}
	}
	return mapped, true
}

func (s *Sampler) roomsURL(homeID string) string {
	return strings.TrimRight(s.hopsBase, "/") + "/homes/" + url.PathEscape(homeID) + "/rooms"
}

func (s *Sampler) getAuthed(ctx context.Context, rawURL string, tok store.TadoToken) ([]byte, bool) {
	if tok.AccessToken == nil || *tok.AccessToken == "" {
		return nil, false
	}
	body, status, ok := s.doGET(ctx, rawURL, *tok.AccessToken)
	if ok {
		return body, true
	}
	if status != http.StatusUnauthorized {
		return nil, false
	}
	if err := s.auth.Refresh(ctx); err != nil {
		if !errors.Is(err, ErrNeedsReauth) {
			slog.Error("tado refresh after 401 failed", "op", "tado_http")
		}
		return nil, false
	}
	tok, err := s.auth.repo.Load(ctx)
	if err != nil || tok.State != store.TadoLinked || tok.AccessToken == nil || *tok.AccessToken == "" {
		return nil, false
	}
	body, _, ok = s.doGET(ctx, rawURL, *tok.AccessToken)
	return body, ok
}

func (s *Sampler) doGET(ctx context.Context, rawURL, accessToken string) (body []byte, status int, ok bool) {
	host := hostOf(rawURL)
	var lastErr error
	for attempt := 1; attempt <= maxFetchAttempts; attempt++ {
		var retryable bool
		var err error
		body, status, retryable, err = s.getOnce(ctx, rawURL, accessToken)
		if err == nil {
			return body, status, true
		}
		lastErr = err
		if status == http.StatusUnauthorized {
			return body, status, false
		}
		if !retryable || attempt == maxFetchAttempts {
			slog.Warn("tado request failed",
				"op", "tado_http",
				"host", host,
				"status", status,
				"err", lastErr,
			)
			return body, status, false
		}
		backoff := retryBackoff * time.Duration(1<<uint(attempt-1))
		if err := s.sleep(ctx, backoff); err != nil {
			return nil, status, false
		}
	}
	return body, status, false
}

func (s *Sampler) getOnce(ctx context.Context, rawURL, accessToken string) (body []byte, status int, retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, false, fmt.Errorf("building tado request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, err
		}
		return nil, 0, true, fmt.Errorf("calling tado: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, true, fmt.Errorf("reading tado response: %w", err)
	}
	s.recordDebug(rawURL, resp.StatusCode, data)

	switch {
	case resp.StatusCode >= 500:
		return data, resp.StatusCode, true, fmt.Errorf("tado status %d", resp.StatusCode)
	case resp.StatusCode >= 400:
		return data, resp.StatusCode, false, fmt.Errorf("tado status %d", resp.StatusCode)
	}
	return data, resp.StatusCode, false, nil
}

func (s *Sampler) recordDebug(rawURL string, status int, body []byte) {
	host := hostOf(rawURL)
	n := len(body)
	truncated := body
	if len(truncated) > debugBodyCap {
		truncated = truncated[:debugBodyCap]
	}
	slog.Debug("tado raw response",
		"host", host,
		"status", status,
		"body_bytes", n,
		"body", string(truncated),
	)
	if s.debug != nil {
		s.debug.add(truncated)
	}
}

type debugRing struct {
	mu    sync.Mutex
	max   int
	items []string
}

func newDebugRing(n int) *debugRing {
	return &debugRing{max: n}
}

func (r *debugRing) add(body []byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, string(body))
	if len(r.items) > r.max {
		r.items = r.items[len(r.items)-r.max:]
	}
}

func (r *debugRing) bodies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.items))
	copy(out, r.items)
	return out
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

func bucketUTC(t time.Time) time.Time {
	t = t.UTC()
	minute := 0
	if t.Minute() >= int(sampleBucket/time.Minute) {
		minute = int(sampleBucket / time.Minute)
	}
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), minute, 0, 0, time.UTC)
}

func parseHomeID(body []byte) (string, bool) {
	var top any
	if err := json.Unmarshal(body, &top); err != nil {
		return "", false
	}
	m, ok := top.(map[string]any)
	if !ok {
		return "", false
	}
	if homes, ok := m["homes"].([]any); ok && len(homes) > 0 {
		if hm, ok := homes[0].(map[string]any); ok {
			if id := stringifyID(hm["id"]); id != "" {
				if len(homes) > 1 {
					slog.Info("tado me listed multiple homes; using the first",
						"op", "tado_me",
						"home_count", len(homes),
					)
				}
				return id, true
			}
		}
	}
	if home, ok := m["home"].(map[string]any); ok {
		if id := stringifyID(home["id"]); id != "" {
			return id, true
		}
	}
	return "", false
}

func parseRoomsBody(body []byte, observedAt time.Time) []climate.RoomClimate {
	if len(body) == 0 {
		slog.Warn("tado rooms response empty", "op", "tado_rooms")
		return nil
	}
	var top any
	if err := json.Unmarshal(body, &top); err != nil {
		slog.Warn("tado rooms body is not json", "op", "tado_rooms", "body_bytes", len(body))
		return nil
	}
	items := roomItems(top)
	if items == nil {
		slog.Warn("tado rooms response had unexpected shape", "op", "tado_rooms", "body_bytes", len(body))
		return nil
	}
	if len(items) == 0 {
		slog.Info("tado rooms list empty", "op", "tado_rooms")
		return []climate.RoomClimate{}
	}
	out := make([]climate.RoomClimate, 0, len(items))
	for _, item := range items {
		if rc, ok := parseOneRoom(item, observedAt); ok {
			out = append(out, rc)
		}
	}
	return out
}

func roomItems(top any) []any {
	switch v := top.(type) {
	case []any:
		return v
	case map[string]any:
		for _, key := range []string{"rooms", "Rooms", "items", "data"} {
			if inner, ok := v[key].([]any); ok {
				return inner
			}
		}
	}
	return nil
}

func parseOneRoom(item any, observedAt time.Time) (climate.RoomClimate, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		slog.Info("tado room skipped: not an object", "op", "tado_rooms")
		return climate.RoomClimate{}, false
	}
	id := firstID(m, "id", "roomId", "room_id")
	if id == "" {
		slog.Info("tado room skipped: missing id", "op", "tado_rooms")
		return climate.RoomClimate{}, false
	}
	name := firstString(m, "name", "roomName", "room_name")
	if name == "" {
		name = id
	}

	temp, hasTemp := lookupFloat(m,
		[]string{"sensor", "temperature", "celsius"},
		[]string{"sensor", "temperature", "value"},
		[]string{"temperature", "celsius"},
		[]string{"temperature", "value"},
		[]string{"insideTemperature", "celsius"},
		[]string{"tempC"},
		[]string{"temp_c"},
		[]string{"temp"},
		[]string{"temperature"},
	)
	hum, hasHum := lookupFloat(m,
		[]string{"sensor", "humidity", "percentage"},
		[]string{"sensor", "humidity", "value"},
		[]string{"humidity", "percentage"},
		[]string{"humidity", "value"},
		[]string{"humidityPct"},
		[]string{"humidity_pct"},
		[]string{"rh"},
		[]string{"humidity"},
	)
	if !hasTemp || !hasHum {
		slog.Info("tado room skipped: no usable sensor reading", "op", "tado_rooms", "room_id", id)
		return climate.RoomClimate{}, false
	}
	if temp < minTempC || temp > maxTempC || hum < minHumidity || hum > maxHumidity {
		slog.Info("tado room skipped: sensor fault",
			"op", "tado_rooms",
			"room_id", id,
			"temp_c", temp,
			"humidity_pct", hum,
		)
		return climate.RoomClimate{}, false
	}
	when := observedAt
	if ts, ok := lookupTime(m); ok {
		when = ts
	}
	return climate.RoomClimate{
		RoomID:      id,
		Name:        name,
		TempC:       temp,
		HumidityPct: hum,
		ObservedAt:  when,
	}, true
}

func firstID(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if id := stringifyID(m[k]); id != "" {
			return id
		}
	}
	return ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok {
			s = strings.TrimSpace(s)
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func lookupFloat(m map[string]any, paths ...[]string) (float64, bool) {
	for _, path := range paths {
		v, ok := walk(m, path)
		if !ok {
			continue
		}
		if f, ok := asFloat(v); ok {
			return f, true
		}
	}
	return 0, false
}

func lookupTime(m map[string]any) (time.Time, bool) {
	paths := [][]string{
		{"timestamp"},
		{"observedAt"},
		{"observed_at"},
		{"measuredAt"},
		{"measured_at"},
		{"lastUpdated"},
		{"sensor", "timestamp"},
	}
	for _, path := range paths {
		v, ok := walk(m, path)
		if !ok {
			continue
		}
		if t, ok := asTime(v); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

func walk(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, key := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func stringifyID(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return ""
	}
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func asTime(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			t, err := time.Parse(layout, s)
			if err == nil {
				return t, true
			}
		}
	case float64:
		if x > 1e12 {
			return time.UnixMilli(int64(x)).UTC(), true
		}
		return time.Unix(int64(x), 0).UTC(), true
	case int64:
		return time.Unix(x, 0).UTC(), true
	}
	return time.Time{}, false
}
