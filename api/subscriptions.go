package api

import (
	"net/http"
)

// getSubscriptionsHandler handles the request to get the subscriptions of an organization.
// It returns the list of subscriptions with their information.
func (a *API) getSubscriptionsHandler(w http.ResponseWriter, r *http.Request) {
	// get the subscritions from the database
	subscriptions, err := a.db.Subscriptions()
	if err != nil {
		ErrGenericInternalServerError.Withf("could not get subscriptions: %v", err).Write(w)
		return
	}
	// send the subscriptions back to the user
	httpWriteJSON(w, subscriptions)
}
