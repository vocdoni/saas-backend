package api

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/errors"
)

// maintenanceHeader carries the scheduled window on every response until it ends, so
// integrators can detect it before it starts.
const maintenanceHeader = "X-Maintenance-Window"

// MaintenanceWindow is a scheduled downtime of the chain behind the API. Inside it, writes are
// refused with ErrMaintenance; reads keep working, except those that need the chain.
type MaintenanceWindow struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
}

// ParseMaintenanceWindow parses "<from>/<until>" in RFC 3339. An empty string is no window.
func ParseMaintenanceWindow(s string) (*MaintenanceWindow, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	fromStr, untilStr, ok := strings.Cut(s, "/")
	if !ok {
		return nil, fmt.Errorf("maintenance window %q: want <from>/<until> in RFC 3339", s)
	}
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		return nil, fmt.Errorf("maintenance window start: %w", err)
	}
	until, err := time.Parse(time.RFC3339, untilStr)
	if err != nil {
		return nil, fmt.Errorf("maintenance window end: %w", err)
	}
	if !until.After(from) {
		return nil, fmt.Errorf("maintenance window %q ends before it starts", s)
	}
	return &MaintenanceWindow{From: from.UTC(), Until: until.UTC()}, nil
}

// String formats the window as the header value, "<from>/<until>" in RFC 3339.
func (m *MaintenanceWindow) String() string {
	return m.From.Format(time.RFC3339) + "/" + m.Until.Format(time.RFC3339)
}

// pending reports whether the window has not ended yet at now.
func (m *MaintenanceWindow) pending(now time.Time) bool {
	return m != nil && now.Before(m.Until)
}

// active reports whether now is inside the window.
func (m *MaintenanceWindow) active(now time.Time) bool {
	return m.pending(now) && !now.Before(m.From)
}

// maintenanceExempt are the writes that never touch the chain and that clients need to keep
// working through the window: sessions, the Stripe webhook and the platform health check.
var maintenanceExempt = map[string]bool{
	authLoginEndpoint:       true,
	authRefresTokenEndpoint: true,
	paymentsWebhook:         true,
	"/ping":                 true,
}

// maintenance announces the window on every response until it ends, and inside it refuses
// writes with ErrMaintenance and a Retry-After pointing at the end of the window.
func maintenance(m *MaintenanceWindow, now func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t := now()
			if !m.pending(t) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set(maintenanceHeader, m.String())
			if !m.active(t) || isReadMethod(r.Method) || maintenanceExempt[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}
			secs := int(math.Ceil(m.Until.Sub(t).Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
			errors.ErrMaintenance.Write(w)
		})
	}
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}
