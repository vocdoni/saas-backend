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
// computed over the configured callback URL plus the request's query string
// (which names the source): behind the edge proxy the scheme and host the
// server sees differ from the ones Twilio signed.
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
	signedURL := tsms.config.StatusCallbackURL
	if r.URL.RawQuery != "" {
		signedURL += "?" + r.URL.RawQuery
	}
	validator := client.NewRequestValidator(tsms.config.AuthToken)
	if !validator.Validate(signedURL, params, r.Header.Get(signatureHeader)) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	source := boundSource(r.URL.Query().Get("source"))
	status, code := params["MessageStatus"], params["ErrorCode"]
	recordStatus(source, status, code, time.Now())
	if status == "undelivered" || status == "failed" {
		log.Warnw("sms not delivered", "source", source, "status", status, "code", code, "sid", params["MessageSid"])
	}
	w.WriteHeader(http.StatusNoContent)
}
