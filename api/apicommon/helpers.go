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
	"go.mongodb.org/mongo-driver/bson/primitive"
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

// CensusIDFromRequest extracts and validates CensusID from URL parameters.
// It returns the CensusID as internal.HexBytes or an error if the parameter
// is missing or invalid.
func CensusIDFromRequest(r *http.Request) (internal.HexBytes, error) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "censusId")); err != nil {
		return nil, fmt.Errorf("invalid census ID: %w", err)
	}
	return censusID, nil
}

// GroupIDFromRequest extracts and validates GroupID from URL parameters.
// It returns the GroupID as primitive.ObjectID or an error if the parameter
// is missing or invalid.
func GroupIDFromRequest(r *http.Request) (primitive.ObjectID, error) {
	groupIDStr := chi.URLParam(r, "groupId")
	if groupIDStr == "" {
		return primitive.NilObjectID, fmt.Errorf("group ID is required")
	}
	groupID, err := primitive.ObjectIDFromHex(groupIDStr)
	if err != nil {
		return primitive.NilObjectID, fmt.Errorf("invalid group ID: %w", err)
	}
	return groupID, nil
}

// JobIDFromRequest extracts and validates JobID from URL parameters.
// It returns the JobID as internal.HexBytes or an error if the parameter
// is missing or invalid.
func JobIDFromRequest(r *http.Request) (internal.HexBytes, error) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobId")); err != nil {
		return nil, fmt.Errorf("invalid job ID: %w", err)
	}
	return jobID, nil
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

// BundleIDFromRequest extracts and validates BundleID from URL parameters.
// It returns the BundleID as internal.HexBytes or an error if the parameter
// is missing or invalid.
func BundleIDFromRequest(r *http.Request) (internal.HexBytes, error) {
	bundleID := internal.HexBytes{}
	if err := bundleID.ParseString(chi.URLParam(r, "bundleId")); err != nil {
		return nil, fmt.Errorf("invalid bundle ID: %w", err)
	}
	return bundleID, nil
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
