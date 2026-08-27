package apicommon

//revive:disable:max-public-structs

import (
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// CensusSpec is the inline census definition of a voting process. The census type is
// inferred from the auth/2FA fields; there is no prebuilt-by-id reference over the API.
type CensusSpec struct {
	Weighted bool `json:"weighted"`
	// Anonymous selects blind-CSP voting: the process publishes with census origin OFF_CHAIN_CA_V2
	// and the CSP blind-signs each ballot so it cannot link a signature to the voter. Orthogonal to
	// the mail/sms/auth 2FA channel; may be combined with Weighted (V2 binds the weight into the salt).
	Anonymous   bool                    `json:"anonymous,omitempty"`
	AuthFields  db.OrgMemberAuthFields  `json:"authFields,omitempty"`
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
	// GroupID is the org member group the census was built from. Round-trips: it is echoed back on
	// process reads, so a client can restore the group a draft targeted. Absent (not a zero id) when
	// the census is organization-wide.
	GroupID string `json:"groupId,omitempty"`
	// MemberIDs selects the census members explicitly, as an alternative to GroupID. Write-only on
	// this type: a process read echoes the census config, not the member list it resolved to. The
	// list itself is read through the deprecated GET /census/{id}/participants (Manager/Admin).
	MemberIDs []string `json:"memberIds,omitempty"`
	// Size is the number of members in the census. Response-only (ignored on create/update): for a
	// published process it equals the on-chain maxCensusSize of its whole-census questions.
	Size int64 `json:"size,omitempty"`
	// TotalWeight is the whole-census total voting weight (sum of members' weights). Response-only;
	// equals Size for a non-weighted census. Needed by clients (e.g. the results report) to turn
	// per-answer weights into percentages.
	TotalWeight int64 `json:"totalWeight,omitempty"`
}

// EligibilitySpec is an optional per-question subset of the process census, resolved to a
// list of member ids. Empty means every census member is eligible.
type EligibilitySpec struct {
	GroupID   string   `json:"groupId,omitempty"`
	MemberIDs []string `json:"memberIds,omitempty"`
}

// UpdatedAtLayout is the wire format of the conditional-update token carried by
// VotingProcessResponse.UpdatedAt and CreateVotingProcessRequest.UpdatedAt: RFC3339 with the
// millisecond precision mongo stores, so a value read and echoed back compares equal.
const UpdatedAtLayout = "2006-01-02T15:04:05.000Z"

// VotingProcessQuestionRequest is one question in a create/update request.
type VotingProcessQuestionRequest struct {
	Title             db.MultiLangString   `json:"title"`
	Description       db.MultiLangString   `json:"description,omitempty"`
	Choices           []db.Choice          `json:"choices"`
	Type              string               `json:"type"`
	TypeSetup         db.QuestionTypeSetup `json:"typeSetup"`
	BallotProtocol    *db.BallotProtocol   `json:"ballotProtocol,omitempty"`
	SecretUntilTheEnd bool                 `json:"secretUntilTheEnd"`
	Eligibility       *EligibilitySpec     `json:"census,omitempty"`
	Metadata          map[string]any       `json:"metadata,omitempty"`
}

// CreateVotingProcessRequest is the body of POST /processes (also used by PUT to update a
// draft). Common params are shared by every question.
type CreateVotingProcessRequest struct {
	OrgAddress  internal.HexBytes              `json:"orgAddress" swaggertype:"string" format:"hex" example:"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"` //nolint:lll
	Census      CensusSpec                     `json:"census"`
	Title       db.MultiLangString             `json:"title"`
	Description db.MultiLangString             `json:"description,omitempty"`
	Header      string                         `json:"header,omitempty"`
	StreamURI   string                         `json:"streamUri,omitempty"`
	StartDate   string                         `json:"startDate,omitempty"`
	EndDate     string                         `json:"endDate,omitempty"`
	Questions   []VotingProcessQuestionRequest `json:"questions"`
	// UpdatedAt, on a PUT, is the updatedAt the client last read. The update then applies only if
	// the process has not been written since, and is rejected with 409 otherwise, so two clients
	// editing the same draft cannot silently overwrite each other. Optional: omitting it keeps the
	// unconditional last-writer-wins behaviour. Ignored on POST.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ValidateProcessCensusRequest is the body of POST /processes/census/validation: the same
// orgAddress + census block as a create request, checked for member-field duplicates / missing data
// before the process is created.
type ValidateProcessCensusRequest struct {
	OrgAddress internal.HexBytes `json:"orgAddress" swaggertype:"string" format:"hex" example:"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"` //nolint:lll
	Census     CensusSpec        `json:"census"`
}

// UpdateProcessCensusResponse is the result of PUT and DELETE /processes/{processId}/census: the
// number of members added to or removed from the census synchronously, plus the async job id that
// raises each published election's maxCensusSize on-chain (empty when no on-chain update was
// needed).
type UpdateProcessCensusResponse struct {
	JobID  string   `json:"jobId,omitempty"`
	Added  uint32   `json:"added"`
	Errors []string `json:"errors,omitempty"`
	// Removed is how many census participants the DELETE actually removed — an id naming nobody
	// does not count. Always present, like Added, so "none removed" is not indistinguishable from
	// a field the PUT simply never sets.
	Removed uint32 `json:"removed"`
}

// UpdateQuestionCensusRequest is the body of PUT /processes/{processId}/questions/{questionId}/census.
type UpdateQuestionCensusRequest struct {
	// MemberIDs is the complete desired eligibility list, not a delta, so the request is
	// idempotent: resend the whole list to change it.
	//
	// An empty list is not "nobody" — it is the encoding for "no restriction", which opens the
	// question to every member of the process census.
	MemberIDs []string `json:"memberIds"`
}

// UpdateQuestionCensusResponse is the result of PUT
// /processes/{processId}/questions/{questionId}/census.
type UpdateQuestionCensusResponse struct {
	// JobID is the async job raising the election's on-chain maxCensusSize, present only when the
	// question needed more room than it was published with (202).
	JobID string `json:"jobId,omitempty"`
	// Eligible is the size of the stored list. Zero means the question is open to the whole census.
	Eligible uint32 `json:"eligible"`
	Added    uint32 `json:"added"`
	Removed  uint32 `json:"removed"`
}

// CreateVotingProcessResponse is returned by POST /processes.
type CreateVotingProcessResponse struct {
	ProcessID string `json:"processId"`
}

// VotingProcessResponse is the full read shape of a voting process, used by the single-read
// and list endpoints. Questions are fully hydrated (including the synced status).
type VotingProcessResponse struct {
	ID          string                     `json:"id"`
	OrgAddress  internal.HexBytes          `json:"orgAddress" swaggertype:"string" format:"hex" example:"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"` //nolint:lll
	Published   bool                       `json:"published"`
	Census      CensusSpec                 `json:"census"`
	Title       db.MultiLangString         `json:"title"`
	Description db.MultiLangString         `json:"description,omitempty"`
	Header      string                     `json:"header,omitempty"`
	StreamURI   string                     `json:"streamUri,omitempty"`
	StartDate   string                     `json:"startDate,omitempty"`
	EndDate     string                     `json:"endDate,omitempty"`
	Questions   []db.VotingProcessQuestion `json:"questions"`
	// ChainID is the Vochain chain id votes must be signed against; clients need it because vote
	// signatures are chain-id-bound (a mismatch makes the on-chain signer recovery diverge).
	ChainID string `json:"chainId,omitempty"`
	// UpdatedAt is the process's last-write timestamp, in millisecond precision. Echo it back in a
	// PUT to make the update conditional on nothing else having written in between (see
	// CreateVotingProcessRequest.UpdatedAt).
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// VotingProcessListResponse is the paginated list of voting processes.
type VotingProcessListResponse struct {
	Processes  []VotingProcessResponse `json:"processes"`
	Pagination *Pagination             `json:"pagination"`
}

// VotingProcessQuestionResults carries one question's on-chain election tally, keyed by the
// question id. The embedded QuestionResults flattens voteCount/maxVoters/finalResults/results.
type VotingProcessQuestionResults struct {
	QuestionID string            `json:"questionId"`
	UpstreamID internal.HexBytes `json:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	db.QuestionResults
}

// VotingProcessResultsResponse is the multi-question results of a published voting process: one
// entry per published question, each carrying that question's QuestionResults tally.
type VotingProcessResultsResponse struct {
	ID        string                         `json:"id"`
	Questions []VotingProcessQuestionResults `json:"questions"`
}

// VotingProcessValidateResponse is the publish-readiness dry-run result.
type VotingProcessValidateResponse struct {
	Valid  bool     `json:"valid"`
	Errors []string `json:"errors"`
}

// ProcessParticipantQuestionVote is one question's voted status for a matched participant
// (hasVoted is true when the member has consumed that question's on-chain election).
type ProcessParticipantQuestionVote struct {
	QuestionID string            `json:"questionId"`
	UpstreamID internal.HexBytes `json:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	HasVoted   bool              `json:"hasVoted"`
}

// ProcessParticipantEntry is a matched org member that is also a participant of the process
// census, with its per-question voted status.
type ProcessParticipantEntry struct {
	MemberID     string                           `json:"memberId"`
	Name         string                           `json:"name,omitempty"`
	Surname      string                           `json:"surname,omitempty"`
	Email        string                           `json:"email,omitempty"`
	MemberNumber string                           `json:"memberNumber,omitempty"`
	Questions    []ProcessParticipantQuestionVote `json:"questions"`
}

// ProcessParticipantsResponse holds the members matching the lookup that are participants of
// the process census (empty when none match).
type ProcessParticipantsResponse struct {
	Participants []ProcessParticipantEntry `json:"participants"`
}

// SetQuestionsStatusRequest changes the on-chain status of many questions of a process to a
// single target status. An empty Questions list targets every published question.
type SetQuestionsStatusRequest struct {
	Status    string             `json:"status" example:"ENDED"`
	Questions []QuestionStatusID `json:"questions,omitempty"`
}

// QuestionStatusID identifies a target question by its id.
type QuestionStatusID struct {
	ID string `json:"id"`
}

// PublicQuestionResponse is the voter-facing single-question read: the voter-safe question
// fields plus the parent process's census config (the auth policy a voter must satisfy). It is
// an explicit allow-list, NOT the raw db.VotingProcessQuestion — the census member list and the
// per-question eligibility subset (member ids) are never exposed on this public endpoint.
type PublicQuestionResponse struct {
	ID                primitive.ObjectID   `json:"id"`
	ParentProcessID   primitive.ObjectID   `json:"parentProcessId"`
	Title             db.MultiLangString   `json:"title"`
	Description       db.MultiLangString   `json:"description,omitempty"`
	Choices           []db.Choice          `json:"choices"`
	Type              string               `json:"type"`
	TypeSetup         db.QuestionTypeSetup `json:"typeSetup"`
	BallotProtocol    *db.BallotProtocol   `json:"ballotProtocol,omitempty"`
	SecretUntilTheEnd bool                 `json:"secretUntilTheEnd"`
	Metadata          map[string]any       `json:"metadata,omitempty"`
	UpstreamID        internal.HexBytes    `json:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	Status            string               `json:"status,omitempty"`
	Census            CensusSpec           `json:"census"`
	// EncryptionKeys are the on-chain vote-encryption public keys (only for secretUntilTheEnd
	// questions). Because of omitempty the field is absent (not an empty array) until the keykeepers
	// publish the keys, so clients treat its absence as "not yet published" and poll. Voters seal
	// encrypted ballots with them.
	EncryptionKeys []db.EncryptionKey `json:"encryptionKeys,omitempty"`
	// Results is this question's live on-chain tally, present (non-null) for any published question and
	// carrying voteCount/maxVoters/finalResults; FinalResults marks live vs final. The inner per-choice
	// matrix (results.results) is omitted until a tally exists — empty while a secretUntilTheEnd election
	// is still encrypted or before any vote — so clients poll on an empty tally. The whole object is
	// absent (omitempty) only for a draft (no election yet).
	Results *db.QuestionResults `json:"results,omitempty"`
	// EligibleMemberIDs is the question's eligibility subset — who may vote it. Present only for a
	// Manager/Admin of the owning organization; an anonymous or non-manager read never carries it.
	// An empty/absent list on a manager read means the question is open to the whole census.
	EligibleMemberIDs []string `json:"eligibleMemberIds,omitempty"`
}

// PublicQuestionResponseFromDB builds the public question read from a question and its parent
// process's census (config only). It copies only the voter-safe fields (no eligibility member ids).
// EligibleMemberIDs is left unset: it names who may vote, so the caller adds it only for a
// Manager/Admin of the owning organization.
func PublicQuestionResponseFromDB(q *db.VotingProcessQuestion, census *db.Census) *PublicQuestionResponse {
	resp := &PublicQuestionResponse{
		ID:                q.ID,
		ParentProcessID:   q.ProcessID,
		Title:             q.Title,
		Description:       q.Description,
		Choices:           q.Choices,
		Type:              q.Type,
		TypeSetup:         q.TypeSetup,
		BallotProtocol:    q.BallotProtocol,
		SecretUntilTheEnd: q.SecretUntilTheEnd,
		Metadata:          q.Metadata,
		UpstreamID:        q.UpstreamID,
		Status:            q.Status,
		EncryptionKeys:    q.EncryptionKeys,
		Results:           q.Results,
	}
	if census != nil {
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
			AuthFields:  census.AuthFields,
			TwoFaFields: census.TwoFaFields,
		}
	}
	return resp
}

// VotingProcessResponseFromDB builds the read response from a process, its (hydrated)
// questions and its census. The census member list is never exposed — only its config.
func VotingProcessResponseFromDB(
	vp *db.VotingProcess, questions []db.VotingProcessQuestion, census *db.Census, chainID string,
) *VotingProcessResponse {
	resp := &VotingProcessResponse{
		ID:          vp.ID.Hex(),
		OrgAddress:  vp.OrgAddress.Bytes(),
		Published:   vp.Published,
		Title:       vp.Title,
		Description: vp.Description,
		Header:      vp.Header,
		StreamURI:   vp.StreamURI,
		Questions:   questions,
		ChainID:     chainID,
	}
	if !vp.StartDate.IsZero() {
		resp.StartDate = vp.StartDate.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !vp.EndDate.IsZero() {
		resp.EndDate = vp.EndDate.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !vp.UpdatedAt.IsZero() {
		// millisecond precision, because that is what mongo stores: a coarser format would never
		// match the stored value when echoed back as a conditional-update token
		resp.UpdatedAt = vp.UpdatedAt.UTC().Format(UpdatedAtLayout)
	}
	if census != nil {
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
			Anonymous:   census.Anonymous,
			AuthFields:  census.AuthFields,
			TwoFaFields: census.TwoFaFields,
			Size:        census.Size,
		}
		// guard the zero id so an organization-wide census reports no group at all: omitempty keys off
		// the empty string, but a zero ObjectID hexes to 24 zeros and would serialize as a real group.
		if !census.GroupID.IsZero() {
			resp.Census.GroupID = census.GroupID.Hex()
		}
	}
	return resp
}
