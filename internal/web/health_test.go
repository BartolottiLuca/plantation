package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "time/tzdata"
)

func TestHealthzOKWithoutCallingPing(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, func(context.Context) error {
		t.Fatal("healthz must not call ping")
		return errors.New("unreachable")
	})

	rec := doGET(t, mux, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
	assertNoIndex(t, rec)
}

func TestReadyz503WhenPingErrors(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, func(context.Context) error {
		return errors.New("db down")
	})

	rec := doGET(t, mux, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", rec.Code)
	}
	assertNoIndex(t, rec)
}

func TestReadyzOKWhenPingSucceeds(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	RegisterHealth(mux, func(context.Context) error {
		called = true
		return nil
	})

	rec := doGET(t, mux, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", rec.Code)
	}
	if !called {
		t.Fatal("readyz did not call ping")
	}
	assertNoIndex(t, rec)
}

func TestReadyzOKWhenPingNil(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, nil)

	rec := doGET(t, mux, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", rec.Code)
	}
	assertNoIndex(t, rec)
}

func TestVersionReportsBuildInfo(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHealth(mux, nil)

	rec := doGET(t, mux, "/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("version status = %d, want 200", rec.Code)
	}
	assertNoIndex(t, rec)

	var got struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if got.Version != Version || got.Commit != Commit {
		t.Fatalf("version body = %+v, want version=%q commit=%q", got, Version, Commit)
	}
}

func TestEmbeddedTZDataLoadsEuropeLondon(t *testing.T) {
	if _, err := time.LoadLocation("Europe/London"); err != nil {
		t.Fatalf("LoadLocation(Europe/London): %v — embed time/tzdata; distroless has no zoneinfo", err)
	}
}

func doGET(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertNoIndex(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("X-Robots-Tag"); got != "noindex" {
		t.Fatalf("X-Robots-Tag = %q, want noindex", got)
	}
}
