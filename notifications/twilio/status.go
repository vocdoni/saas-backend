package twilio

import (
	"net/http"
	"time"

	"github.com/twilio/twilio-go/client"
	"go.vocdoni.io/dvote/log"
)

// signatureHeader carries Twilio's HMAC of the callback URL and form parameters.
const signatureHeader = "X-Twilio-Signature"

// HandleStatusCallback receives the delivery status Twilio posts to
// Config.StatusCallbackURL for every message sent with it. Failures
// that happen after Twilio accepts a message (carrier filtering, unreachable
// handset) only show up here. The request must carry a valid Twilio signature,
// computed over the configured callback URL: behind the edge proxy the URL the
// server sees differs from the one Twilio signed.
func (tsms *SMS) HandleStatusCallback(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	params := make(map[string]string, len(r.PostForm))
	for k, v := range r.PostForm {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}
	validator := client.NewRequestValidator(tsms.config.AuthToken)
	if !validator.Validate(tsms.config.StatusCallbackURL, params, r.Header.Get(signatureHeader)) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	status, code := params["MessageStatus"], params["ErrorCode"]
	recordStatus(status, code, time.Now())
	if status == "undelivered" || status == "failed" {
		log.Warnw("sms not delivered", "status", status, "code", code, "sid", params["MessageSid"])
	}
	w.WriteHeader(http.StatusNoContent)
}
