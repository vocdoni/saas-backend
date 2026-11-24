package api

import (
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/errors"
)

// maintenanceHeader carries the scheduled window on every response until it ends, so
// integrators can detect it before it starts.
const maintenanceHeader = "X-Maintenance-Window"

// maintenanceMaxDuration is the longest window ParseMaintenanceWindow accepts;
// it catches obvious typos such as an off-by-one year in the end date.
const maintenanceMaxDuration = 24 * time.Hour

// MaintenanceWindow is a scheduled downtime of the chain behind the API. Inside it, writes are
// refused with ErrMaintenance; reads keep working, except those that need the chain.
type MaintenanceWindow struct {
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
}

// ParseMaintenanceWindow parses "<from>/<until>" in RFC 3339. An empty string is no window.
// Both endpoints are truncated to second precision and the window must not exceed 24 hours.
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
	// Truncate to second precision so String() and stored values agree.
	from = from.UTC().Truncate(time.Second)
	until = until.UTC().Truncate(time.Second)
	if !until.After(from) {
		return nil, fmt.Errorf("maintenance window %q ends before it starts", s)
	}
	if until.Sub(from) > maintenanceMaxDuration {
		return nil, fmt.Errorf("maintenance window %q: duration exceeds 24h", s)
	}
	return &MaintenanceWindow{From: from, Until: until}, nil
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

// maintenance announces the window on every response until it ends by setting the
// X-Maintenance-Window header. Individual write routes are blocked via
// maintenanceWriteBlock so that readers and read-only POSTs work through the window.
func maintenance(m *MaintenanceWindow, now func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if m.pending(now()) {
				w.Header().Set(maintenanceHeader, m.String())
			}
			next.ServeHTTP(w, r)
		})
	}
}

// maintenanceWriteBlock wraps h so that if the window is active, the request is refused
// with ErrMaintenance and a jittered Retry-After hint pointing past the end of the window.
// Apply to handlers that write to the chain or to critical shared state.
// If m is nil the wrapper is a no-op and h is returned unchanged.
func maintenanceWriteBlock(m *MaintenanceWindow, now func() time.Time, h http.HandlerFunc) http.HandlerFunc {
	if m == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		t := now()
		if m.active(t) {
			// Jitter 0–60 s so clients spread retries instead of stampeding at exactly Until.
			secs := int(math.Ceil(m.Until.Sub(t).Seconds())) + rand.Intn(61)
			w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
			errors.ErrMaintenance.Write(w)
			return
		}
		h(w, r)
	}
}
