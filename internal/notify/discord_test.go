package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscordClientSend(t *testing.T) {
	cases := []struct {
		name string
		// statuses is returned in order, one per request; requests past the
		// end of the slice repeat the last entry.
		statuses     []int
		retryAfter   float64 // seconds, echoed in the 429 body
		wantErr      bool
		wantAttempts int32
		wantSleep    time.Duration // 0 means "assert sleep was never called"
		checkErr     func(t *testing.T, err error)
	}{
		{
			name:         "200 succeeds on first attempt",
			statuses:     []int{200},
			wantErr:      false,
			wantAttempts: 1,
		},
		{
			name:         "429 with retry_after sleeps then retries and succeeds",
			statuses:     []int{429, 200},
			retryAfter:   0.05,
			wantErr:      false,
			wantAttempts: 2,
			wantSleep:    50 * time.Millisecond,
		},
		{
			name:         "404 is permanent and never retries",
			statuses:     []int{404, 200}, // second entry proves it's never reached
			wantErr:      true,
			wantAttempts: 1,
			checkErr: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "permanent") {
					t.Errorf("error = %q, want it to describe a permanent failure", err.Error())
				}
			},
		},
		{
			name:         "500 retries up to 3 total then fails",
			statuses:     []int{500, 500, 500, 200}, // 4th entry proves cap at 3
			wantErr:      true,
			wantAttempts: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&attempts, 1)
				idx := int(n) - 1
				if idx >= len(tc.statuses) {
					idx = len(tc.statuses) - 1
				}
				status := tc.statuses[idx]
				if status == http.StatusTooManyRequests {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(map[string]float64{"retry_after": tc.retryAfter})
					return
				}
				w.WriteHeader(status)
			}))
			defer srv.Close()

			var sleptFor time.Duration
			var sleptCalls int
			client := newDiscordClient(srv.URL)
			client.httpClient = srv.Client()
			client.sleep = func(d time.Duration) {
				sleptFor = d
				sleptCalls++
			}

			err := client.send(context.Background(), Message{Title: "t", Description: "d"})

			if (err != nil) != tc.wantErr {
				t.Fatalf("send() error = %v, wantErr %v", err, tc.wantErr)
			}
			if got := atomic.LoadInt32(&attempts); got != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", got, tc.wantAttempts)
			}
			if tc.wantSleep == 0 {
				if sleptCalls != 0 {
					t.Errorf("sleep called %d time(s), want 0", sleptCalls)
				}
			} else {
				if sleptCalls != 1 {
					t.Errorf("sleep called %d time(s), want 1", sleptCalls)
				}
				if sleptFor != tc.wantSleep {
					t.Errorf("slept for %s, want %s", sleptFor, tc.wantSleep)
				}
			}
			if tc.checkErr != nil && err != nil {
				tc.checkErr(t, err)
			}
		})
	}
}

func TestDiscordClientSendNeverLeaksWebhookURL(t *testing.T) {
	const marker = "super-secret-webhook-token-4f8a"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	webhookURL := srv.URL + "/webhooks/1/" + marker
	srv.Close() // closed: every request now fails at the transport level

	client := newDiscordClient(webhookURL)
	client.sleep = func(time.Duration) {}

	err := client.send(context.Background(), Message{Title: "t", Description: "d"})
	if err == nil {
		t.Fatal("send() error = nil, want a transport error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("send() error leaked the webhook URL: %q", err.Error())
	}
	if strings.Contains(err.Error(), webhookURL) {
		t.Fatalf("send() error leaked the webhook URL: %q", err.Error())
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{name: "float seconds", body: `{"retry_after":1.5}`, want: 1500 * time.Millisecond},
		{name: "missing field defaults to one second", body: `{}`, want: time.Second},
		{name: "malformed json defaults to one second", body: `not json`, want: time.Second},
		{name: "zero defaults to one second", body: `{"retry_after":0}`, want: time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRetryAfter([]byte(tc.body))
			if got != tc.want {
				t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.body, got, tc.want)
			}
		})
	}
}

func TestSanitizeTransportErrorNeverLeaksURL(t *testing.T) {
	const marker = "leaked-token-xyz"
	client := newDiscordClient(fmt.Sprintf("http://127.0.0.1:1/%s", marker))
	client.httpClient = &http.Client{Timeout: 100 * time.Millisecond}
	client.sleep = func(time.Duration) {}

	err := client.send(context.Background(), Message{Title: "t"})
	if err == nil {
		t.Fatal("send() error = nil, want a connection error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Fatalf("send() error leaked the webhook URL: %q", err.Error())
	}
}
