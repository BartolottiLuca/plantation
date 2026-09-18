package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/BartolottiLuca/plantation/internal/care"
	"github.com/BartolottiLuca/plantation/internal/climate"
	"github.com/BartolottiLuca/plantation/internal/climate/tado"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/notify"
)

const (
	secretAccess  = "tado-access-token-secret"
	secretRefresh = "tado-refresh-token-secret"
	secretWebhook = "https://discord.com/api/webhooks/000/secret-webhook"
	secretCoords  = "51.507351,-0.127758"
)

func testSettings(t *testing.T, extra func(*Server)) (*Server, *http.ServeMux) {
	t.Helper()
	s, _, mux := testUI(t, func(_ *memDB, s *Server) {
		s.CatalogReady = true
		if extra != nil {
			extra(s)
		}
	})
	t.Cleanup(func() { cancelTadoFlow(s) })
	return s, mux
}

func cancelTadoFlow(s *Server) {
	if s == nil || s.tadoFlow == nil {
		return
	}
	s.tadoFlow.mu.Lock()
	cancel := s.tadoFlow.cancel
	s.tadoFlow.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func assertNoSecrets(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assertNotContains(t, rec,
		secretAccess, secretRefresh, secretWebhook, secretCoords,
		"device_code=", "access_token", "refresh_token",
	)
}

type fakeTado struct {
	mu       sync.Mutex
	status   climate.LinkStatus
	start    tado.DeviceCode
	startErr error
	waitErr  error
	waitFn   func(ctx context.Context, dc tado.DeviceCode) error
}

func (f *fakeTado) StartDeviceFlow(context.Context) (tado.DeviceCode, error) {
	if f.startErr != nil {
		return tado.DeviceCode{}, f.startErr
	}
	dc := f.start
	if dc.UserCode == "" {
		dc.UserCode = "WDJB-MJHT"
	}
	if dc.VerificationURI == "" {
		dc.VerificationURI = tadoDeviceLoginURL
	}
	return dc, nil
}

func (f *fakeTado) WaitForLink(ctx context.Context, dc tado.DeviceCode) error {
	if f.waitFn != nil {
		return f.waitFn(ctx, dc)
	}
	if f.waitErr != nil {
		return f.waitErr
	}
	f.mu.Lock()
	f.status.State = climate.StateLinked
	if f.status.HomeID == "" {
		f.status.HomeID = "home-1"
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeTado) Status(context.Context) climate.LinkStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.status
	if st.State == "" {
		st.State = climate.StateUnlinked
	}
	return st
}

type fakeNotifier struct {
	err error
}

func (f fakeNotifier) SendOnce(context.Context, string, string, notify.Message) error {
	return f.err
}

type fakeWeatherAge struct {
	at  time.Time
	err error
}

func (f fakeWeatherAge) LastFetchedAt(context.Context, string) (time.Time, error) {
	return f.at, f.err
}

type fakeWeather struct {
	series care.EnvSeries
	err    error
}

func (f fakeWeather) Series(context.Context, domain.Date, domain.Date) (care.EnvSeries, error) {
	return f.series, f.err
}

type fakeSched struct {
	hold bool
	tick time.Time
}

func (f fakeSched) HoldsLock() bool     { return f.hold }
func (f fakeSched) LastTick() time.Time { return f.tick }

func waitPhase(t *testing.T, s *Server, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		phase, _, _ := s.tadoFlow.snapshot(s.Clock.Now())
		if phase == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("tado flow never reached %q", want)
}

func TestSettingsTadoUnlinked(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{status: climate.LinkStatus{State: climate.StateUnlinked}}
	})
	rec := doGET(t, mux, "/settings/tado")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec, "not linked", "Link Tado")
	assertNoSecrets(t, rec)
}

func TestSettingsTadoLinkedShowsHomeAndRooms(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{status: climate.LinkStatus{
			State:             climate.StateLinked,
			HomeID:            "home-99",
			AccessExpiresAt:   testNow.Add(8 * time.Minute),
			RefreshObtainedAt: testNow.Add(-2 * time.Hour),
		}}
		s.Rooms = roomList{
			{RoomID: "r1", Name: "Kitchen"},
			{RoomID: "r2", Name: "Studio"},
		}
	})
	rec := doGET(t, mux, "/settings/tado")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "home-99", "Kitchen", "Studio", "Linked")
	assertNoSecrets(t, rec)
}

