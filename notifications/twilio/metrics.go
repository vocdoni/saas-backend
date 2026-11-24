package twilio

import (
	"errors"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/twilio/twilio-go/client"
)

// Send results recorded in the sends counter.
const (
	sendAccepted = "accepted" // Twilio accepted the message
	sendRejected = "rejected" // Twilio answered with an API error (code label set)
	sendError    = "error"    // no Twilio answer: network error, HTTP client timeout
)

// Traffic sources, so canary messages can be told apart from voter codes.
const (
	sourceVoter  = "voter"
	sourceCanary = "canary"
)

var (
	smsSends = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saas_sms_sends_total",
		Help: "SMS send attempts by source (voter, canary), result (accepted, rejected, error) and Twilio error code.",
	}, []string{"source", "result", "code"})
	smsStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saas_sms_status_total",
		Help: "SMS delivery status callbacks received from Twilio, by source, status and Twilio error code.",
	}, []string{"source", "status", "code"})
	smsLastDelivered = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "saas_sms_last_delivered_timestamp_seconds",
		Help: "Unix time of the last SMS Twilio reported as delivered; set to the start time when the service starts.",
	})
)

// knownStatuses bounds the status label to the values Twilio documents for SMS.
var knownStatuses = map[string]struct{}{
	"accepted": {}, "scheduled": {}, "queued": {}, "sending": {}, "sent": {},
	"delivered": {}, "undelivered": {}, "failed": {}, "canceled": {},
}

// boundSource folds anything but the canary into the voter source.
func boundSource(source string) string {
	if source == sourceCanary {
		return sourceCanary
	}
	return sourceVoter
}

// recordSend counts the outcome of one CreateMessage call.
func recordSend(source string, err error) {
	source = boundSource(source)
	if err == nil {
		smsSends.WithLabelValues(source, sendAccepted, "").Inc()
		return
	}
	var restErr *client.TwilioRestError
	if errors.As(err, &restErr) {
		smsSends.WithLabelValues(source, sendRejected, strconv.Itoa(restErr.Code)).Inc()
		return
	}
	smsSends.WithLabelValues(source, sendError, "").Inc()
}

// recordStatus counts one delivery status callback. Unknown statuses are
// folded into "other" so a malformed callback can't grow the label set.
func recordStatus(source, status, code string, now time.Time) {
	if _, ok := knownStatuses[status]; !ok {
		status = "other"
	}
	if _, err := strconv.Atoi(code); err != nil {
		code = ""
	}
	smsStatus.WithLabelValues(boundSource(source), status, code).Inc()
	if status == "delivered" {
		smsLastDelivered.Set(float64(now.Unix()))
	}
}
