package handlers

import (
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// ProcessPastEnd reports whether the process is past its end (plus endGrace), when it has one: the
// window the signing endpoints enforce, exported for the certificate sign-in.
func ProcessPastEnd(vp *db.VotingProcess) bool { return processEnded(vp) }

// ProcessBeforeStart reports whether the process start is still more than startGrace ahead. A zero
// StartDate counts as started, as unvotableElection has it.
func ProcessBeforeStart(vp *db.VotingProcess) bool {
	return !vp.StartDate.IsZero() && time.Now().Add(startGrace).Before(vp.StartDate)
}

// CertificateCensusError is the auth/0 answer for a certificate census (contract section 2): its
// voters sign in only through the certificate endpoints, whose tokens are verified already.
func CertificateCensusError() errors.Error {
	return errors.CertificateAuthError(http.StatusBadRequest, "this census signs in with a digital certificate",
		apicommon.CertAuthErrorData{Reason: apicommon.CertAuthReasonCertificateCensus})
}
