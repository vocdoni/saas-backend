package authenticator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/twofactor/internal"
	"github.com/vocdoni/saas-backend/twofactor/storage"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/log"
)

// DefaultAuthenticator implements the Authenticator interface
type DefaultAuthenticator struct {
	storage              internal.Storage
	notificationQueues   map[notifications.NotificationType]*NotificationQueue
	notificationServices struct {
		SMS  notifications.NotificationService
		Mail notifications.NotificationService
	}
	signer         internal.Signer
	mutex          sync.RWMutex
	maxAttempts    int
	cooldownPeriod time.Duration
	otpSalt        string
}

// NewAuthenticator creates a new DefaultAuthenticator
func NewAuthenticator() *DefaultAuthenticator {
	return &DefaultAuthenticator{
		notificationQueues: make(map[notifications.NotificationType]*NotificationQueue),
	}
}

// Initialize initializes the authenticator
func (a *DefaultAuthenticator) Initialize(config *internal.Config) error {
	a.mutex.Lock()
	defer a.mutex.Unlock()

	// Initialize storage
	if config.Storage.MongoURI != "" {
		a.storage = storage.NewMongoDBStorage()
	} else {
		a.storage = storage.NewJSONStorage()
	}

	if err := a.storage.Initialize(
		config.Storage.DataDir,
		config.Storage.MaxAttempts,
		config.Storage.CooldownPeriod,
	); err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Initialize notification services
	a.notificationServices.SMS = config.NotificationServices.SMS
	a.notificationServices.Mail = config.NotificationServices.Mail

	// Initialize notification queues
	a.notificationQueues[notifications.SMS] = NewNotificationQueue(
		config.Notification.TTL,
		config.Notification.ThrottlePeriod,
		config.Notification.MaxRetries,
		[]internal.SendChallengeFunc{a.sendSMSChallenge},
		a.storage.UpdateAttempts,
	)
	a.notificationQueues[notifications.Email] = NewNotificationQueue(
		config.Notification.TTL,
		config.Notification.ThrottlePeriod,
		config.Notification.MaxRetries,
		[]internal.SendChallengeFunc{a.sendEmailChallenge},
		a.storage.UpdateAttempts,
	)

	// Start notification queues
	for _, queue := range a.notificationQueues {
		queue.Start()
	}

	// Initialize signer
	var err error
	a.signer, err = NewSigner(config.Signer.PrivateKey, config.Signer.KeysDir)
	if err != nil {
		return fmt.Errorf("failed to initialize signer: %w", err)
	}

	a.maxAttempts = config.Storage.MaxAttempts
	a.cooldownPeriod = config.Storage.CooldownPeriod
	a.otpSalt = gotp.RandomSecret(8)

	return nil
}

// AddProcess adds a process or process bundle to the authenticator
func (a *DefaultAuthenticator) AddProcess(censusType db.CensusType, participants []db.CensusMembershipParticipant) error {
	for i, participant := range participants {
		var bundleID internal.ElectionID
		if err := bundleID.FromString(participant.BundleId); err != nil {
			log.Warnw("invalid bundleId format", "line", i, "bundleId", participant.BundleId)
			continue
		}

		var userID internal.UserID
		if len(participant.BundleId) == 0 {
			userID = internal.BuildUserID(participant.ParticipantNo, participant.ElectionIds[0])
		} else {
			userID = internal.BuildUserID(participant.ParticipantNo, bundleID)
		}

		// Convert election IDs
		var electionIDs []internal.ElectionID
		for _, id := range participant.ElectionIds {
			var electionID internal.ElectionID
			if err := electionID.FromString(id.String()); err != nil {
				log.Warnw("invalid electionId format", "line", i, "electionId", id.String())
				continue
			}
			electionIDs = append(electionIDs, electionID)
		}

		// Add bundle ID to election IDs if not already present
		if len(participant.BundleId) > 0 {
			var found bool
			for _, id := range electionIDs {
				if id.String() == bundleID.String() {
					found = true
					break
				}
			}
			if !found {
				electionIDs = append(electionIDs, bundleID)
			}
		}

		if err := a.storage.AddUser(userID, electionIDs, participant.HashedEmail, participant.HashedPhone, ""); err != nil {
			log.Warnw("cannot add user", "line", i, "error", err)
		}
		log.Debugw("user added to process", "userID", userID.String(), "electionIDs", formatElectionIDs(electionIDs))
	}
	return nil
}

// formatElectionIDs formats a slice of election IDs for logging
func formatElectionIDs(ids []internal.ElectionID) string {
	if len(ids) == 0 {
		return "[]"
	}

	result := "["
	for i, id := range ids {
		if i > 0 {
			result += ", "
		}
		result += id.String()
	}
	result += "]"
	return result
}

