package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
)

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

// sendUserCode method allows to send a code to the user via email or SMS. It
// generates a verification code and stores it in the database associated to
// the user email. If the mail service is available, it sends the verification
// code via email. If the SMS service is available, it sends the verification
// code via SMS. The code is generated associated a the type of code received,
// that can be either a verification code or a password reset code. Other types
// of codes can be added in the future. If neither the mail service nor the SMS
// service are available, the verification code will be empty but stored in the
// database to mock the verification process in any case.
func (a *API) sendUserCode(ctx context.Context, user *db.User, codeType db.CodeType) error {
	// generate verification code if the mail service is available, if not
	// the verification code will not be sent but stored in the database
	// generated with just the user email to mock the verification process
	var code string
	if a.mail != nil || a.sms != nil {
		code = util.RandomHex(VerificationCodeLength)
	}
	hashCode := internal.HashVerificationCode(user.Email, code)
	// store the verification code in the database
	if err := a.db.SetVerificationCode(&db.User{ID: user.ID}, hashCode, codeType); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second*10)
	defer cancel()
	// send the verification code via email if the mail service is available
	if a.mail != nil {
		if err := a.mail.SendNotification(ctx, &notifications.Notification{
			ToName:    fmt.Sprintf("%s %s", user.FirstName, user.LastName),
			ToAddress: user.Email,
			Subject:   VerificationCodeEmailSubject,
			Body:      VerificationCodeTextBody + code,
		}); err != nil {
			return err
		}
	} else if a.sms != nil {
		// send the verification code via SMS if the SMS service is available
		if err := a.sms.SendNotification(ctx, &notifications.Notification{
			ToNumber: user.Phone,
			Body:     VerificationCodeTextBody + code,
		}); err != nil {
			return err
		}
	}
	return nil
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
