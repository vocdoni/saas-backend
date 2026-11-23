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
	sendError    = "error"    // no Twilio answer: network error, timeout, cancelled context
)

var (
	smsSends = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saas_sms_sends_total",
		Help: "SMS send attempts by result (accepted, rejected, error) and Twilio error code.",
	}, []string{"result", "code"})
	smsStatus = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "saas_sms_status_total",
		Help: "SMS delivery status callbacks received from Twilio, by status and Twilio error code.",
	}, []string{"status", "code"})
	smsLastDelivered = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "saas_sms_last_delivered_timestamp_seconds",
		Help: "Unix time of the last SMS Twilio reported as delivered (0 until one is, after each restart).",
	})
)

// knownStatuses bounds the status label to the values Twilio documents for SMS.
var knownStatuses = map[string]struct{}{
	"accepted": {}, "scheduled": {}, "queued": {}, "sending": {}, "sent": {},
	"delivered": {}, "undelivered": {}, "failed": {}, "canceled": {},
}

// recordSend counts the outcome of one CreateMessage call.
func recordSend(err error) {
	if err == nil {
		smsSends.WithLabelValues(sendAccepted, "").Inc()
		return
	}
	var restErr *client.TwilioRestError
	if errors.As(err, &restErr) {
		smsSends.WithLabelValues(sendRejected, strconv.Itoa(restErr.Code)).Inc()
		return
	}
	smsSends.WithLabelValues(sendError, "").Inc()
}

// recordStatus counts one delivery status callback. Unknown statuses are
// folded into "other" so a malformed callback can't grow the label set.
func recordStatus(status, code string, now time.Time) {
	if _, ok := knownStatuses[status]; !ok {
		status = "other"
	}
	if _, err := strconv.Atoi(code); err != nil {
		code = ""
	}
	smsStatus.WithLabelValues(status, code).Inc()
	if status == "delivered" {
		smsLastDelivered.Set(float64(now.Unix()))
	}
}