func TestSettingsTadoNeedsReauthIsRecoveryPath(t *testing.T) {
	s, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{
			status: climate.LinkStatus{State: climate.StateNeedsReauth, HomeID: "home-old"},
			start: tado.DeviceCode{
				UserCode:        "REAU-1234",
				VerificationURI: tadoDeviceLoginURL,
				ExpiresAt:       testNow.Add(5 * time.Minute),
			},
			waitFn: func(ctx context.Context, _ tado.DeviceCode) error {
				<-ctx.Done()
				return ctx.Err()
			},
		}
	})
	rec := doGET(t, mux, "/settings/tado")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "authorized again", "Re-link Tado")
	assertNoSecrets(t, rec)

	post := doPOST(t, mux, "/settings/tado", nil)
	assertStatus(t, post, http.StatusOK)
	assertNoIndex(t, post)
	assertContains(t, post, "REAU-1234", tadoDeviceLoginURL, "Waiting for authorization")
	assertNoSecrets(t, post)
	if phase, _, _ := s.tadoFlow.snapshot(s.Clock.Now()); phase != flowWaiting {
		t.Fatalf("phase = %q, want waiting", phase)
	}
}

func TestSettingsTadoExpiredDeviceCode(t *testing.T) {
	s, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{
			status: climate.LinkStatus{State: climate.StateUnlinked},
			start: tado.DeviceCode{
				UserCode:        "EXPD-0001",
				VerificationURI: tadoDeviceLoginURL,
				ExpiresAt:       testNow.Add(-time.Second),
			},
			waitErr: tado.ErrDeviceCodeExpired,
		}
	})
	post := doPOST(t, mux, "/settings/tado", nil)
	assertStatus(t, post, http.StatusOK)
	assertContains(t, post, "expired")
	assertNoSecrets(t, post)

	waitPhase(t, s, flowExpired)
	st := doGET(t, mux, "/settings/tado/status")
	assertStatus(t, st, http.StatusOK)
	assertNoIndex(t, st)
	assertContains(t, st, "expired")
	assertNoSecrets(t, st)
}

func TestSettingsTadoDisabled(t *testing.T) {
	_, mux := testSettings(t, nil)
	rec := doGET(t, mux, "/settings/tado")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Tado is disabled")
	assertNoSecrets(t, rec)

	post := doPOST(t, mux, "/settings/tado", nil)
	assertStatus(t, post, http.StatusOK)
	assertContains(t, post, "Tado is disabled")
}

func TestSettingsTadoPOSTThenLinked(t *testing.T) {
	s, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{
			status: climate.LinkStatus{State: climate.StateUnlinked},
			start: tado.DeviceCode{
				UserCode:        "LINK-9999",
				VerificationURI: tadoDeviceLoginURL,
				ExpiresAt:       testNow.Add(5 * time.Minute),
			},
		}
		s.Rooms = roomList{{RoomID: "r1", Name: "Conservatory"}}
	})
	post := doPOST(t, mux, "/settings/tado", nil)
	assertStatus(t, post, http.StatusOK)
	assertContains(t, post, "LINK-9999")
	waitPhase(t, s, flowLinked)
	st := doGET(t, mux, "/settings/tado/status")
	assertContains(t, st, "home-1", "Conservatory", "Tado is linked")
	assertNoSecrets(t, st)
}

func TestSettingsDiagnosticsStaleWeather(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.LocationKey = "51.500,-0.120"
		s.WeatherAge = fakeWeatherAge{at: testNow.Add(-72 * time.Hour)}
		s.Weather = fakeWeather{series: care.EnvSeries{
			OutdoorStale: true,
			Days:         []care.DayEnv{{Estimated: true}},
		}}
		s.Tado = &fakeTado{status: climate.LinkStatus{
			State:             climate.StateLinked,
			AccessExpiresAt:   testNow.Add(8 * time.Minute),
			RefreshObtainedAt: testNow.Add(-22 * 24 * time.Hour),
		}}
		s.Scheduler = fakeSched{hold: true, tick: testNow.Add(-2 * time.Minute)}
	})
	rec := doGET(t, mux, "/settings/diagnostics")
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec,
		"51.500,-0.120",
		"Outdoor stale", "yes",
		"Estimated days",
		"Weather is stale",
		"linked",
		"expires in",
		"21 or more days",
		"this pod holds the lock",
	)
	assertNoSecrets(t, rec)
}