// StartAuthentication initiates the authentication process
func (a *DefaultAuthenticator) StartAuthentication(
	bundleID, participantID, contact string,
	notificationType notifications.NotificationType,
) (*internal.AuthResponse, error) {
	// Validate input
	if len(participantID) == 0 || len(bundleID) == 0 {
		return &internal.AuthResponse{Error: "incorrect auth data fields"}, nil
	}

	var bundleIDBytes internal.ElectionID
	if err := bundleIDBytes.FromString(bundleID); err != nil {
		return &internal.AuthResponse{Error: "invalid bundleId format"}, nil
	}

	userID := internal.BuildUserID(participantID, bundleIDBytes)

	// Generate challenge and authentication token
	challengeSecret := gotp.RandomSecret(16)
	token := internal.NewAuthToken()

	// Get the contact information and check verification status
	phone, email, attemptNo, err := a.storage.CreateChallenge(userID, bundleIDBytes, challengeSecret, token)
	if err != nil {
		log.Warnw("new attempt for user failed", "userID", userID.String(), "error", err)
		return &internal.AuthResponse{Error: err.Error()}, nil
	}

	// Use the provided contact or fall back to stored contact
	contactToUse := contact
	if contactToUse == "" {
		if notificationType == notifications.Email {
			contactToUse = email
		} else {
			contactToUse = phone
		}
	}

	if contactToUse == "" {
		log.Warnw("contact is empty for user", "userID", userID.String())
		return &internal.AuthResponse{Error: "no contact information for this user"}, nil
	}

	// Generate OTP code
	hotp := gotp.NewDefaultHOTP(challengeSecret)
	otpCode := hotp.At(attemptNo)

	// Enqueue notification
	queue, ok := a.notificationQueues[notificationType]
	if !ok {
		return &internal.AuthResponse{Error: "invalid notification type"}, nil
	}

	if err := queue.Add(userID, bundleIDBytes, contactToUse, otpCode); err != nil {
		log.Warnw("cannot enqueue challenge", "error", err)
		return &internal.AuthResponse{Error: fmt.Sprintf("problem with %s challenge system", notificationType)}, nil
	}

	log.Infow("user challenged", "userID", userID.String(), "otpCode", otpCode, "contact", contactToUse)

	// Return last two digits of contact for verification
	var contactHint string
	if len(contactToUse) >= 2 {
		contactHint = contactToUse[len(contactToUse)-2:]
	}

	return &internal.AuthResponse{
		Success: true,
		Token:   &token,
		Message: []string{contactHint},
	}, nil
}

// VerifyChallenge verifies a challenge response
func (a *DefaultAuthenticator) VerifyChallenge(
	electionID internal.ElectionID,
	token internal.AuthToken,
	solution string,
) (*internal.AuthResponse, error) {
	if err := a.storage.VerifyChallenge(electionID, token, solution); err != nil {
		log.Warnw("verify challenge failed", "solution", solution, "error", err)
		return &internal.AuthResponse{Error: "challenge not completed: " + err.Error()}, nil
	}

	log.Infow("challenge verified", "solution", solution)
	return &internal.AuthResponse{
		Success: true,
		Token:   &token,
		Message: []string{"challenge resolved"},
	}, nil
}

