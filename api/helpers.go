package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

// sendNotification method sends a notification to the email provided. It
// requires the email, the name of the recipient, the subject, the plain body,
// the mail template and the data to fill the template. It returns an error if
// the mail service is available and the notification could not be sent. If the
// mail service is not available, the notification is not sent but the function
// returns nil.
func (a *API) sendNotification(ctx context.Context, email, name, subject,
	plainbody string, temp notifications.MailTemplate, data any,
) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	// send the verification code via email if the mail service is available
	if a.mail != nil {
		// create the notification with the verification code
		notification := &notifications.Notification{
			ToName:    name,
			ToAddress: email,
			Subject:   subject,
			PlainBody: plainbody,
			Body:      plainbody,
		}
		// execute the template with the data provided
		if err := notification.ExecTemplate(a.mailTemplates[temp], data); err != nil {
			return err
		}
		// send the notification
		if err := a.mail.SendNotification(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}

// organizationFromRequest helper function allows to get the organization info
// related to the request provided. It gets the organization address from the
// URL parameters and retrieves the organization from the database. If the
// organization is a suborganization, it also retrieves the parent organization.
func (a *API) organizationFromRequest(r *http.Request) (*db.Organization, *db.Organization, bool) {
	orgAddress := chi.URLParam(r, "address")
	// if the organization address is not empty, get the organization from
	// the database and add it to the context
	if orgAddress != "" {
		// get the organization from the database
		if org, parent, err := a.db.Organization(orgAddress, true); err == nil {
			return org, parent, true
		}
	}
	return nil, nil, false
}

// buildLoginResponse creates a JWT token for the given user identifier.
// The token is signed with the API secret, following the JWT specification.
// The token is valid for the period specified on jwtExpiration constant.
func (a *API) buildLoginResponse(id string) (*LoginResponse, error) {
	j := jwt.New()
	if err := j.Set("userId", id); err != nil {
		return nil, err
	}
	if err := j.Set(jwt.ExpirationKey, time.Now().Add(jwtExpiration).UnixNano()); err != nil {
		return nil, err
	}
	lr := LoginResponse{}
	lr.Expirity = time.Now().Add(jwtExpiration)
	jmap, err := j.AsMap(context.Background())
	if err != nil {
		return nil, err
	}
	_, lr.Token, _ = a.auth.Encode(jmap)
	return &lr, nil
}

// buildWebAppURL method allows to build a URL for the web application using
// the path and the parameters provided. It returns the URL as a string and an
// error if the URL could not be built. It encodes the parameters in the query
// string of the URL to prevent any issues with special characters. It returns
// the URL as a string and an error if the URL could not be built.
func (a *API) buildWebAppURL(path string, params map[string]any) (string, error) {
	// parse the web app URL with the path provided
	url, err := url.Parse(a.webAppURL + path)
	if err != nil {
		return "", err
	}
	// encode the parameters in the query string of the URL
	q := url.Query()
	for k, v := range params {
		q.Set(k, fmt.Sprint(v))
	}
	// include the encoded query string in the URL
	url.RawQuery = q.Encode()
	return url.String(), nil
}

// httpWriteJSON helper function allows to write a JSON response.
func httpWriteJSON(w http.ResponseWriter, data interface{}) {
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

// httpWriteOK helper function allows to write an OK response.
func httpWriteOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}
