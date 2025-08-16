package apicommon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// UserFromContext retrieves the user from the context provided, expected to be
// the context of a request handled by the authenticator middleware.
func UserFromContext(ctx context.Context) (*db.User, bool) {
	rawUser, ok := ctx.Value(UserMetadataKey).(db.User)
	if ok {
		return &rawUser, ok
	}
	return nil, false
}

// ProcessIDFromRequest extracts and validates ProcessID from URL parameters.
// It returns the ProcessID as internal.HexBytes or an error if the parameter
// is missing or invalid.
func ProcessIDFromRequest(r *http.Request) (internal.HexBytes, error) {
	processID := internal.HexBytes{}
	if err := processID.ParseString(chi.URLParam(r, "processId")); err != nil {
		return nil, fmt.Errorf("invalid process ID: %w", err)
	}
	return processID, nil
}

// HTTPWriteJSON helper function allows to write a JSON response.
func HTTPWriteJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}

// HTTPWriteOK helper function allows to write an OK response.
func HTTPWriteOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}
