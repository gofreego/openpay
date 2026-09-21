package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePinger struct {
	err   error
	delay time.Duration
}

func (f fakePinger) Ping(ctx context.Context) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	var body response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	return body
}

// Liveness answers for the process alone. If it consulted the database, a brief
// outage would fail this probe on every pod at once and the orchestrator would
// restart the whole service — turning a recoverable blip into a real outage.
func TestLiveIsIndependentOfDependencies(t *testing.T) {
	rec := httptest.NewRecorder()
	Live()(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := decode(t, rec).Status; got != "ok" {
		t.Errorf("status field = %q, want ok", got)
	}
}

func TestReadyWhenDatabaseIsReachable(t *testing.T) {
	rec := httptest.NewRecorder()
	Ready(fakePinger{})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if got := decode(t, rec).Status; got != "ok" {
		t.Errorf("status field = %q, want ok", got)
	}
}

// Readiness failure takes the pod out of rotation without restarting it, so it
// recovers by itself once the dependency returns.
func TestReadyWhenDatabaseIsDown(t *testing.T) {
	rec := httptest.NewRecorder()
	Ready(fakePinger{err: errors.New("connection refused")})(
		rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	body := decode(t, rec)
	if body.Status != "unavailable" {
		t.Errorf("status field = %q, want unavailable", body.Status)
	}
	// The detail says which dependency, without quoting the driver error at
	// whoever can reach the probe.
	if body.Detail != "database unreachable" {
		t.Errorf("detail = %q, want a generic dependency description", body.Detail)
	}
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Error("probe response leaked the underlying driver error")
	}
}

// A hung database must not hang the probe, or the orchestrator's own timeout
// becomes the only thing bounding it.
func TestReadyTimesOutOnAHungDatabase(t *testing.T) {
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		Ready(fakePinger{delay: 10 * time.Second})(
			rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		close(done)
	}()

	select {
	case <-done:
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rec.Code)
		}
	case <-time.After(checkTimeout + 2*time.Second):
		t.Fatal("readiness probe did not return; it must bound its own check")
	}
}
