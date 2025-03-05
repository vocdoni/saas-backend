package twofactor

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	blind "github.com/arnaucube/go-blindsecp256k1"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/twofactor/authenticator"
	tfinternal "github.com/vocdoni/saas-backend/twofactor/internal"
	"go.vocdoni.io/dvote/log"
)

// Twofactor is the original service that handles two-factor authentication
// This is kept for backward compatibility
type Twofactor struct {
	auth tfinternal.Authenticator
}

// Constants for default values
const (
	// TFDefaultMaxAttempts defines the default maximum number of authentication attempts allowed
	TFDefaultMaxAttempts = 5

	// TFDefaultCooldownMinutes defines the default cool down time window for sending challenges in minutes
	TFDefaultCooldownMinutes = 2

	// TFDefaultThrottleMillis is the default throttle time for the notification provider API in milliseconds
	TFDefaultThrottleMillis = 500

	// TFDefaultMaxRetries is how many times to retry delivering a notification in case upstream provider returns an error
	TFDefaultMaxRetries = 10
)

// TwofactorConfig contains the configuration parameters for the two-factor authentication service
// This is kept for backward compatibility
type TwofactorConfig struct {
	NotificationServices struct {
		SMS  notifications.NotificationService
		Mail notifications.NotificationService
	}
	MaxAttempts  int
	CoolDownTime time.Duration
	ThrottleTime time.Duration
	MaxRetries   int
	PrivKey      string
	MongoURI     string
}

// New creates and initializes a new Twofactor service with the provided configuration
// This is kept for backward compatibility
func (tf *Twofactor) New(conf *TwofactorConfig) (*Twofactor, error) {
	if conf == nil {
		return nil, nil
	}
	if conf.NotificationServices.Mail == nil || conf.NotificationServices.SMS == nil {
		return nil, fmt.Errorf("no notification services defined")
	}

	maxAttempts := TFDefaultMaxAttempts
	if conf.MaxAttempts != 0 {
		maxAttempts = conf.MaxAttempts
	}

	cooldownPeriod := time.Duration(TFDefaultCooldownMinutes) * time.Minute
	if conf.CoolDownTime != 0 {
		cooldownPeriod = conf.CoolDownTime
	}

	throttlePeriod := time.Duration(TFDefaultThrottleMillis) * time.Millisecond
	if conf.ThrottleTime != 0 {
		throttlePeriod = conf.ThrottleTime
	}

	maxRetries := TFDefaultMaxRetries
	if conf.MaxRetries != 0 {
		maxRetries = conf.MaxRetries
	}

	// Create data directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot get user home directory: %w", err)
	}
	dataDir := path.Join(home, ".saas-backend")
	if err := os.MkdirAll(dataDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("cannot create data directory: %w", err)
	}

	// Create internal config
	internalCfg := &tfinternal.Config{
		Storage: &tfinternal.StorageConfig{
			DataDir:        path.Join(dataDir, "storage"),
			MaxAttempts:    maxAttempts,
			CooldownPeriod: cooldownPeriod,
			MongoURI:       conf.MongoURI,
		},
		Notification: &tfinternal.NotificationConfig{
			ThrottlePeriod: throttlePeriod,
			MaxRetries:     maxRetries,
			TTL:            cooldownPeriod,
		},
		Signer: &tfinternal.SignerConfig{
			PrivateKey: conf.PrivKey,
			KeysDir:    path.Join(dataDir, "keys"),
		},
	}
	internalCfg.NotificationServices.SMS = conf.NotificationServices.SMS
	internalCfg.NotificationServices.Mail = conf.NotificationServices.Mail

	// Create authenticator
	auth := authenticator.NewAuthenticator()
	if err := auth.Initialize(internalCfg); err != nil {
		return nil, fmt.Errorf("failed to initialize authenticator: %w", err)
	}

	return &Twofactor{
		auth: auth,
	}, nil
}

