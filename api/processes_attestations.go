package api

import (
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/attestation"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// attestedParticipantsField is the only lookup field of the participants of an attested census.
const attestedParticipantsField = "voterId"

// attestedParticipants answers GET /processes/{processId}/participants for an attested census, which
// has no members to look up: field=voterId and value=<hex voterId> name one attested voter, served
// from their attestation record with the per-question vote status. It writes the response.
func (a *API) attestedParticipants(w http.ResponseWriter, r *http.Request, vp *db.VotingProcess,
	questions []db.VotingProcessQuestion,
) {
	if r.URL.Query().Get("field") != attestedParticipantsField {
		errors.ErrMalformedBody.Withf("an attested census is looked up by field=%s", attestedParticipantsField).Write(w)
		return
	}
	var voterID internal.HexBytes
	if err := voterID.ParseString(r.URL.Query().Get("value")); err != nil || len(voterID) != common.HashLength {
		errors.ErrMalformedBody.Withf("value must be a 32-byte hex voterId").Write(w)
		return
	}
	resp := &apicommon.ProcessParticipantsResponse{Participants: []apicommon.ProcessParticipantEntry{}}
	uid := internal.HexBytes(attestation.UserID([attestation.ProcessIDLen]byte(vp.ID), common.BytesToHash(voterID)))
	if _, err := a.db.AttestationRecord(vp.ID, uid); err != nil {
		if err == db.ErrNotFound {
			apicommon.HTTPWriteJSON(w, resp)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	upstreamIDs := make([]internal.HexBytes, 0, len(questions))
	for i := range questions {
		if len(questions[i].UpstreamID) > 0 {
			upstreamIDs = append(upstreamIDs, questions[i].UpstreamID)
		}
	}
	rows, err := a.db.CSPProcessesByMembers(upstreamIDs, []string{uid.String()})
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	statuses, err := a.resolveVoteStatuses(r.Context(), rows)
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		log.Warnw("vote status lookup failed", "processId", vp.ID.Hex(), "error", err)
	}
	entry := apicommon.ProcessParticipantEntry{VoterID: voterID}
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) == 0 {
			continue
		}
		st := statuses[voteStatusKey(uid.String(), q.UpstreamID.String())]
		entry.Questions = append(entry.Questions, apicommon.ProcessParticipantQuestionVote{
			QuestionID:      q.ID.Hex(),
			UpstreamID:      q.UpstreamID,
			HasSigned:       st.Signed,
			HasVoted:        st.Voted,
			HasVotedUnknown: st.unknown,
		})
	}
	resp.Participants = append(resp.Participants, entry)
	apicommon.HTTPWriteJSON(w, resp)
}

// processAttestationsHandler godoc
//
//	@Summary		Published attestation records of an attested census
//	@Description	Public, paginated audit of an attested census once it is over: the EIP-712 domain, the
//	@Description	signer history and, per attested voter the CSP signed for, the attestation it signed
//	@Description	against and the questions it signed. Served once the process end date has passed (or
//	@Description	it has none) and every question is ENDED or RESULTS; 409 before. The records carry no
//	@Description	contact, salt or voting address.
//	@Tags			processes
//	@Produce		json
//	@Param			processId	path		string	true	"Voting process ID"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			limit		query		integer	false	"Items per page (default: 10)"
//	@Success		200			{object}	apicommon.ProcessAttestationsResponse
//	@Failure		400			{object}	errors.Error	"Not an attested census"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		409			{object}	errors.Error	"Process not over yet"
//	@Router			/processes/{processId}/attestations [get]
func (a *API) processAttestationsHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	vp, questions, err := a.db.ProcessWithQuestions(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}
	if census.Attested == nil {
		errors.ErrInvalidData.With(errCensusNotAttested).Write(w)
		return
	}
	if !attestationsPublic(vp, questions, time.Now()) {
		errors.ErrDuplicateConflict.Withf("the attestations are published once every question has ended").Write(w)
		return
	}
	domain, err := a.csp.AttestationDomain()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	total, records, err := a.db.AttestationRecords(oid, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, total)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	questionIDs := make(map[string]string, len(questions))
	for i := range questions {
		questionIDs[questions[i].UpstreamID.String()] = questions[i].ID.Hex()
	}
	resp := &apicommon.ProcessAttestationsResponse{
		Domain: apicommon.AttestationDomainInfo{
			Name:              attestation.DomainName,
			Version:           attestation.DomainVersion,
			VerifyingContract: domain.VerifyingContract,
		},
		SignerHistory: census.Attested.SignerHistory,
		MaxCensusSize: census.Size,
		Attestations:  make([]apicommon.AttestationRecordEntry, 0, len(records)),
		Pagination:    pagination,
	}
	for i := range records {
		rec := &records[i]
		entry := apicommon.AttestationRecordEntry{
			VoterID:     rec.VoterID,
			ContactHash: rec.ContactHash,
			ExpiresAt:   rec.ExpiresAt,
			Signature:   rec.Signature,
			Signer:      rec.Signer,
			Questions:   make([]apicommon.AttestedQuestion, 0, len(rec.Questions)),
		}
		for _, upstreamID := range rec.Questions {
			entry.Questions = append(entry.Questions, apicommon.AttestedQuestion{
				QuestionID: questionIDs[upstreamID.String()],
				UpstreamID: upstreamID,
			})
		}
		resp.Attestations = append(resp.Attestations, entry)
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// attestationsPublic reports whether the attestation records of vp are public: its end date has
// passed, when it has one, and every question is ENDED or RESULTS, so no record can change.
func attestationsPublic(vp *db.VotingProcess, questions []db.VotingProcessQuestion, now time.Time) bool {
	if !vp.Published || len(questions) == 0 || (!vp.EndDate.IsZero() && now.Before(vp.EndDate)) {
		return false
	}
	for i := range questions {
		if questions[i].Status != db.QuestionStatusEnded && questions[i].Status != db.QuestionStatusResults {
			return false
		}
	}
	return true
}
