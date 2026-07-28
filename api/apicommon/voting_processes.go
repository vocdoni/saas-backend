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
	Weighted    bool                    `json:"weighted"`
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
}

// ValidateProcessCensusRequest is the body of POST /processes/census/validation: the same
// orgAddress + census block as a create request, checked for member-field duplicates / missing data
// before the process is created.
type ValidateProcessCensusRequest struct {
	OrgAddress internal.HexBytes `json:"orgAddress" swaggertype:"string" format:"hex" example:"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"` //nolint:lll
	Census     CensusSpec        `json:"census"`
}

// UpdateProcessCensusResponse is the result of PUT /processes/{processId}/census: the number of
// members added to the census synchronously, plus the async job id that raises each published
// election's maxCensusSize on-chain (empty when no on-chain update was needed).
type UpdateProcessCensusResponse struct {
	JobID  string   `json:"jobId,omitempty"`
	Added  uint32   `json:"added"`
	Errors []string `json:"errors,omitempty"`
}

// UpdateQuestionCensusRequest is the body of PUT /processes/{processId}/questions/{questionId}/census:
// the complete list of members eligible to vote that question, not a delta. An empty list means every
// census member is eligible. Sending the list a question already has is a no-op rather than an error.
//
// While the process is a draft the list is applied as given. Once it is published members may still
// be added and removed, with one restriction: a member the CSP has already signed for on the
// question cannot lose eligibility, and a request that would drop one is refused with 409. Note that
// is "signed for", not "voted on chain" — they hold a valid signature either way.
type UpdateQuestionCensusRequest struct {
	// Member ids eligible for this question; each must be a participant of the process census
	MemberIDs []string `json:"memberIds"`
}

// UpdateQuestionCensusResponse is the result of PUT /processes/{processId}/questions/{questionId}/census:
// how the eligible list changed, plus the async job that raises the question's on-chain maxCensusSize
// when it grew (empty for a draft, or when the list did not change).
type UpdateQuestionCensusResponse struct {
	JobID string `json:"jobId,omitempty"`
	// Eligible is the length of the stored eligible list after the update, so zero means the
	// question is open to the WHOLE census rather than to nobody — an empty list is "no
	// restriction", the same convention the request body uses.
	Eligible int `json:"eligible"`
	// Added and Removed count the members whose eligibility changed with this request. Both are
	// always present: omitting a zero would leave a client unable to tell "removed nobody" from
	// "this response does not report removals".
	Added   int `json:"added"`
	Removed int `json:"removed"`
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

// PublicQuestionResponse is the voter-facing single-question read. It is an explicit allow-list,
// NOT the raw db.VotingProcessQuestion. The parent process's census config is not repeated here —
// read it from GET /processes/{parentProcessId} — and EligibleMemberIDs, which names the members
// allowed to vote this question, is filled in only for a manager of the owning organization; an
// anonymous voter never sees who the electorate is.
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
	// EligibleMemberIDs are the members allowed to vote this question; absent when the question is
	// open to the whole census, and always absent for a caller who is not a manager of the org.
	EligibleMemberIDs []string `json:"eligibleMemberIds,omitempty"`
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
}

// PublicQuestionResponseFromDB builds the public question read from a question. It copies only the
// voter-safe fields and never the eligible member ids: a caller entitled to those adds them
// afterwards (see WithEligibility), so the default stays safe for the voter-facing route.
func PublicQuestionResponseFromDB(q *db.VotingProcessQuestion) *PublicQuestionResponse {
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
	return resp
}

// WithEligibility discloses which members may vote the question. Reserved for a manager of the
// owning organization — a voter is told the question, not the electorate.
func (r *PublicQuestionResponse) WithEligibility(q *db.VotingProcessQuestion) *PublicQuestionResponse {
	r.EligibleMemberIDs = q.EligibleMemberIDs
	return r
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
	if census != nil {
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
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