// Message is the JSON API body message used by the CSP and the client
// This is kept for backward compatibility
type Message struct {
	Error     string            `json:"error,omitempty"`
	TokenR    internal.HexBytes `json:"token,omitempty"`
	AuthToken *uuid.UUID        `json:"authToken,omitempty"`
	Payload   internal.HexBytes `json:"payload,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
	SharedKey internal.HexBytes `json:"sharedkey,omitempty"`
	Title     string            `json:"title,omitempty"`         // reserved for the info handler
	SignType  []string          `json:"signatureType,omitempty"` // reserver for the info handler
	AuthType  string            `json:"authType,omitempty"`      // reserved for the info handler
	AuthSteps []*AuthField      `json:"authSteps,omitempty"`     // reserved for the info handler
	AuthData  []string          `json:"authData,omitempty"`      // reserved for the auth handler
	Response  []string          `json:"response,omitempty"`      // reserved for the handlers
	Elections []Election        `json:"elections,omitempty"`     // reserved for the indexer handler
}

// Marshal serializes the message to JSON
func (m *Message) Marshal() []byte {
	r, err := json.Marshal(m)
	if err != nil {
		log.Warnf("error marshaling message: %v", err)
	}
	return r
}

// Unmarshal deserializes the message from JSON
func (m *Message) Unmarshal(data []byte) error {
	return json.Unmarshal(data, m)
}

// Election represents a process voting election which might be available for
// CSP signature or not (already used)
// This is kept for backward compatibility
type Election struct {
	ElectionID        internal.HexBytes `json:"electionId"`
	RemainingAttempts int               `json:"remainingAttempts"`
	Consumed          bool              `json:"consumed"`
	ExtraData         []string          `json:"extra"`
	Voted             internal.HexBytes `json:"voted,omitempty"`
}

// AuthField is the type used by the Info method for returning the description of the
// authentication steps for the CSP implementation
// This is kept for backward compatibility
type AuthField struct {
	Title string `json:"title"`
	Type  string `json:"type"`
}

// TFAuthResponse is the type returned by Auth methods on the AuthHandler interface
// This is kept for backward compatibility
type TFAuthResponse struct {
	Success   bool              // Either the authentication step is success or not
	Response  []string          // Response can be used by the handler to provide arbitrary data to the client
	AuthToken *uuid.UUID        // Only if there is a next step
	TokenR    internal.HexBytes // TokenR is the random token generated for the client
	Signature internal.HexBytes // Signature is the CSP signature
	Error     string            // Error is used to provide an error message to the client
}

// AuthResponse is an alias for TFAuthResponse for backward compatibility
// This is needed for API compatibility
type AuthResponse = TFAuthResponse

// String returns a string representation of the auth response
func (a *TFAuthResponse) String() string {
	if len(a.Response) == 0 {
		return ""
	}
	var buf strings.Builder
	for i, r := range a.Response {
		buf.WriteString(r)
		if i < len(a.Response)-1 {
			buf.WriteString("/")
		}
	}
	return buf.String()
}

// AddProcess adds a process or process bundle to the authenticator
// This is kept for backward compatibility
func (tf *Twofactor) AddProcess(
	pubCensusType db.CensusType,
	orgParticipants []db.CensusMembershipParticipant,
) error {
	return tf.auth.AddProcess(pubCensusType, orgParticipants)
}

// PubKeyBlind returns the public key for blind signatures
// This is kept for backward compatibility
func (tf *Twofactor) PubKeyBlind(processID []byte) string {
	pubKey, err := tf.auth.GetPublicKey(processID, tfinternal.SignatureTypeBlind)
	if err != nil {
		log.Warnf("failed to get blind public key: %v", err)
		return ""
	}
	return pubKey
}

// PubKeyECDSA returns the public key for ECDSA signatures
// This is kept for backward compatibility
func (tf *Twofactor) PubKeyECDSA(processID []byte) string {
	pubKey, err := tf.auth.GetPublicKey(processID, tfinternal.SignatureTypeECDSA)
	if err != nil {
		log.Warnf("failed to get ECDSA public key: %v", err)
		return ""
	}
	return pubKey
}

// NewBlindRequestKey generates a new request key for blinding content on the client side
// This is kept for backward compatibility
func (tf *Twofactor) NewBlindRequestKey() (*blind.Point, error) {
	// This is a placeholder. In the real implementation, we would need to
	// generate a new blind request key and store it for later use.
	return nil, fmt.Errorf("not implemented")
}

// NewRequestKey generates a new request key for authentication on the client side
// This is kept for backward compatibility
func (tf *Twofactor) NewRequestKey() []byte {
	// This is a placeholder. In the real implementation, we would need to
	// generate a new request key and store it for later use.
	return nil
}

// SignECDSA signs a message using ECDSA
// This is kept for backward compatibility
func (tf *Twofactor) SignECDSA(token, msg []byte, processID []byte) ([]byte, error) {
	// Convert token to AuthToken
	tokenUUID, err := uuid.FromBytes(token)
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	authToken := tfinternal.FromUUID(tokenUUID)

	// Convert processID to ElectionID
	var electionID tfinternal.ElectionID
	copy(electionID, processID)

	// Sign the message
	resp, err := tf.auth.Sign(authToken, msg, electionID, "", tfinternal.SignatureTypeECDSA)
	if err != nil {
		return nil, err
	}

	if !resp.Success {
		return nil, fmt.Errorf(resp.Error)
	}

	return resp.Signature, nil
}

// SignBlind signs a blinded message
// This is kept for backward compatibility
func (tf *Twofactor) SignBlind(signerR *blind.Point, hash, processID []byte) ([]byte, error) {
	// This is a placeholder. In the real implementation, we would need to
	// retrieve the secret key for the signerR and use it to sign the hash.
	return nil, fmt.Errorf("not implemented")
}

// SharedKey generates a shared key for a process
// This is kept for backward compatibility
func (tf *Twofactor) SharedKey(processID []byte) ([]byte, error) {
	return tf.auth.GenerateSharedKey(processID)
}

// InitiateAuth initiates the authentication process
// This is kept for backward compatibility
func (tf *Twofactor) InitiateAuth(
	bundleID, participantID, contact string,
	notifType notifications.NotificationType,
) TFAuthResponse {
	resp, err := tf.auth.StartAuthentication(bundleID, participantID, contact, notifType)
	if err != nil {
		return TFAuthResponse{
			Error: err.Error(),
		}
	}

	// Convert internal.AuthToken to uuid.UUID
	var authToken *uuid.UUID
	if resp.Token != nil {
		uuidVal := resp.Token.ToUUID()
		authToken = &uuidVal
	}

	return TFAuthResponse{
		Success:   resp.Success,
		Response:  resp.Message,
		AuthToken: authToken,
		Error:     resp.Error,
	}
}

// Auth verifies the authentication challenge
// This is kept for backward compatibility
func (tf *Twofactor) Auth(bundleID string, authToken *uuid.UUID, authData []string) TFAuthResponse {
	if authToken == nil || len(authData) != 1 {
		return TFAuthResponse{
			Error: "invalid auth token or auth data",
		}
	}

	// Convert bundleID to ElectionID
	var electionID tfinternal.ElectionID
	if err := electionID.FromString(bundleID); err != nil {
		return TFAuthResponse{
			Error: fmt.Sprintf("invalid bundle ID: %v", err),
		}
	}

	// Convert authToken to internal.AuthToken
	token := tfinternal.FromUUID(*authToken)

	// Verify the challenge
	resp, err := tf.auth.VerifyChallenge(electionID, token, authData[0])
	if err != nil {
		return TFAuthResponse{
			Error: err.Error(),
		}
	}

	// Convert internal.AuthToken to uuid.UUID
	var respAuthToken *uuid.UUID
	if resp.Token != nil {
		uuidVal := resp.Token.ToUUID()
		respAuthToken = &uuidVal
	}

	return TFAuthResponse{
		Success:   resp.Success,
		Response:  resp.Message,
		AuthToken: respAuthToken,
		Error:     resp.Error,
	}
}

// Indexer takes a unique user identifier and returns the list of processIDs where
// the user is eligible for participation
// This is kept for backward compatibility
func (tf *Twofactor) Indexer(participantId, bundleId, electionId string) []Election {
	if len(participantId) == 0 {
		log.Warnw("no participant ID provided")
		return nil
	}

	// Create userID either based on bundleId or electionId
	var userID tfinternal.UserID
	var electionID tfinternal.ElectionID
	switch {
	case len(bundleId) != 0:
		if err := electionID.FromString(bundleId); err != nil {
			return nil
		}
		userID = tfinternal.BuildUserID(participantId, electionID)
	case len(electionId) != 0:
		if err := electionID.FromString(electionId); err != nil {
			return nil
		}
		userID = tfinternal.BuildUserID(participantId, electionID)
	default:
		return nil
	}

	// Get user data
	user, err := tf.auth.GetUser(userID)
	if err != nil {
		log.Warnw("cannot get indexer elections", "error", err)
		return nil
	}

	// Get the last two digits of the contact and return them as extraData
	contact := user.Email
	if contact == "" {
		contact = user.Phone
	}
	var contactHint string
	if contact != "" {
		if len(contact) < 3 {
			contactHint = contact
		} else {
			contactHint = contact[len(contact)-2:]
		}
	}

	// Convert internal elections to the old format
	var elections []Election
	for _, e := range user.Elections {
		election := Election{
			ElectionID:        internal.HexBytes(e.ID),
			RemainingAttempts: e.RemainingAttempts,
			Consumed:          e.Verified,
			ExtraData:         []string{user.ExtraData, contactHint},
			Voted:             internal.HexBytes(e.VotedWith),
		}
		elections = append(elections, election)
	}

	return elections
}

// Sign handles signing operations for different signature types
// This is kept for backward compatibility
func (tf *Twofactor) Sign(
	authToken uuid.UUID,
	token, msg, electionID internal.HexBytes,
	bundleId, sigType string,
) TFAuthResponse {
	switch sigType {
	case "blind":
		// For blind signatures, the token is expected to be a blind point
		blindPoint, err := blind.NewPointFromBytesUncompressed(token)
		if err != nil {
			return TFAuthResponse{
				Success: false,
				Error:   fmt.Sprintf("invalid blind point: %v", err),
			}
		}

		// Sign the blinded message
		signature, err := tf.SignBlind(blindPoint, msg, electionID)
		if err != nil {
			return TFAuthResponse{
				Success: false,
				Error:   fmt.Sprintf("signing error: %v", err),
			}
		}

		return TFAuthResponse{
			Success:   true,
			Signature: signature,
		}

	case "ecdsa":
		// Convert authToken to internal.AuthToken
		token := tfinternal.FromUUID(authToken)

		// Convert electionID to internal.ElectionID
		var electionIDBytes tfinternal.ElectionID
		copy(electionIDBytes, electionID)

		// Sign the message
		resp, err := tf.auth.Sign(token, msg, electionIDBytes, bundleId, tfinternal.SignatureTypeECDSA)
		if err != nil {
			return TFAuthResponse{
				Success: false,
				Error:   fmt.Sprintf("signing error: %v", err),
			}
		}

		return TFAuthResponse{
			Success:   resp.Success,
			Signature: resp.Signature,
			Error:     resp.Error,
		}

	case "sharedkey":
		// Generate shared key
		sharedKey, err := tf.SharedKey(electionID)
		if err != nil {
			return TFAuthResponse{
				Success: false,
				Error:   fmt.Sprintf("failed to generate shared key: %v", err),
			}
		}

		return TFAuthResponse{
			Success:   true,
			Signature: sharedKey,
		}

	default:
		return TFAuthResponse{
			Success: false,
			Error:   "invalid signature type",
		}
	}
}