func TestSettingsDiagnosticsWeatherDisabled(t *testing.T) {
	_, mux := testSettings(t, nil)
	rec := doGET(t, mux, "/settings/diagnostics")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Weather is disabled", "Tado is disabled", "Scheduler is not running", "succeeded")
	assertNoSecrets(t, rec)
}

func TestSettingsDiagnosticsFailedDigest(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.LastDigestFailed = func(context.Context) bool { return true }
		s.LastNotifications = func(context.Context) (NotificationSnapshot, error) {
			return NotificationSnapshot{
				LastStatus:      "failed",
				LastAt:          testNow.Add(-time.Hour),
				SkippedThisWeek: 3,
				Failed: []FailedNotification{{
					DedupeKey: "digest:2026-09-16",
					LastError: "discord returned status 404",
				}, {
					DedupeKey: "ops:send",
					LastError: "post failed: " + secretWebhook,
				}},
			}, nil
		}
	})
	rec := doGET(t, mux, "/settings/diagnostics")
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Last digest failed", "digest:2026-09-16", "discord returned status 404", "Skipped days this week: 3", "operation failed")
	assertNoSecrets(t, rec)
}

func TestSettingsTestNotificationNoWebhook(t *testing.T) {
	_, mux := testSettings(t, nil)
	page := doGET(t, mux, "/settings/diagnostics")
	assertContains(t, page, "No Discord webhook is configured")
	assertNoSecrets(t, page)

	rec := doPOST(t, mux, "/settings/test-notification", nil)
	assertStatus(t, rec, http.StatusOK)
	assertNoIndex(t, rec)
	assertContains(t, rec, "No Discord webhook is configured", "Nothing was sent")
	assertNoSecrets(t, rec)
}

func TestSettingsTestNotificationFailure(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Notifier = fakeNotifier{err: errors.New("discord returned status 500")}
		s.BaseURL = "https://plantation.example"
	})
	rec := doPOST(t, mux, "/settings/test-notification", nil)
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Test notification failed", "discord returned status 500")
	assertNoSecrets(t, rec)
}

func TestSettingsTestNotificationWebhookRedacted(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Notifier = fakeNotifier{err: errors.New("dial " + secretWebhook)}
	})
	rec := doPOST(t, mux, "/settings/test-notification", nil)
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Test notification failed", "operation failed")
	assertNoSecrets(t, rec)
}

func TestSettingsTestNotificationSuccess(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Notifier = fakeNotifier{}
		s.BaseURL = "https://plantation.example"
	})
	rec := doPOST(t, mux, "/settings/test-notification", nil)
	assertStatus(t, rec, http.StatusOK)
	assertContains(t, rec, "Test notification sent")
	assertNoSecrets(t, rec)
}

func TestSettingsWriteTokenOnPOST(t *testing.T) {
	clock := &care.NoopClock{Instant: testNow}
	db := newMem(clock)
	db.addSpecies(fixtureSpecies())
	s := NewServer(Server{
		Plants:       db,
		Species:      memSpecies{db},
		Events:       db,
		Clock:        clock,
		Location:     time.UTC,
		WriteToken:   "secret",
		CatalogReady: true,
		Tado:         &fakeTado{},
		Notifier:     fakeNotifier{},
	})
	mux := http.NewServeMux()
	s.Register(mux)
	t.Cleanup(func() { cancelTadoFlow(s) })

	for _, path := range []string{"/settings/tado", "/settings/test-notification"} {
		denied := doPOST(t, mux, path, nil)
		assertStatus(t, denied, http.StatusUnauthorized)
		assertNoIndex(t, denied)
	}
	ok := doPOSTHdr(t, mux, "/settings/tado", nil, http.Header{"Authorization": {"Bearer secret"}})
	assertStatus(t, ok, http.StatusOK)
	assertContains(t, ok, "WDJB-MJHT")
}

func TestSettingsMutationsArePOSTOnly(t *testing.T) {
	_, mux := testSettings(t, func(s *Server) {
		s.Tado = &fakeTado{}
		s.Notifier = fakeNotifier{}
	})
	rec := doGET(t, mux, "/settings/test-notification")
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Fatalf("GET /settings/test-notification status = %d", rec.Code)
	}
}
