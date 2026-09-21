// Package health serves the liveness and readiness probes.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofreego/goutils/logger"
)

const checkTimeout = 2 * time.Second

// Pinger is the dependency a readiness check consults.
type Pinger interface {
	Ping(ctx context.Context) error
}

type response struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Live reports that the process is running. It checks **nothing else**, on
// purpose.
//
// Liveness failure means "restart me". If this checked the database, a brief
// database outage would fail the probe on every pod at once and the
// orchestrator would restart the entire service — turning a recoverable
// dependency blip into a full outage, and adding a thundering herd of
// reconnects to a database that is already struggling.
func Live() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(r.Context(), w, http.StatusOK, response{Status: "ok"})
	}
}

// Ready reports whether the service can serve traffic, which for OpenPay means
// the database is reachable: every endpoint that matters reads or writes it.
//
// Readiness failure removes the pod from load balancing without restarting it,
// so it recovers on its own when the dependency comes back.
func Ready(db Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
		defer cancel()

		if err := db.Ping(ctx); err != nil {
			logger.Warn(ctx, "readiness check failed: %v", err)
			writeJSON(ctx, w, http.StatusServiceUnavailable, response{
				Status: "unavailable",
				Detail: "database unreachable",
			})
			return
		}
		writeJSON(ctx, w, http.StatusOK, response{Status: "ok"})
	}
}

func writeJSON(ctx context.Context, w http.ResponseWriter, status int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logger.Error(ctx, "failed to write health response: %v", err)
	}
}