// Sign signs a message using the specified signature type
func (a *DefaultAuthenticator) Sign(
	token internal.AuthToken,
	message []byte,
	electionID internal.ElectionID,
	bundleID string,
	sigType internal.SignatureType,
) (*internal.AuthResponse, error) {
	switch sigType {
	case internal.SignatureTypeBlind:
		// For blind signatures, the message is expected to be a blind point
		// Validate the blind point
		if _, err := a.signer.GetBlindPoint(message); err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("invalid blind point: %v", err),
			}, nil
		}

		// Get the secret key for this token
		secretK, err := a.signer.GetSecretKey(token.String())
		if err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("invalid token: %v", err),
			}, nil
		}

		// Sign the blinded message
		var processID []byte
		if bundleID != "" {
			var bundleIDBytes internal.ElectionID
			if err := bundleIDBytes.FromString(bundleID); err != nil {
				return &internal.AuthResponse{
					Success: false,
					Error:   "invalid bundleId format",
				}, nil
			}
			processID = bundleIDBytes
		} else {
			processID = electionID
		}

		signature, err := a.signer.SignBlind(processID, message, secretK)
		if err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("signing error: %v", err),
			}, nil
		}

		return &internal.AuthResponse{
			Success:   true,
			Signature: signature,
		}, nil

	case internal.SignatureTypeECDSA:
		// Get the user from the token
		user, err := a.storage.GetUserByToken(token)
		if err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("invalid token: %v", err),
			}, nil
		}

		// Find the election
		procID := electionID.String()
		if bundleID != "" {
			procID = bundleID
		}

		election, ok := user.Elections[procID]
		if !ok {
			return &internal.AuthResponse{
				Success: false,
				Error:   internal.ErrUserNotInElection.Error(),
			}, nil
		}

		if !election.Verified {
			return &internal.AuthResponse{
				Success: false,
				Error:   "user has not completed the challenge",
			}, nil
		}

		if election.VotedWith != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   "user already voted",
			}, nil
		}

		// Sign the message
		var processID []byte
		if bundleID != "" {
			var bundleIDBytes internal.ElectionID
			if err := bundleIDBytes.FromString(bundleID); err != nil {
				return &internal.AuthResponse{
					Success: false,
					Error:   "invalid bundleId format",
				}, nil
			}
			processID = bundleIDBytes
		} else {
			processID = electionID
		}

		signature, err := a.signer.SignECDSA(processID, message)
		if err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("signing error: %v", err),
			}, nil
		}

		// Mark the user as voted
		election.VotedWith = message
		user.Elections[procID] = election
		if err := a.storage.UpdateUser(user); err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("failed to update user: %v", err),
			}, nil
		}

		return &internal.AuthResponse{
			Success:   true,
			Signature: signature,
		}, nil

	case internal.SignatureTypeSharedKey:
		var processID []byte
		if bundleID != "" {
			var bundleIDBytes internal.ElectionID
			if err := bundleIDBytes.FromString(bundleID); err != nil {
				return &internal.AuthResponse{
					Success: false,
					Error:   "invalid bundleId format",
				}, nil
			}
			processID = bundleIDBytes
		} else {
			processID = electionID
		}

		sharedKey, err := a.signer.GenerateSharedKey(processID)
		if err != nil {
			return &internal.AuthResponse{
				Success: false,
				Error:   fmt.Sprintf("failed to generate shared key: %v", err),
			}, nil
		}

		return &internal.AuthResponse{
			Success:   true,
			Signature: sharedKey,
		}, nil

	default:
		return &internal.AuthResponse{
			Success: false,
			Error:   "invalid signature type",
		}, nil
	}
}

// GetPublicKey returns the public key for the specified signature type
func (a *DefaultAuthenticator) GetPublicKey(processID []byte, sigType internal.SignatureType) (string, error) {
	switch sigType {
	case internal.SignatureTypeBlind:
		pubKey := a.signer.GetBlindPublicKey()
		if processID == nil {
			return fmt.Sprintf("%x", pubKey.Bytes()), nil
		}

		saltedPubKey, err := internal.SaltBlindPublicKey(pubKey, processID)
		if err != nil {
			return "", fmt.Errorf("failed to salt public key: %w", err)
		}
		return fmt.Sprintf("%x", saltedPubKey.Bytes()), nil

	case internal.SignatureTypeECDSA:
		pubKey, err := a.signer.GetECDSAPublicKey()
		if err != nil {
			return "", fmt.Errorf("failed to get ECDSA public key: %w", err)
		}

		if processID == nil {
			return fmt.Sprintf("%x", pubKey), nil
		}

		saltedPubKey, err := internal.SaltECDSAPublicKey(pubKey, processID)
		if err != nil {
			return "", fmt.Errorf("failed to salt ECDSA public key: %w", err)
		}
		return fmt.Sprintf("%x", saltedPubKey), nil

	default:
		return "", fmt.Errorf("invalid signature type")
	}
}

// GenerateSharedKey generates a shared key for a process
func (a *DefaultAuthenticator) GenerateSharedKey(processID []byte) ([]byte, error) {
	return a.signer.GenerateSharedKey(processID)
}

// GetUser retrieves a user by ID
func (a *DefaultAuthenticator) GetUser(userID internal.UserID) (*internal.User, error) {
	return a.storage.GetUser(userID)
}

// sendSMSChallenge sends an SMS challenge
func (a *DefaultAuthenticator) sendSMSChallenge(phone string, challenge string) error {
	sanitizedPhone, err := sanitizePhone(phone)
	if err != nil {
		return fmt.Errorf("invalid phone number: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	notif := &notifications.Notification{
		ToNumber:  sanitizedPhone,
		Subject:   "Vocdoni verification code",
		PlainBody: fmt.Sprintf("Your authentication code is %s", challenge),
	}

	return a.notificationServices.SMS.SendNotification(ctx, notif)
}

// sendEmailChallenge sends an email challenge
func (a *DefaultAuthenticator) sendEmailChallenge(email string, challenge string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	notif := &notifications.Notification{
		ToAddress: email,
		Subject:   "Vocdoni verification code",
		PlainBody: fmt.Sprintf("Your authentication code is %s", challenge),
		Body:      fmt.Sprintf("Your authentication code is %s", challenge),
	}

	return a.notificationServices.Mail.SendNotification(ctx, notif)
}

// sanitizePhone sanitizes a phone number
func sanitizePhone(phone string) (string, error) {
	// This is a placeholder. In a real implementation, you would use a proper
	// phone number validation library like phonenumbers.
	return phone, nil
}
