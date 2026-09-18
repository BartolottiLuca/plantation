package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/climate/tado"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
)

const (
	tadoDeviceLoginURL  = "https://login.tado.com/oauth2/device"
	tadoFlowHTTPTimeout = 10 * time.Second
	tadoWaitSlack       = 15 * time.Second
	tadoDefaultWait     = 300 * time.Second
	refreshWarnAfter    = 21 * 24 * time.Hour
	weatherStaleAfter   = 48 * time.Hour
	diagIOTimeout       = 5 * time.Second

	flowWaiting = "waiting"
	flowLinked  = "linked"
	flowExpired = "expired"
	flowDenied  = "denied"
	flowFailed  = "failed"
)

// TadoLinker is the settings-page seam for the device-code flow.
// *tado.Client satisfies it; cmd wires the concrete client.
type TadoLinker interface {
	StartDeviceFlow(ctx context.Context) (tado.DeviceCode, error)
	WaitForLink(ctx context.Context, dc tado.DeviceCode) error
	Status(ctx context.Context) climate.LinkStatus
}

var _ TadoLinker = (*tado.Client)(nil)

// WeatherAge is *store.WeatherRepo.LastFetchedAt.
type WeatherAge interface {
	LastFetchedAt(ctx context.Context, locationKey string) (time.Time, error)
}

// WeatherSeries is *weather.Service.Series.
type WeatherSeries interface {
	Series(ctx context.Context, from, to domain.Date) (care.EnvSeries, error)
}

// SchedulerStat is *scheduler.Loop (HoldsLock / LastTick).
type SchedulerStat interface {
	HoldsLock() bool
	LastTick() time.Time
}

// FailedNotification is one failed outbox row for the diagnostics page.
type FailedNotification struct {
	DedupeKey string
	LastError string
}

// NotificationSnapshot is injected by cmd — web does not query the outbox.
type NotificationSnapshot struct {
	LastStatus      string
	LastAt          time.Time
	Failed          []FailedNotification
	SkippedThisWeek int
}

// DeviceCode.device_code is unexported, so it cannot go in HTML, cookies, or
// JSON. This single in-process slot is the only place a pending code lives.
type tadoFlowState struct {
	mu      sync.Mutex
	gen     uint64
	cancel  context.CancelFunc
	pending tado.DeviceCode
	phase   string
	errText string
}

func (s *Server) registerSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/tado", noIndex(s.settingsTadoGET))
	mux.HandleFunc("POST /settings/tado", s.requireWrite(s.settingsTadoPOST))
	mux.HandleFunc("GET /settings/tado/status", noIndex(s.settingsTadoStatus))
	mux.HandleFunc("GET /settings/diagnostics", noIndex(s.settingsDiagnostics))
	mux.HandleFunc("POST /settings/test-notification", s.requireWrite(s.settingsTestNotification))
}

func (s *Server) settingsTadoGET(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "settings_tado.html", s.tadoPage(r, ""))
}

func (s *Server) settingsTadoPOST(w http.ResponseWriter, r *http.Request) {
	data := s.startTadoFlow(r)
	if isHTMX(r) {
		s.renderPartial(w, http.StatusOK, "settings_tado.html", "tado-status", data)
		return
	}
	s.render(w, http.StatusOK, "settings_tado.html", data)
}

func (s *Server) settingsTadoStatus(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, http.StatusOK, "settings_tado.html", "tado-status", s.tadoPage(r, ""))
}

func (s *Server) startTadoFlow(r *http.Request) tadoPageData {
	if s.Tado == nil {
		return s.tadoPage(r, "")
	}
	ctx, cancel := context.WithTimeout(r.Context(), tadoFlowHTTPTimeout)
	defer cancel()
	dc, err := s.Tado.StartDeviceFlow(ctx)
	if err != nil {
		slog.Error("starting tado device flow", "err", err)
		return s.tadoPage(r, publicError(err.Error()))
	}
	s.watchTadoLink(dc)
	return s.tadoPage(r, "")
}

