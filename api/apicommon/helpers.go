package apicommon

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/vocdoni/saas-backend/db"
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

// PaginationFromRequest parse pagination parameters from query string
func PaginationFromRequest(r *http.Request) *Pagination {
	pagination := &Pagination{
		CurrentPage: 1,  // Default page number
		PageSize:    10, // Default page size
	}

	// Parse pagination parameters from query string
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if pageVal, err := strconv.ParseInt(pageStr, 10, 64); err == nil && pageVal > 0 {
			pagination.CurrentPage = pageVal
		}
	}

	if pageSizeStr := r.URL.Query().Get("pageSize"); pageSizeStr != "" {
		if pageSizeVal, err := strconv.ParseInt(pageSizeStr, 10, 64); err == nil && pageSizeVal > 0 {
			pagination.PageSize = pageSizeVal
		}
	}

	return pagination
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
