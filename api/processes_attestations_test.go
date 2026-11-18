package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/attestation"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestAttestedParticipantsAndAudit: the participants of an attested census are looked up by voterId
// from the attestation records, and the public attestations endpoint opens once every question has
// ended, even before the scheduled end date, listing every signed voter with its attestation and questions.
func TestAttestedParticipantsAndAudit(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedaudit12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	key, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	signer := ethcrypto.PubkeyToAddress(key.PublicKey)
	pid, elections := publishProcessRequest(t, token, newAttestedProcessRequest(orgAddress, signer, 10))
	oid := objectID(c, pid)

	voters := []common.Hash{{0x11}, {0x12}}
	for i, voter := range voters {
		auth := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
			attestedAuthRequest(t, key, oid, voter, "", "", time.Now().Add(10*time.Minute)), "processes", pid, "auth", "0")
		// the first voter signs both questions, the second only the first one
		batch := &handlers.SignBatchRequest{AuthToken: auth.AuthToken}
		for _, e := range elections[:len(elections)-i] {
			batch.Ballots = append(batch.Ballots, handlers.SignBatchBallot{UpstreamID: e, Address: common.Address{0x42}.Bytes()})
		}
		requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", batch, "processes", pid, "sign-batch")
	}

	found := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
		"processes", pid, "participants?field=voterId&value="+voters[1].Hex()[2:])
	c.Assert(found.Participants, qt.HasLen, 1)
	c.Assert(found.Participants[0].VoterID.String(), qt.Equals, voters[1].Hex()[2:])
	c.Assert(found.Participants[0].Questions, qt.HasLen, 2)
	c.Assert(found.Participants[0].Questions[0].HasSigned, qt.IsTrue)
	c.Assert(found.Participants[0].Questions[1].HasSigned, qt.IsFalse)
	missing := requestAndParse[apicommon.ProcessParticipantsResponse](t, http.MethodGet, token, nil,
		"processes", pid, "participants?field=voterId&value="+common.Hash{0x99}.Hex()[2:])
	c.Assert(missing.Participants, qt.HasLen, 0)
	byEmail := requestAndParseWithAssertCode[errors.Error](http.StatusBadRequest, t, http.MethodGet, token, nil,
		"processes", pid, "participants?field=email&value=a@b.org")
	c.Assert(byEmail.Code, qt.Equals, errors.ErrMalformedBody.Code)

	// still open: no audit yet
	requestAndAssertError(errors.ErrProcessNotEnded, t, http.MethodGet, "", nil, "processes", pid, "attestations")

	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.EndDate.After(time.Now()), qt.IsTrue)
	// ended early, one question at a time: the audit waits for the last one, not for the end date
	c.Assert(testDB.SetQuestionStatus(questions[0].ID, db.QuestionStatusEnded), qt.IsNil)
	requestAndAssertError(errors.ErrProcessNotEnded, t, http.MethodGet, "", nil, "processes", pid, "attestations")
	for _, q := range questions[1:] {
		c.Assert(testDB.SetQuestionStatus(q.ID, db.QuestionStatusEnded), qt.IsNil)
	}

	audit := requestAndParse[apicommon.ProcessAttestationsResponse](t, http.MethodGet, "", nil,
		"processes", pid, "attestations?limit=1")
	domain, err := testCSP.AttestationDomain()
	c.Assert(err, qt.IsNil)
	c.Assert(audit.Domain.Name, qt.Equals, attestation.DomainName)
	c.Assert(audit.Domain.VerifyingContract, qt.Equals, domain.VerifyingContract)
	c.Assert(audit.SignerHistory, qt.HasLen, 1)
	c.Assert(audit.MaxCensusSize, qt.Equals, int64(10))
	c.Assert(audit.Pagination.TotalItems, qt.Equals, int64(2))
	c.Assert(audit.Attestations, qt.HasLen, 1)

	// every published record verifies against the domain and the signer of its time
	page2 := requestAndParse[apicommon.ProcessAttestationsResponse](t, http.MethodGet, "", nil,
		"processes", pid, "attestations?limit=1&page=2")
	for _, rec := range append(audit.Attestations, page2.Attestations...) {
		att := attestation.Attestation{
			VoterID:     common.BytesToHash(rec.VoterID),
			ContactHash: common.BytesToHash(rec.ContactHash),
			ExpiresAt:   uint64(rec.ExpiresAt),
		}
		copy(att.ProcessID[:], oid[:])
		_, err := domain.Verify(&att, rec.Signature, signer)
		c.Assert(err, qt.IsNil)
		c.Assert(rec.Questions[0].QuestionID, qt.Not(qt.Equals), "")
	}
}

// TestAttestationsPublic: the audit opens on the questions' terminal status alone, so an early end
// publishes before the scheduled end date and a single open question keeps it closed.
func TestAttestationsPublic(t *testing.T) {
	future := &db.VotingProcess{Published: true, EndDate: time.Now().Add(time.Hour)}
	questions := func(statuses ...string) []db.VotingProcessQuestion {
		qs := make([]db.VotingProcessQuestion, len(statuses))
		for i, s := range statuses {
			qs[i].Status = s
		}
		return qs
	}
	for _, tc := range []struct {
		name      string
		vp        *db.VotingProcess
		questions []db.VotingProcessQuestion
		want      bool
	}{
		{"ended early", future, questions(db.QuestionStatusEnded, db.QuestionStatusResults), true},
		{"one question open", future, questions(db.QuestionStatusEnded, db.QuestionStatusReady), false},
		{"paused", future, questions(db.QuestionStatusPaused), false},
		{"no end date", &db.VotingProcess{Published: true}, questions(db.QuestionStatusResults), true},
		{"unpublished", &db.VotingProcess{EndDate: time.Now().Add(-time.Hour)}, questions(db.QuestionStatusEnded), false},
		{"no questions", future, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qt.Assert(t, attestationsPublic(tc.vp, tc.questions), qt.Equals, tc.want)
		})
	}
}