func (s *Server) watchTadoLink(dc tado.DeviceCode) {
	if s.Tado == nil || s.tadoFlow == nil {
		return
	}
	timeout := tadoDefaultWait + tadoWaitSlack
	if !dc.ExpiresAt.IsZero() {
		remain := dc.ExpiresAt.Sub(s.Clock.Now()) + tadoWaitSlack
		if remain > 0 {
			timeout = remain
		} else {
			timeout = tadoWaitSlack
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	gen := s.tadoFlow.begin(dc, cancel)
	go func() {
		err := s.Tado.WaitForLink(ctx, dc)
		phase, errText := classifyTadoWait(err)
		s.tadoFlow.finish(gen, phase, errText)
	}()
}

func classifyTadoWait(err error) (phase, errText string) {
	if err == nil {
		return flowLinked, ""
	}
	switch {
	case errors.Is(err, tado.ErrDeviceCodeExpired):
		return flowExpired, ""
	case errors.Is(err, tado.ErrAccessDenied):
		return flowDenied, ""
	case errors.Is(err, tado.ErrTokenLost):
		return flowFailed, "Tado accepted the login but the token could not be saved. Try again."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return flowExpired, ""
	default:
		return flowFailed, publicError(err.Error())
	}
}

func (f *tadoFlowState) begin(dc tado.DeviceCode, cancel context.CancelFunc) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil {
		f.cancel()
	}
	f.gen++
	f.cancel = cancel
	f.pending = dc
	f.phase = flowWaiting
	f.errText = ""
	return f.gen
}

func (f *tadoFlowState) finish(gen uint64, phase, errText string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gen != gen {
		return
	}
	f.phase = phase
	f.errText = errText
}

func (f *tadoFlowState) snapshot(now time.Time) (phase, errText string, dc tado.DeviceCode) {
	if f == nil {
		return "", "", tado.DeviceCode{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	phase, errText, dc = f.phase, f.errText, f.pending
	if phase == flowWaiting && !dc.ExpiresAt.IsZero() && now.After(dc.ExpiresAt) {
		phase = flowExpired
	}
	return phase, errText, dc
}

type tadoPageData struct {
	page
	Disabled   bool
	State      string
	HomeID     string
	Rooms      []climate.RoomClimate
	StartError string
	Flow       tadoFlowView
}

type tadoFlowView struct {
	Phase           string
	UserCode        string
	VerificationURI string
	Message         string
	Poll            bool
}

func (s *Server) tadoPage(r *http.Request, startErr string) tadoPageData {
	data := tadoPageData{
		page:       s.page(r, "settings"),
		Disabled:   s.Tado == nil,
		StartError: startErr,
	}
	now := s.Clock.Now()
	phase, errText, dc := s.tadoFlow.snapshot(now)
	data.Flow = tadoFlowView{
		Phase:           phase,
		UserCode:        dc.UserCode,
		VerificationURI: verificationURI(dc),
		Message:         flowMessage(phase, errText),
		Poll:            phase == flowWaiting,
	}
	if s.Tado == nil {
		return data
	}
	ctx, cancel := context.WithTimeout(r.Context(), tadoFlowHTTPTimeout)
	defer cancel()
	st := s.Tado.Status(ctx)
	data.State = st.State
	data.HomeID = st.HomeID
	if st.State == climate.StateLinked || phase == flowLinked {
		data.Rooms = s.listRooms(ctx)
	}
	return data
}

func verificationURI(dc tado.DeviceCode) string {
	u := strings.TrimSpace(dc.VerificationURI)
	if u == "" || strings.Contains(strings.ToLower(u), "device_code") {
		return tadoDeviceLoginURL
	}
	return u
}

func flowMessage(phase, errText string) string {
	switch phase {
	case flowWaiting:
		return "Waiting for authorization. This code expires in about five minutes."
	case flowLinked:
		return "Tado is linked."
	case flowExpired:
		return "The device code expired before Tado was authorized. Start again."
	case flowDenied:
		return "Tado authorization was denied."
	case flowFailed:
		if errText != "" {
			return errText
		}
		return "Linking failed."
	default:
		return ""
	}
}

func (s *Server) settingsDiagnostics(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "settings_diagnostics.html", s.diagPage(r, testNotifyView{}))
}

func (s *Server) settingsTestNotification(w http.ResponseWriter, r *http.Request) {
	result := s.sendTestNotification(r.Context())
	if isHTMX(r) {
		s.renderPartial(w, http.StatusOK, "settings_diagnostics.html", "test-notification-result", result)
		return
	}
	s.render(w, http.StatusOK, "settings_diagnostics.html", s.diagPage(r, result))
}

func (s *Server) sendTestNotification(ctx context.Context) testNotifyView {
	if s.Notifier == nil {
		return testNotifyView{Attempted: true, NoWebhook: true}
	}
	ctx, cancel := context.WithTimeout(ctx, diagIOTimeout)
	defer cancel()
	err := notify.RunSendTestDigest(ctx, s.Notifier, s.BaseURL, s.Clock.Now)
	if err != nil {
		return testNotifyView{Attempted: true, Error: publicError(err.Error())}
	}
	return testNotifyView{Attempted: true, OK: true}
}

type diagData struct {
	page
	Weather weatherDiagView
	Tado    tadoDiagView
	Notify  notifyDiagView
	Catalog catalogDiagView
	Sched   schedDiagView
	Test    testNotifyView
}

type weatherDiagView struct {
	Disabled     bool
	Unavailable  bool
	NeverFetched bool
	LastFetch    string
	OutdoorStale bool
	Estimated    bool
	LocationKey  string
}

type tadoDiagView struct {
	Disabled        bool
	State           string
	AccessCountdown string
	RefreshAge      string
	ReauthWarning   bool
}

type notifyDiagView struct {
	LastDigestFailed bool
	LastStatus       string
	LastAt           string
	HistoryUnknown   bool
	NoWebhook        bool
	Failed           []FailedNotification
	SkippedThisWeek  int
}

type catalogDiagView struct {
	SpeciesCount int
	Ready        bool
	Unavailable  bool
}

type schedDiagView struct {
	Running   bool
	HoldsLock bool
	LastTick  string
}

type testNotifyView struct {
	Attempted bool
	OK        bool
	NoWebhook bool
	Error     string
}

func (s *Server) diagPage(r *http.Request, test testNotifyView) diagData {
	ctx := r.Context()
	return diagData{
		page:    s.page(r, "settings"),
		Weather: s.weatherDiag(ctx),
		Tado:    s.tadoDiag(ctx),
		Notify:  s.notifyDiag(ctx),
		Catalog: s.catalogDiag(ctx),
		Sched:   s.schedDiag(),
		Test:    test,
	}
}

func (s *Server) weatherDiag(ctx context.Context) weatherDiagView {
	if s.WeatherAge == nil && s.Weather == nil {
		return weatherDiagView{Disabled: true}
	}
	view := weatherDiagView{LocationKey: s.LocationKey}
	ctx, cancel := context.WithTimeout(ctx, diagIOTimeout)
	defer cancel()
	now := s.Clock.Now()
	if s.WeatherAge != nil && s.LocationKey != "" {
		at, err := s.WeatherAge.LastFetchedAt(ctx, s.LocationKey)
		if err != nil {
			slog.Error("reading weather last fetch", "err", err)
			view.Unavailable = true
		} else if at.IsZero() {
			view.NeverFetched = true
			view.OutdoorStale = true
		} else {
			view.LastFetch = s.formatTime(at)
			if now.Sub(at) > weatherStaleAfter {
				view.OutdoorStale = true
			}
		}
	}
	if s.Weather != nil {
		today := s.today()
		series, err := s.Weather.Series(ctx, today.AddDays(-7), today.AddDays(16))
		if err != nil {
			slog.Error("reading weather series", "err", err)
			view.Unavailable = true
		} else {
			view.OutdoorStale = series.OutdoorStale
			for _, d := range series.Days {
				if d.Estimated {
					view.Estimated = true
					break
				}
			}
		}
	}
	return view
}

func (s *Server) tadoDiag(ctx context.Context) tadoDiagView {
	if s.Tado == nil {
		return tadoDiagView{Disabled: true}
	}
	ctx, cancel := context.WithTimeout(ctx, tadoFlowHTTPTimeout)
	defer cancel()
	st := s.Tado.Status(ctx)
	now := s.Clock.Now()
	view := tadoDiagView{State: st.State}
	if !st.AccessExpiresAt.IsZero() {
		d := st.AccessExpiresAt.Sub(now)
		if d >= 0 {
			view.AccessCountdown = "expires in " + formatAge(d)
		} else {
			view.AccessCountdown = "expired " + formatAge(d) + " ago"
		}
	}
	if !st.RefreshObtainedAt.IsZero() {
		age := now.Sub(st.RefreshObtainedAt)
		view.RefreshAge = formatAge(age)
		view.ReauthWarning = age >= refreshWarnAfter
	}
	return view
}

func (s *Server) notifyDiag(ctx context.Context) notifyDiagView {
	view := notifyDiagView{
		LastDigestFailed: s.digestFailed(ctx),
		NoWebhook:        s.Notifier == nil,
	}
	if s.LastNotifications == nil {
		view.HistoryUnknown = true
		return view
	}
	ctx, cancel := context.WithTimeout(ctx, diagIOTimeout)
	defer cancel()
	snap, err := s.LastNotifications(ctx)
	if err != nil {
		slog.Error("reading notification snapshot", "err", err)
		view.HistoryUnknown = true
		return view
	}
	view.LastStatus = snap.LastStatus
	view.LastAt = s.formatTime(snap.LastAt)
	view.SkippedThisWeek = snap.SkippedThisWeek
	view.Failed = make([]FailedNotification, 0, len(snap.Failed))
	for _, row := range snap.Failed {
		view.Failed = append(view.Failed, FailedNotification{
			DedupeKey: row.DedupeKey,
			LastError: publicError(row.LastError),
		})
	}
	return view
}

func (s *Server) catalogDiag(ctx context.Context) catalogDiagView {
	view := catalogDiagView{Ready: s.CatalogReady}
	if s.Species == nil {
		view.Unavailable = true
		return view
	}
	ctx, cancel := context.WithTimeout(ctx, diagIOTimeout)
	defer cancel()
	list, err := s.Species.List(ctx)
	if err != nil {
		slog.Error("listing species for diagnostics", "err", err)
		view.Unavailable = true
		return view
	}
	view.SpeciesCount = len(list)
	return view
}

func (s *Server) schedDiag() schedDiagView {
	if s.Scheduler == nil {
		return schedDiagView{}
	}
	return schedDiagView{
		Running:   true,
		HoldsLock: s.Scheduler.HoldsLock(),
		LastTick:  s.formatTime(s.Scheduler.LastTick()),
	}
}

func (s *Server) formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(s.loc()).Format("2006-01-02 15:04 MST")
}

func (s *Server) renderPartial(w http.ResponseWriter, status int, page, name string, data any) {
	t := s.pages[page]
	if t == nil {
		slog.Error("unknown template", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("executing template", "page", page, "name", name, "err", err)
	}
}

func formatAge(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < 2*time.Minute:
		return "1 minute"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 2*time.Hour:
		return "1 hour"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d < 48*time.Hour:
		return "1 day"
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

func publicError(s string) string {
	lower := strings.ToLower(s)
	if strings.Contains(lower, "discord.com/api/webhooks") ||
		strings.Contains(lower, "access_token") ||
		strings.Contains(lower, "refresh_token") ||
		strings.Contains(lower, "device_code") {
		return "operation failed"
	}
	return s
}
