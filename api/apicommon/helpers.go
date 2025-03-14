package apicommon

import (
	"context"
	"encoding/json"
	"net/http"

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

// HttpWriteJSON helper function allows to write a JSON response.
func HttpWriteJSON(w http.ResponseWriter, data interface{}) {
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

// HttpWriteOK helper function allows to write an OK response.
func HttpWriteOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}
