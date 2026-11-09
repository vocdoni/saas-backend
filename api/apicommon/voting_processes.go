package apicommon

//revive:disable:max-public-structs

import (
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// CensusSpec is the inline census definition of a voting process. The census type is
// inferred from the auth/2FA fields; there is no prebuilt-by-id reference over the API.
type CensusSpec struct {
	Weighted bool `json:"weighted"`
	// Anonymous selects blind-CSP voting: the process publishes with census origin OFF_CHAIN_CA_V2
	// and the CSP blind-signs each ballot so it cannot link a signature to the voter. Orthogonal to
	// the mail/sms/auth 2FA channel; may be combined with Weighted (V2 binds the weight into the salt).
	Anonymous bool `json:"anonymous,omitempty"`
	// AuthFields are the member fields a voter logs in with. "password" is checked against the
	// member's stored password after the other fields find the participant, so it needs at least
	// one other auth field or a 2FA field.
	AuthFields  db.OrgMemberAuthFields  `json:"authFields,omitempty"`
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
	// GroupID is the org member group the census was built from. Round-trips: it is echoed back on
	// process reads, so a client can restore the group a draft targeted. Absent (not a zero id) when
	// the census is organization-wide.
	GroupID string `json:"groupId,omitempty"`
	// MemberIDs selects the census members explicitly, as an alternative to GroupID. Write-only on
	// this type: a process read echoes the census config, not the member list it resolved to.
	MemberIDs []string `json:"memberIds,omitempty"`
	// Size is the number of members in the census. Response-only (ignored on create/update): for a
	// published process it equals the on-chain maxCensusSize of its whole-census questions.
	Size int64 `json:"size,omitempty"`
	// TotalWeight is the whole-census total voting weight (sum of members' weights). Response-only;
	// equals Size for a non-weighted census. Needed by clients (e.g. the results report) to turn
	// per-answer weights into percentages.
	TotalWeight int64 `json:"totalWeight,omitempty"`
	// Attested makes the census an attested one: an integrator vouches for each voter with an EIP-712
	// attestation instead of a member list. It excludes groupId, memberIds, authFields, weighted,
	// anonymous and question eligibility, and allows twoFaFields [], ["email"] or ["phone"].
	Attested *AttestedCensusSpec `json:"attested,omitempty"`
}

// AttestedCensusSpec configures an attested census.
type AttestedCensusSpec struct {
	// Signer is the address whose attestations the CSP accepts.
	Signer common.Address `json:"signer" swaggertype:"string" example:"0x71C7656EC7ab88b098defB751B7401B5f6d8976F"`
	// MaxCensusSize is the declared maximum number of voters; it prices and sizes the process.
	MaxCensusSize int64 `json:"maxCensusSize"`
	// SignerHistory lists every signer with the time it became active. Response-only.
	SignerHistory []db.AttestationSigner `json:"signerHistory,omitempty"`
	// VerifyingContract is the verifyingContract of the attestation EIP-712 domain: the address of
	// the CSP signer of this environment. Response-only.
	VerifyingContract *common.Address `json:"verifyingContract,omitempty" swaggertype:"string"`
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
	// InitialStatus is the on-chain status every question's election is published with.
	// Empty (default) means READY; "PAUSED" publishes them PAUSED so voting only opens after
	// an admin sets each question to READY. Only "" / "READY" / "PAUSED" are accepted (the
	// vochain whitelist for NewProcess); anything else is rejected with 400. Persisted on
	// the draft and applied at publish time.
	InitialStatus string `json:"initialStatus,omitempty" example:"PAUSED"`
	// AddOns are the paid per-process options (signed results certificate, custom URL,
	// branding). The 2FA add-ons are not here: they derive from census.twoFaFields.
	// They change the process price, so editing them invalidates an open checkout session.
	AddOns db.ProcessAddOns `json:"addOns,omitzero"`
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

// ProcessAttestationsResponse is the public audit of an attested census: the EIP-712 domain, the
// signer history and a page of attestation records.
type ProcessAttestationsResponse struct {
	Domain        AttestationDomainInfo    `json:"domain"`
	SignerHistory []db.AttestationSigner   `json:"signerHistory"`
	MaxCensusSize int64                    `json:"maxCensusSize"`
	Attestations  []AttestationRecordEntry `json:"attestations"`
	Pagination    *Pagination              `json:"pagination"`
}

// AttestationDomainInfo is the EIP-712 domain every attestation of the process is signed under.
type AttestationDomainInfo struct {
	Name              string         `json:"name"`
	Version           string         `json:"version"`
	VerifyingContract common.Address `json:"verifyingContract" swaggertype:"string"`
}

// AttestationRecordEntry is the attestation the CSP signed an attested voter's ballots against, with
// the questions it signed for them. ContactHash is absent for a census without 2FA.
type AttestationRecordEntry struct {
	VoterID     internal.HexBytes  `json:"voterId" swaggertype:"string" format:"hex"`
	ContactHash internal.HexBytes  `json:"contactHash,omitempty" swaggertype:"string" format:"hex"`
	ExpiresAt   int64              `json:"expiresAt"`
	Signature   internal.HexBytes  `json:"signature" swaggertype:"string" format:"hex"`
	Signer      common.Address     `json:"signer" swaggertype:"string"`
	Questions   []AttestedQuestion `json:"questions"`
}

// AttestedQuestion is a question the CSP signed an attested voter's ballot for.
type AttestedQuestion struct {
	QuestionID string            `json:"questionId"`
	UpstreamID internal.HexBytes `json:"upstreamId" swaggertype:"string" format:"hex"`
}

// AttestedCensusSizeRequest is the body of PUT /processes/{processId}/census/size.
type AttestedCensusSizeRequest struct {
	Size int64 `json:"size"`
}

// AttestedCensusSizeResponse answers PUT /processes/{processId}/census/size: the stored size and,
// when open questions are resized on chain, the job doing it.
type AttestedCensusSizeResponse struct {
	Size  int64  `json:"size"`
	JobID string `json:"jobId,omitempty"`
}

// AttestationSignerRequest is the body of PUT /processes/{processId}/attestation-signer.
type AttestationSignerRequest struct {
	Signer common.Address `json:"signer" swaggertype:"string" example:"0x71C7656EC7ab88b098defB751B7401B5f6d8976F"`
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

// CreateVotingProcessResponse is returned by POST /processes and PUT /processes/{processId}.
type CreateVotingProcessResponse struct {
	ProcessID string `json:"processId"`
	// MissingData lists the members left out of the census for missing the auth data it requires.
	MissingData []string `json:"missingData,omitempty"`
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
	// InitialStatus echoes the draft's initialStatus: the on-chain status every question's
	// election was (or will be) published with. Empty/absent means READY (the default);
	// "PAUSED" means every question was published PAUSED. Publish-time only — it does not
	// track later per-question status changes.
	InitialStatus string `json:"initialStatus,omitempty" example:"PAUSED"`
	// ChainID is the Vochain chain id votes must be signed against; clients need it because vote
	// signatures are chain-id-bound (a mismatch makes the on-chain signer recovery diverge).
	ChainID string `json:"chainId,omitempty"`
	// UpdatedAt is the process's last-write timestamp, in millisecond precision. Echo it back in a
	// PUT to make the update conditional on nothing else having written in between (see
	// CreateVotingProcessRequest.UpdatedAt).
	UpdatedAt string `json:"updatedAt,omitempty"`
	// Legacy marks a process projected read-only from the deprecated /process generation or from a
	// process bundle: not editable through /processes, and its questions may share one upstreamId.
	Legacy bool `json:"legacy,omitempty"`
	// EndedAt is the UTC timestamp at which the process was ended early: the latest question endedAt,
	// present only when every published question has one (i.e. was stopped before its scheduled end).
	// Formatted as "2006-01-02T15:04:05Z". Absent when the process ran to its scheduled end, or while
	// any published question is still open or has no recorded endedAt yet.
	EndedAt string `json:"endedAt,omitempty"`
	// AddOns echoes the paid per-process options selected on the draft.
	AddOns db.ProcessAddOns `json:"addOns,omitzero"`
}

// ProcessPriceResponse is the server-side quote of a voting process, in EUR cents, VAT
// excluded (Stripe Tax adds VAT at checkout).
type ProcessPriceResponse struct {
	Lines      []pricing.QuoteLine `json:"lines"`
	TotalCents pricing.Cents       `json:"totalCents"`
	Currency   string              `json:"currency"`
	// QuoteRecommended: census above 15 000 voters — suggest requesting a custom quote.
	QuoteRecommended bool `json:"quoteRecommended"`
	// QuoteRequired: census above 50 000 voters — quote-only: no price (no lines, totalCents 0,
	// which is not free) and no self-service payment.
	QuoteRequired bool `json:"quoteRequired"`
	// PaymentStatus is the process's current payment state (pending, failed, paid, refunded);
	// empty when no payment exists yet.
	PaymentStatus db.ProcessPaymentStatus `json:"paymentStatus,omitempty"`
	// AddOnsHeldBy names, per once-per-organization add-on the draft selects (branding,
	// customUrl), the other process of the organization whose payment holds it. It is priced in,
	// and publish and checkout answer 409 (40177) until that payment completes or is cancelled.
	AddOnsHeldBy map[string]string `json:"addOnsHeldBy,omitempty"`
}

// ProcessCheckoutRequest starts (or resumes) the wallet top-up checkout of a voting process.
type ProcessCheckoutRequest struct {
	// ReturnURL is where Stripe redirects after checkout; the session id is appended.
	ReturnURL string `json:"returnURL"`
	Locale    string `json:"locale,omitempty"`
}

// ProcessCheckoutResponse carries the embedded checkout credentials of a wallet top-up.
// AmountCents is the net amount the session charges (or, for census headroom paid from the
// wallet with no checkout, what was debited).
type ProcessCheckoutResponse struct {
	ClientSecret string        `json:"clientSecret"`
	SessionID    string        `json:"sessionId"`
	AmountCents  pricing.Cents `json:"amountCents"`
	Currency     string        `json:"currency"`
}

// ProcessCensusGrowthQuote is the 402 payload of a census that would grow a published
// process past the price it was paid for: the projected quote at the requested size, what is
// already covered, the difference the organization has to buy, and how much of it the payer's
// wallet lacks. Raise the envelope with POST /processes/{processId}/census/checkout, then retry
// the request that was refused.
type ProcessCensusGrowthQuote struct {
	// ProcessID is the process whose envelope is exceeded. A memberbase change can reach the
	// census of several processes, so it names the one to buy headroom for.
	ProcessID  string              `json:"processId"`
	Lines      []pricing.QuoteLine `json:"lines"`
	TotalCents pricing.Cents       `json:"totalCents"`
	// PaidCents is the envelope already bought, DueCents the difference to TotalCents.
	PaidCents pricing.Cents `json:"paidCents"`
	DueCents  pricing.Cents `json:"dueCents"`
	// CensusSize is the projected size the quote was computed for.
	CensusSize int64  `json:"censusSize"`
	Currency   string `json:"currency"`
	// BalanceCents is the payer's wallet balance, ShortfallCents what DueCents exceeds it by.
	BalanceCents   pricing.Cents `json:"balanceCents"`
	ShortfallCents pricing.Cents `json:"shortfallCents"`
}

// ProcessPaymentRequired is the 402 (40178) payload of a publish the organization's wallet does
// not cover: the quote, the balance, and the shortfall POST /processes/{processId}/checkout tops
// up (charged at least the minimum).
type ProcessPaymentRequired struct {
	pricing.Quote
	BalanceCents   pricing.Cents `json:"balanceCents"`
	ShortfallCents pricing.Cents `json:"shortfallCents"`
	// PaidCents and CensusSize are set for a process that already paid part of the price (its
	// census grew since): it buys the difference with POST /processes/{processId}/census/checkout
	// for CensusSize, not with a process checkout.
	PaidCents  pricing.Cents `json:"paidCents,omitempty"`
	CensusSize int64         `json:"censusSize,omitempty"`
}

// ProcessCensusCheckoutRequest buys census headroom for a paid process: the difference between
// what the process paid and what CensusSize would cost, debited from the payer's wallet (topped
// up through a checkout first when it falls short).
type ProcessCensusCheckoutRequest struct {
	// CensusSize is the census size to buy room for; it must exceed what is already paid.
	CensusSize int64 `json:"censusSize"`
	// ReturnURL is where Stripe redirects after checkout; the session id is appended.
	ReturnURL string `json:"returnURL"`
	Locale    string `json:"locale,omitempty"`
}

// WalletResponse is an organization's prepaid wallet: EUR balance plus its paged history,
// newest first.
type WalletResponse struct {
	BalanceCents pricing.Cents          `json:"balanceCents"`
	Currency     string                 `json:"currency"`
	History      []db.WalletLedgerEntry `json:"history"`
	Pagination   *Pagination            `json:"pagination"`
}

// WalletTopUpRequest starts a one-time checkout that credits the organization's wallet once
// the payment is webhook-verified. AmountCents is EUR cents, VAT excluded.
type WalletTopUpRequest struct {
	AmountCents pricing.Cents `json:"amountCents"`
	ReturnURL   string        `json:"returnURL"`
	Locale      string        `json:"locale,omitempty"`
}

// ProcessPaymentStatusResponse reports the payment state of a voting process, combining
// the stored payment with the live checkout session state when one is open.
type ProcessPaymentStatusResponse struct {
	Status      db.ProcessPaymentStatus `json:"status"`
	AmountCents pricing.Cents           `json:"amountCents"`
	Currency    string                  `json:"currency"`
	PaidAt      string                  `json:"paidAt,omitempty"`
	// SessionStatus is the live Stripe session state (open, complete, expired) of the top-up
	// checkout opened for the process, when there is one.
	SessionStatus string `json:"sessionStatus,omitempty"`
	// SessionPaymentStatus is the live Stripe payment state of that session (paid,
	// unpaid, no_payment_required).
	SessionPaymentStatus string `json:"sessionPaymentStatus,omitempty"`
	// Consumed: the top-up was credited to the wallet and the publish it was opened for has not
	// been recorded yet (the webhook runs it).
	Consumed bool `json:"consumed"`
	// Refusal is why the webhook did not publish the process once its top-up was credited; the
	// credit stays in the wallet and the process is published by hand.
	Refusal *db.TopUpRefusal `json:"refusal,omitempty"`
	// JobID is the latest publish job of the process, the top-up webhook's included: poll
	// GET /jobs/{jobId}, whose errors say why a failed publish failed.
	JobID string `json:"jobId,omitempty"`
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

// ProcessParticipantQuestionVote is one question's vote status for a matched participant.
// HasSigned is true once the CSP signed the member's ballot for the question (the vote may not
// be on chain); HasVoted is true only when the vote is on chain, and is always false in an
// anonymous (blind) census. HasVotedUnknown is set when the chain could not be asked about this
// member's vote: HasVoted is then false without meaning "not voted".
type ProcessParticipantQuestionVote struct {
	QuestionID      string            `json:"questionId"`
	UpstreamID      internal.HexBytes `json:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
	HasSigned       bool              `json:"hasSigned"`
	HasVoted        bool              `json:"hasVoted"`
	HasVotedUnknown bool              `json:"hasVotedUnknown,omitempty"`
}

// ProcessParticipantEntry is a matched org member that is also a participant of the process
// census, with its per-question voted status.
type ProcessParticipantEntry struct {
	MemberID string `json:"memberId"`
	// VoterID is the attested voter an attested census looks up, which has no member behind it.
	VoterID      internal.HexBytes                `json:"voterId,omitempty" swaggertype:"string" format:"hex"`
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

// SetProcessEndDateRequest moves the end date of a published process later. EndDate is RFC 3339;
// it is truncated to whole seconds.
type SetProcessEndDateRequest struct {
	EndDate string `json:"endDate" example:"2026-12-31T23:59:59Z"`
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
	ID                bson.ObjectID        `json:"id"`
	ParentProcessID   bson.ObjectID        `json:"parentProcessId"`
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
	// EndedAt is the UTC timestamp at which this question's election was ended early, present only
	// when the vote was manually ended before its scheduled end. Formatted as "2006-01-02T15:04:05Z".
	// Absent when the question ran to its scheduled end.
	EndedAt string `json:"endedAt,omitempty"`
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
	if !q.EndedAt.IsZero() {
		resp.EndedAt = q.EndedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if census != nil {
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
			Anonymous:   census.Anonymous,
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
		ID:            vp.ID.Hex(),
		OrgAddress:    vp.OrgAddress.Bytes(),
		Published:     vp.Published,
		Title:         vp.Title,
		Description:   vp.Description,
		Header:        vp.Header,
		StreamURI:     vp.StreamURI,
		Questions:     questions,
		InitialStatus: vp.InitialStatus,
		AddOns:        vp.AddOns,
		ChainID:       chainID,
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
	// process-level endedAt: the latest question EndedAt, but only when every published question
	// (with a non-empty UpstreamID) has a non-zero EndedAt — so a partially-ended process does
	// not get a process-level endedAt.
	if latestEndedAt := processEndedAt(questions); !latestEndedAt.IsZero() {
		resp.EndedAt = latestEndedAt.UTC().Format("2006-01-02T15:04:05Z")
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
		if census.Attested != nil {
			resp.Census.Attested = &AttestedCensusSpec{
				Signer:        census.Attested.Signer,
				MaxCensusSize: census.Size,
				SignerHistory: census.Attested.SignerHistory,
			}
		}
	}
	return resp
}

// processEndedAt returns the latest question EndedAt across all published questions of a process,
// but only when every published question (non-empty UpstreamID) has a non-zero EndedAt. Returns a
// zero time when the process has no published questions, or when any published question has not yet
// recorded its end time (i.e. not all questions were ended early).
func processEndedAt(questions []db.VotingProcessQuestion) time.Time {
	var latest time.Time
	hasPublished := false
	for _, q := range questions {
		if len(q.UpstreamID) == 0 {
			continue // unpublished draft question
		}
		hasPublished = true
		if q.EndedAt.IsZero() {
			return time.Time{} // at least one published question has no endedAt
		}
		if q.EndedAt.After(latest) {
			latest = q.EndedAt
		}
	}
	if !hasPublished {
		return time.Time{}
	}
	return latest
}
