package apicommon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

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
// It returns the ProcessID as internal.ObjectID or an error if the parameter
// is missing or invalid.
func ProcessIDFromRequest(r *http.Request) (internal.ObjectID, error) {
	return objectIDFromRequest(r, "processId")
}

func objectIDFromRequest(r *http.Request, key string) (internal.ObjectID, error) {
	paramStr := chi.URLParam(r, key)
	if paramStr == "" {
		return internal.NilObjectID, fmt.Errorf("param %s is required", key)
	}
	groupID, err := internal.ObjectIDFromHex(paramStr)
	if err != nil {
		return internal.NilObjectID, fmt.Errorf("invalid %s: %w", key, err)
	}
	return groupID, nil
}

// CensusIDFromRequest extracts and validates CensusID from URL parameters.
// It returns the CensusID as internal.ObjectID or an error if the parameter
// is missing or invalid.
func CensusIDFromRequest(r *http.Request) (internal.ObjectID, error) {
	return objectIDFromRequest(r, "censusId")
}

// GroupIDFromRequest extracts and validates GroupID from URL parameters.
// It returns the GroupID as internal.ObjectID or an error if the parameter
// is missing or invalid.
func GroupIDFromRequest(r *http.Request) (internal.ObjectID, error) {
	return objectIDFromRequest(r, "groupId")
}

// BundleIDFromRequest extracts and validates GroupID from URL parameters.
// It returns the GroupID as internal.ObjectID or an error if the parameter
// is missing or invalid.
func BundleIDFromRequest(r *http.Request) (internal.ObjectID, error) {
	return objectIDFromRequest(r, "bundleId")
}

// JobIDFromRequest extracts and validates JobID from URL parameters.
// It returns the JobID as internal.ObjectID or an error if the parameter
// is missing or invalid.
func JobIDFromRequest(r *http.Request) (internal.ObjectID, error) {
	return objectIDFromRequest(r, "jobId")
}

// UserIDFromRequest extracts and validates UserID from URL parameters.
// It returns the UserID as uint64 or an error if the parameter
// is missing or invalid.
func UserIDFromRequest(r *http.Request) (uint64, error) {
	userIDStr := chi.URLParam(r, "userId")
	if userIDStr == "" {
		return 0, fmt.Errorf("user ID is required")
	}
	userID, err := strconv.ParseUint(userIDStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid user ID: %w", err)
	}
	return userID, nil
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
