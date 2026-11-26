// Package csp implements the Census Service Provider functionality
package csp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/log"
)

// challengeDelivery is where and how a challenge code is sent.
type challengeDelivery struct {
	to    string
	ctype notifications.ChallengeType
	lang  string
	org   notifications.OrganizationInfo
}

// dest is the destination key of the delivery (DestinationKey).
func (d challengeDelivery) dest() string {
	return DestinationKey(d.ctype, d.to)
}

// challengeCode is a code to deliver: whose it is and when it expires.
type challengeCode struct {
	userID    internal.HexBytes
	anchorID  internal.HexBytes
	code      string
	expiresAt time.Time
}

// challengeSend is one send of a challenge code: the composed message and the claims it holds,
// given back if the message never reaches the voter. The queue carries it back with the delivery
// result (NotificationChallenge.SendInfo).
type challengeSend struct {
	// challengeID is the challenge document the send was claimed on.
	challengeID internal.HexBytes
	// dest is the destination key the send was claimed for.
	dest string
	// claimedAt is the send time the claim stored; a rollback only matches it, so it never undoes a
	// later send.
	claimedAt time.Time
	// previous is the destination's send time before the claim, zero when it had none.
	previous time.Time
	// resend reports whether the claim spent one of the code's resends; a new code's first send
	// spends none.
	resend bool
	// daily is the unit of the member's daily send budget the send holds.
	daily db.CSPCounterClaim
	// ch is the composed message.
	ch *notifications.NotificationChallenge
}

// DestinationKey is the key under which a challenge records its sends to one destination: the
// lowercase hex of the first 16 bytes of SHA-256 over the challenge type, ":" and the destination.
// Contact matching compares emails with strings.EqualFold, so they are folded the same way first
// (upper then lower case maps each simple case-folding orbit, such as "ſ" and "s", to one form), or
// another spelling of one address would dodge its cooldown; phones arrive normalized. The key
// reveals no contact data.
func DestinationKey(ctype notifications.ChallengeType, to string) string {
	if ctype == notifications.EmailChallenge {
		to = strings.ToLower(strings.ToUpper(to))
	}
	sum := sha256.Sum256([]byte(string(ctype) + ":" + to))
	return hex.EncodeToString(sum[:16])
}

// AuthToken method generates a new authentication token for a user, anchored to
// a voting process, and sends its challenge code to `to`.
//
// While the user's last code is reusable (unverified, unlocked, and living at least one more
// cooldown) and can take another token, it is reused rather than replaced: the new token answers
// the same code, so every code the user received keeps working. The code is re-sent to `to` unless
// it was sent there less than the notification cooldown ago, its resends are spent, or so is the
// daily send cap. In those cases the token is returned without sending anything when `to` already
// got the code; a `to` that never got it goes on to a new code instead. Each call returns a
// distinct token, so whoever only knows the user's login data never holds the token the user
// verifies.
//
// Otherwise a new code is created, subject to the cooldown since the last code's latest send and
// to the daily send cap. It returns the token as HexBytes.
func (c *CSP) AuthToken(anchorID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string, orgAddress common.Address,
) (internal.HexBytes, error) {
	// check the input parameters
	if len(anchorID) == 0 {
		return nil, ErrNoAnchorID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}

	// For auth-only cases (no challenge type and no destination), create a pre-verified token
	if to == "" && ctype == "" {
		return c.createAuthOnlyToken(anchorID, uID)
	}
	delivery := challengeDelivery{
		to:    to,
		ctype: ctype,
		lang:  lang,
		org:   notifications.OrganizationInfo{Address: orgAddress, Name: orgName, Logo: orgLogo},
	}

	last, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && !errors.Is(err, db.ErrTokenNotFound) {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if last == nil {
		return c.newChallenge(uID, anchorID, delivery)
	}
	if c.reusable(last) {
		token, err := c.reuseChallenge(last, delivery)
		if err != nil || token != nil {
			return token, err
		}
		// the code took no more tokens, stopped being usable meanwhile, or cannot be sent to a
		// destination that never got it: a new one follows
	}
	if remaining := c.coolDownSince(last.LastSend()); remaining > 0 {
		log.Warnw("cooldown time not reached",
			"userID", uID,
			"anchorID", anchorID,
			"challenge", last.ID)
		return nil, coolDownError(remaining)
	}
	return c.newChallenge(uID, anchorID, delivery)
}

// coolDownSince is how long the notification cooldown still runs after a send at sentAt; zero or
// less once it has passed, or when there was no send.
func (c *CSP) coolDownSince(sentAt time.Time) time.Duration {
	if sentAt.IsZero() {
		return 0
	}
	return c.notificationCoolDownTime - time.Since(sentAt)
}

// reuseChallenge issues a new token for the user's pending code and re-sends the code to the
// delivery's destination when that is due. It returns a nil token, and no error, when the code
// takes no more tokens or stopped being usable before the send was claimed, so the caller creates
// a new code instead. A send refused by the destination's cooldown still returns the token, and so
// does one refused by the code's resends or the daily cap when the destination already got the
// code, as the user holds it. Refused so for a destination that never got the code, the token is
// dropped and the caller goes on to a new code. Any other failed send drops the token too.
func (c *CSP) reuseChallenge(challenge *db.CSPAuth, delivery challengeDelivery) (internal.HexBytes, error) {
	token := newAuthToken()
	added, err := c.Storage.AddCSPAuthToken(db.CSPTokenRequest{
		ChallengeID: challenge.ID,
		Token:       token,
		MaxTokens:   MaxChallengeTokens,
		MaxAttempts: MaxChallengeAttempts,
		TTL:         c.notificationTTL,
	})
	if err != nil {
		log.Warnw("error adding token to pending challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if !added {
		return nil, nil
	}
	// a destination that never got the code must not get a token without it
	sent := !challenge.Sends[delivery.dest()].IsZero()
	if !c.sendDue(challenge, delivery.dest()) {
		if sent {
			return token, nil
		}
		c.dropToken(challenge, token)
		return nil, nil
	}
	err = c.resendCode(challenge, token, delivery)
	if err == nil || errors.Is(err, errors.ErrAttemptCoolDownTime) ||
		(sent && errors.Is(err, errors.ErrVerificationMaxAttempts)) {
		return token, nil
	}
	c.dropToken(challenge, token)
	if errors.Is(err, ErrTokenExpired) || errors.Is(err, errors.ErrVerificationMaxAttempts) {
		return nil, nil
	}
	return nil, err
}

// dropToken removes a token reuseChallenge added to challenge without sending its code.
func (c *CSP) dropToken(challenge *db.CSPAuth, token internal.HexBytes) {
	if err := c.Storage.RemoveCSPAuthToken(challenge.ID, token); err != nil {
		log.Warnw("error dropping token of unsent challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", err)
	}
}

// sendDue reports whether challenge's code can be sent to dest now: it has resends left and was
// not sent there within the cooldown. ClaimCSPAuthSend decides; this only skips composing a send
// that would certainly be refused.
func (c *CSP) sendDue(challenge *db.CSPAuth, dest string) bool {
	return challenge.Resends < MaxChallengeResends && c.coolDownSince(challenge.Sends[dest]) <= 0
}

// resendCode re-sends challenge's code on a repeated auth request issuing token, in order: compose
// the message, claim a unit of the daily send cap, claim the send on the challenge, push. Each
// failed step gives back what the earlier ones claimed.
func (c *CSP) resendCode(challenge *db.CSPAuth, token internal.HexBytes, delivery challengeDelivery) error {
	ch, err := c.composeResend(challenge, delivery)
	if err != nil {
		return err
	}
	daily, err := c.claimDailyChallengeSend(challenge.UserID, challenge.AnchorID)
	if err != nil {
		return err
	}
	dest := delivery.dest()
	claim, err := c.claimSend(challenge, token, dest)
	if err != nil {
		c.releaseChallengeSend(daily)
		return err
	}
	_, err = c.pushChallengeCode(&challengeSend{
		challengeID: challenge.ID,
		dest:        dest,
		claimedAt:   claim.At,
		previous:    claim.Previous,
		resend:      true,
		daily:       daily,
		ch:          ch,
	})
	return err
}

// newChallenge creates a new code for the user and sends it, in order: compose the message, claim
// a unit of the daily send cap, store the code with its first send, push. A code that never
// reached the queue is deleted again.
func (c *CSP) newChallenge(uID, anchorID internal.HexBytes, delivery challengeDelivery) (internal.HexBytes, error) {
	token, secret, code := c.generateToken()
	ch, err := c.composeChallenge(challengeCode{
		userID:    uID,
		anchorID:  anchorID,
		code:      code,
		expiresAt: time.Now().Add(c.notificationTTL),
	}, delivery)
	if err != nil {
		return nil, err
	}
	// cap the challenges (new tokens + resends) this member can be sent for this process today:
	// the cooldown only spaces sends, so without a cap a member's contact could be flooded with
	// one email/SMS per cooldown period all day long
	daily, err := c.claimDailyChallengeSend(uID, anchorID)
	if err != nil {
		return nil, err
	}
	dest := delivery.dest()
	challenge, err := c.Storage.NewCSPChallenge(db.CSPChallengeRequest{
		Token:    token,
		UserID:   uID,
		AnchorID: anchorID,
		Secret:   secret,
		Dest:     dest,
	})
	if err != nil {
		log.Warnw("error storing new challenge",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		c.releaseChallengeSend(daily)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth token stored",
		"userID", uID,
		"anchorID", anchorID,
		"token", token)
	queued, err := c.pushChallengeCode(&challengeSend{
		challengeID: challenge.ID,
		dest:        dest,
		claimedAt:   challenge.Sends[dest],
		daily:       daily,
		ch:          ch,
	})
	if err != nil {
		// a code that never reached the queue must not look pending, or the next step 0 would
		// hand out a token for it without sending anything
		if !queued {
			if _, delErr := c.Storage.DeleteCSPChallenge(challenge.ID); delErr != nil {
				log.Warnw("error deleting unsent challenge", "userID", uID, "anchorID", anchorID, "error", delErr)
			}
		}
		return nil, err
	}
	return token, nil
}

// ChallengeSendRefusal returns, without spending anything, the refusal AuthToken would answer
// right now for the member on the anchor process and the destination dest (DestinationKey): the
// cooldown since the last code's latest send, or the daily send cap. It is a peek, for a caller
// that must not spend something else (a certificate code) on a request AuthToken would refuse;
// AuthToken still enforces both. Reusing a code that can take another token is never refused when
// dest already got it, and otherwise only when the code cannot be sent to dest.
func (c *CSP) ChallengeSendRefusal(uID, anchorID internal.HexBytes, dest string) error {
	last, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && !errors.Is(err, db.ErrTokenNotFound) {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return ErrStorageFailure
	}
	reuse := last != nil && c.reusable(last) && len(last.Tokens) < MaxChallengeTokens
	if reuse && !last.Sends[dest].IsZero() {
		return nil
	}
	reached, err := c.Storage.CSPCounterReached(db.CSPCounterChallengeSends, counterKey(uID, anchorID),
		c.maxDailyChallengeSends)
	if err != nil {
		log.Warnw("error reading daily challenge sends", "userID", uID, "anchorID", anchorID, "error", err)
		return ErrStorageFailure
	}
	// the reused code reaches dest if it has a resend and a daily send left; else a new code follows
	if reuse && c.sendDue(last, dest) && !reached {
		return nil
	}
	if last != nil {
		if remaining := c.coolDownSince(last.LastSend()); remaining > 0 {
			return coolDownError(remaining)
		}
	}
	if reached {
		window := db.CSPCounterWindow(db.CSPCounterChallengeSends)
		resetAt := time.Now().UTC().Truncate(window).Add(window)
		return counterLimitError(db.CSPCounterResult{ResetAt: resetAt}, "daily challenge limit reached, try again tomorrow")
	}
	return nil
}

// ResendClaim is a resend ClaimChallengeResend claimed: the send ResendChallenge makes once the
// caller has matched the contact against the member. A claim never passed to ResendChallenge
// stays spent.
type ResendClaim struct {
	challenge *db.CSPAuth
	to        string
	ctype     notifications.ChallengeType
	send      challengeSend
}

// ClaimChallengeResend claims, in one update of the code the token answers, one of the code's
// MaxChallengeResends resends and the send to the submitted destination (to, ctype). The caller
// claims before checking that destination against the member, so a wrong contact keeps both
// spent: otherwise a token holder could probe a member's other contact (say, the phone once they
// know the email) with unlimited resends. The resends are counted per code, so every token sharing
// it shares them. Inside the destination's cooldown it spends nothing and fails with
// ErrAttemptCoolDownTime carrying the remaining milliseconds; once the resends are spent it fails
// with ErrVerificationMaxAttempts.
func (c *CSP) ClaimChallengeResend(token internal.HexBytes, to string, ctype notifications.ChallengeType,
) (*ResendClaim, error) {
	if len(token) == 0 {
		return nil, ErrInvalidAuthToken
	}
	if to == "" || ctype == "" {
		return nil, errors.ErrInvalidData.Withf("missing challenge destination or type")
	}
	challenge, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, ErrInvalidAuthToken
		}
		log.Warnw("error getting token for resend",
			"token", token,
			"error", err)
		return nil, ErrStorageFailure
	}
	dest := DestinationKey(ctype, to)
	claim, err := c.claimSend(challenge, token, dest)
	if err != nil {
		return nil, err
	}
	return &ResendClaim{
		challenge: challenge,
		to:        to,
		ctype:     ctype,
		send: challengeSend{
			challengeID: challenge.ID,
			dest:        dest,
			claimedAt:   claim.At,
			previous:    claim.Previous,
			resend:      true,
		},
	}, nil
}

// ResendChallenge sends the code of a resend claimed with ClaimChallengeResend, counting it against
// the member's daily challenge budget. If the message cannot be composed or the daily budget is
// spent, nothing is sent and the claim is given back.
func (c *CSP) ResendChallenge(claim *ResendClaim, lang string, org notifications.OrganizationInfo) error {
	if claim == nil {
		return ErrInvalidAuthToken
	}
	send := &claim.send
	ch, err := c.composeResend(claim.challenge, challengeDelivery{to: claim.to, ctype: claim.ctype, lang: lang, org: org})
	if err != nil {
		c.rollbackSend(send)
		return err
	}
	if send.daily, err = c.claimDailyChallengeSend(claim.challenge.UserID, claim.challenge.AnchorID); err != nil {
		c.rollbackSend(send)
		return err
	}
	send.ch = ch
	_, err = c.pushChallengeCode(send)
	return err
}

// ResendCoolDown returns how long until the code the token answers can be sent again to the
// destination dest (DestinationKey), zero if it can be sent now. ok is false when no resend is
// possible: an auth-only or verified token, a code that is gone, solved, expired or locked or has
// spent its resends, a spent daily send cap, or a storage error. It is a hint for clients; the
// resend itself is still refused by the cooldown it reports.
func (c *CSP) ResendCoolDown(token internal.HexBytes, dest string) (remaining time.Duration, ok bool) {
	challenge, err := c.Storage.CSPAuth(token)
	if err != nil || challenge.Verified || !c.challengeUsable(challenge) || challenge.Resends >= MaxChallengeResends {
		return 0, false
	}
	reached, err := c.Storage.CSPCounterReached(db.CSPCounterChallengeSends,
		counterKey(challenge.UserID, challenge.AnchorID), c.maxDailyChallengeSends)
	if err != nil || reached {
		return 0, false
	}
	return max(c.coolDownSince(challenge.Sends[dest]), 0), true
}

// coolDownError is the ErrAttemptCoolDownTime answer carrying the milliseconds left, at least one.
func coolDownError(remaining time.Duration) errors.Error {
	remaining = max(remaining, time.Millisecond)
	return errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": remaining.Milliseconds()})
}

// claimSend claims a send of challenge's code to dest for the requesting token (ClaimCSPAuthSend).
// When the claim does not match it re-reads the challenge through that token, not the one
// challenge was read through, and returns why (sendRefusal), so a request that lost a send race
// reports the wait the winner stored; if nothing refuses the send any more, it claims once again.
func (c *CSP) claimSend(challenge *db.CSPAuth, token internal.HexBytes, dest string) (db.CSPSendClaim, error) {
	req := db.CSPSendClaimRequest{
		ChallengeID: challenge.ID,
		Dest:        dest,
		CoolDown:    c.notificationCoolDownTime,
		TTL:         c.notificationTTL,
		MaxAttempts: MaxChallengeAttempts,
		MaxResends:  MaxChallengeResends,
	}
	for range 2 {
		claim, claimed, err := c.Storage.ClaimCSPAuthSend(req)
		if err != nil {
			log.Warnw("error claiming challenge send",
				"userID", challenge.UserID,
				"anchorID", challenge.AnchorID,
				"challenge", challenge.ID,
				"error", err)
			return db.CSPSendClaim{}, ErrStorageFailure
		}
		if claimed {
			return claim, nil
		}
		fresh, err := c.Storage.CSPAuth(token)
		if err != nil {
			if errors.Is(err, db.ErrTokenNotFound) {
				return db.CSPSendClaim{}, ErrTokenExpired
			}
			log.Warnw("error reading challenge after a refused send", "challenge", challenge.ID, "error", err)
			return db.CSPSendClaim{}, ErrStorageFailure
		}
		if refusal := c.sendRefusal(fresh, dest); refusal != nil {
			return db.CSPSendClaim{}, refusal
		}
	}
	// both claims lost to concurrent sends that were gone by the re-read: rare, and retryable
	log.Warnw("challenge send claim lost to concurrent sends twice", "challenge", challenge.ID)
	return db.CSPSendClaim{}, ErrStorageFailure
}

// sendRefusal is why a send of challenge's code to dest cannot be claimed, read from a fresh copy
// of the challenge viewed through the requesting token; nil when nothing refuses it.
func (c *CSP) sendRefusal(challenge *db.CSPAuth, dest string) error {
	if challenge.Verified {
		// a verified token is always reported as such, regardless of age, so it never
		// masquerades as merely expired
		left := max(c.notificationTTL-time.Since(challenge.CreatedAt), 0)
		return errors.ErrUserAlreadyVerified.WithData(map[string]any{"coolDownTime": left.Seconds()})
	}
	// no secret (solved, or never had one), expired or locked: ErrTokenExpired prompts the client
	// to restart the OTP flow, and a code is never regenerated from an empty secret
	if !c.challengeUsable(challenge) {
		log.Warnw("send requested for token without usable challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"token", challenge.Token,
			"attempts", challenge.Attempts)
		return ErrTokenExpired
	}
	if remaining := c.coolDownSince(challenge.Sends[dest]); remaining > 0 {
		return coolDownError(remaining)
	}
	if challenge.Resends >= MaxChallengeResends {
		return errors.ErrVerificationMaxAttempts.Withf("challenge resend limit reached, request a new token")
	}
	return nil
}

// reusable reports whether a step 0 may give challenge's code another token: the code is usable
// and lives at least one more cooldown, so it is not re-sent only to expire before it can be
// replaced.
func (c *CSP) reusable(challenge *db.CSPAuth) bool {
	return c.challengeUsable(challenge) &&
		c.notificationTTL-time.Since(challenge.CreatedAt) >= c.notificationCoolDownTime
}

// challengeUsable reports whether the challenge's code can still be accepted: it has a secret (not
// yet solved), is not verified, not expired and not locked by attempts.
func (c *CSP) challengeUsable(challenge *db.CSPAuth) bool {
	return challenge.Secret != "" &&
		len(challenge.VerifiedBy) == 0 &&
		challenge.Attempts < MaxChallengeAttempts &&
		time.Since(challenge.CreatedAt) < c.notificationTTL
}

// composeResend composes the message carrying challenge's existing code, which expires with the
// code.
func (c *CSP) composeResend(challenge *db.CSPAuth, delivery challengeDelivery,
) (*notifications.NotificationChallenge, error) {
	code, err := c.regenerateTokenCode(challenge.Secret)
	if err != nil {
		log.Warnw("error regenerating token code",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", err)
		return nil, ErrChallengeCodeFailure
	}
	return c.composeChallenge(challengeCode{
		userID:    challenge.UserID,
		anchorID:  challenge.AnchorID,
		code:      code,
		expiresAt: challenge.CreatedAt.Add(c.notificationTTL),
	}, delivery)
}

// composeChallenge composes the notification carrying a challenge code, advertising the time left
// until the code expires.
func (*CSP) composeChallenge(code challengeCode, delivery challengeDelivery,
) (*notifications.NotificationChallenge, error) {
	ch, err := notifications.NewNotificationChallenge(delivery.ctype, delivery.lang, code.userID, code.anchorID,
		delivery.to, code.code, delivery.org, time.Until(code.expiresAt).Round(time.Second).String())
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", code.userID,
			"anchorID", code.anchorID,
			"error", err)
		return nil, ErrNotificationFailure
	}
	ch.ExpiresAt = code.expiresAt
	return ch, nil
}

// pushChallengeCode pushes the message of send to the queue, which carries send back with the
// delivery result so a send no provider was called for gives back its claims (settleNotification).
// When the message never reached the queue, it gives them back itself and queued is false. A synchronous
// delivery that timed out is still queued and keeps them for the queue result to settle.
func (c *CSP) pushChallengeCode(send *challengeSend) (queued bool, err error) {
	send.ch.SendInfo = send
	err = c.pushChallenge(send.ch)
	if err == nil {
		return true, nil
	}
	log.Warnw("error pushing notification challenge",
		"userID", send.ch.UserID,
		"anchorID", send.ch.AnchorID,
		"error", err)
	if errors.Is(err, errChallengeStillQueued) {
		return true, ErrNotificationFailure
	}
	c.rollbackSend(send)
	return false, ErrNotificationFailure
}

// rollbackSend gives back the claims of a send no provider was called for: the send on the
// challenge, unless a later send replaced it, and the daily-cap unit, always, as nothing was
// delivered for it.
func (c *CSP) rollbackSend(send *challengeSend) {
	if _, err := c.Storage.RollbackCSPAuthSend(db.CSPSendRollback{
		ChallengeID: send.challengeID,
		Dest:        send.dest,
		ClaimedAt:   send.claimedAt,
		Previous:    send.previous,
		Resend:      send.resend,
	}); err != nil {
		log.Warnw("error rolling back challenge send", "challenge", send.challengeID, "error", err)
	}
	c.releaseChallengeSend(send.daily)
}

// claimDailyChallengeSend claims one unit of the member's daily challenge-send budget for the
// anchor process, counting new tokens and resends together. The counter is Mongo-backed, so it
// survives service restarts. Fails closed on storage errors.
func (c *CSP) claimDailyChallengeSend(uID, anchorID internal.HexBytes) (db.CSPCounterClaim, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterChallengeSends,
		counterKey(uID, anchorID), c.maxDailyChallengeSends)
	if err != nil {
		log.Warnw("error claiming daily challenge send",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if !res.Granted {
		return nil, counterLimitError(res, "daily challenge limit reached, try again tomorrow")
	}
	return res.Claim, nil
}

// releaseChallengeSend gives back a claimDailyChallengeSend unit for a send no provider was called
// for. A nil claim is a no-op. Storage errors are logged: at worst the member loses one unit
// of today's budget.
func (c *CSP) releaseChallengeSend(claim db.CSPCounterClaim) {
	if err := c.Storage.ReleaseCSPCounter(claim); err != nil {
		log.Warnw("error releasing daily challenge send", "error", err)
	}
}

// counterLimitError is the 40017 a refused counter claim answers, carrying in its data the
// seconds until the counter's window resets as retryAfter.
func counterLimitError(res db.CSPCounterResult, msg string) errors.Error {
	retryAfter := int64(math.Ceil(time.Until(res.ResetAt).Seconds()))
	if retryAfter < 1 {
		retryAfter = 1
	}
	return errors.ErrVerificationMaxAttempts.With(msg).WithData(map[string]any{"retryAfter": retryAfter})
}

// counterKey joins an identity (a member id, or a login-fields hash) and an anchor (process) id
// into the key of a per-identity-per-process counter.
func counterKey(uID, anchorID internal.HexBytes) []byte {
	key := make([]byte, 0, len(uID)+len(anchorID))
	key = append(key, uID...)
	key = append(key, anchorID...)
	return key
}

// AuthAttemptClaim holds the failed-attempt units one step-0 attempt reserved with
// ClaimAuthAttempt.
type AuthAttemptClaim []db.CSPCounterClaim

// ClaimAuthAttempt reserves, before a step-0 attempt is checked, one unit of the current hour's
// failed-authentication budgets on the anchor process: the submission's (login plus contact
// fields), capped at maxHourlyAuthFailures, and, when voter is not empty, the claimed voter's
// (login fields alone), capped at VoterAuthFailuresFactor times that. Spending a voter's coarse
// budget takes that many failed submissions, so whoever merely knows a voter's login fields can no
// longer lock them out with a handful of junk-contact requests, while guessing one voter's contact
// stays bounded. Reserving up front, rather than counting failures afterwards, keeps concurrent
// attempts from all passing a not-yet-reached check. The caller must hand the claim to
// ReleaseAuthAttempt unless the attempt fails on the submitted login or contact data. The counters
// are Mongo-backed, so they survive service restarts.
//
// It returns an ErrVerificationMaxAttempts carrying retryAfter (seconds until the hour resets)
// once either budget is spent, and an ErrInternalStorageError if a counter cannot be claimed.
func (c *CSP) ClaimAuthAttempt(anchorID internal.HexBytes, submission, voter []byte) (AuthAttemptClaim, error) {
	// an empty key would make the process's own budget, shared by every caller
	if len(submission) == 0 {
		return nil, errors.ErrGenericInternalServerError.Withf("empty auth attempt key")
	}
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterAuthFailures,
		counterKey(submission, anchorID), c.maxHourlyAuthFailures)
	if err != nil {
		return nil, errors.ErrInternalStorageError.WithErr(fmt.Errorf("claiming auth attempt: %w", err))
	}
	if !res.Granted {
		return nil, counterLimitError(res, "too many failed authentication attempts, try again later")
	}
	claim := AuthAttemptClaim{res.Claim}
	if len(voter) == 0 {
		return claim, nil
	}
	res, err = c.Storage.ClaimCSPCounter(db.CSPCounterVoterAuthFailures,
		counterKey(voter, anchorID), c.maxHourlyAuthFailures*VoterAuthFailuresFactor)
	if err != nil {
		c.ReleaseAuthAttempt(claim)
		return nil, errors.ErrInternalStorageError.WithErr(fmt.Errorf("claiming voter auth attempt: %w", err))
	}
	if !res.Granted {
		c.ReleaseAuthAttempt(claim)
		return nil, counterLimitError(res, "too many failed authentication attempts, try again later")
	}
	return append(claim, res.Claim), nil
}

// ReleaseAuthAttempt gives back a ClaimAuthAttempt reservation for an attempt that did not fail on
// the submitted data. Storage errors are logged but not returned: at worst the identity loses one
// unit of the current hour's budget.
func (c *CSP) ReleaseAuthAttempt(claim AuthAttemptClaim) {
	for _, cl := range claim {
		if err := c.Storage.ReleaseCSPCounter(cl); err != nil {
			log.Warnw("error releasing auth attempt", "error", err)
		}
	}
}

// VerifyAuthToken method verifies the authentication token for a user. It gets the user data from the token and checks
// if the process is already consumed. It checks if the process is related to
// the user and if the token matches. It verifies the solution and updates the
// user data in the storage. It returns an error if the process is already
// consumed, if the process is not related to the user, if the token does not
// match, if the solution is not correct or if there is an error updating the
// user data.
func (c *CSP) VerifyAuthToken(token internal.HexBytes, solution string) error {
	if len(token) == 0 {
		return ErrInvalidAuthToken
	}
	if len(solution) == 0 {
		return ErrInvalidSolution
	}
	// get the user data from the token
	authTokenData, err := c.Storage.CSPAuth(token)
	if err != nil {
		log.Warnw("error getting user data by token",
			"token", token,
			"error", err)
		return ErrInvalidAuthToken
	}
	// reject tokens whose code has no secret: auth-only grants, and codes already solved through
	// this token or another one. An OTP derived from an empty secret would be trivially guessable.
	// ErrTokenExpired prompts the client to restart the OTP flow rather than treating it as a hard
	// failure.
	if authTokenData.Verified || authTokenData.Secret == "" {
		log.Warnw("verification attempted for token without challenge secret",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// reject if the OTP window has passed
	if time.Since(authTokenData.CreatedAt) > c.notificationTTL {
		log.Warnw("OTP expired",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrTokenExpired
	}
	// claim one attempt on the code before comparing it: the conditional increment only succeeds
	// while attempts are below the cap, so concurrent requests cannot all evaluate a guess against
	// the same stale count. Attempts are counted per code, so issuing more tokens for it grants no
	// more.
	recorded, err := c.Storage.IncrementCSPAuthAttempts(token, MaxChallengeAttempts)
	if errors.Is(err, db.ErrChallengeConsumed) {
		// a concurrent verification solved the code first
		return ErrTokenExpired
	}
	if err != nil {
		// fail closed: if we cannot persist the attempt, do not allow the
		// verification to proceed, otherwise attempt limiting could be
		// bypassed while the database is unhealthy
		log.Warnw("error claiming challenge attempt",
			"token", token,
			"challenge", authTokenData.ID,
			"error", err)
		return ErrStorageFailure
	}
	if !recorded {
		log.Warnw("too many challenge attempts",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"attempts", MaxChallengeAttempts) // the conditional increment never exceeds the cap
		return ErrTooManyAttempts
	}
	if !c.verifySolution(authTokenData.Secret, solution) {
		log.Warnw("challenge code does not match",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token)
		return ErrChallengeCodeFailure
	}
	// set the token as verified
	if err := c.Storage.VerifyCSPAuth(token); err != nil {
		if err == db.ErrChallengeConsumed {
			// a concurrent verification solved the challenge first
			return ErrTokenExpired
		}
		log.Warnw("error verifying token",
			"userID", authTokenData.UserID,
			"anchorID", authTokenData.AnchorID,
			"token", token,
			"error", err)
		return ErrStorageFailure
	}
	return nil
}

// generateToken generates a new authentication token with a random OTP secret.
// It returns the bearer token, the base32 OTP secret, and the 6-digit code.
func (*CSP) generateToken() (bToken internal.HexBytes, secret, code string) {
	secret = gotp.RandomSecret(16)
	code = gotp.NewDefaultHOTP(secret).At(0)
	return newAuthToken(), secret, code
}

// newAuthToken returns a new random bearer token.
func newAuthToken() internal.HexBytes {
	// a uuid.UUID is a [16]byte; slice its bytes directly to avoid the fallible
	// MarshalBinary call (and the panic path it would require).
	id := uuid.New()
	return internal.HexBytes(id[:])
}

// regenerateTokenCode recomputes the OTP code for a stored secret (used for resend).
func (*CSP) regenerateTokenCode(secret string) (string, error) {
	return gotp.NewDefaultHOTP(secret).At(0), nil
}

// verifySolution checks whether solution matches the HOTP code for secret. The
// comparison is constant-time to avoid leaking the code through timing.
func (*CSP) verifySolution(secret, solution string) bool {
	code := gotp.NewDefaultHOTP(secret).At(0)
	return subtle.ConstantTimeCompare([]byte(code), []byte(solution)) == 1
}

// createAuthOnlyToken creates a pre-verified token for auth-only censuses, which need no challenge.
func (c *CSP) createAuthOnlyToken(anchorID, uID internal.HexBytes) (internal.HexBytes, error) {
	token := newAuthToken()
	if err := c.Storage.SetVerifiedCSPAuth(token, uID, anchorID); err != nil {
		log.Warnw("error setting new auth-only token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth-only token created and verified",
		"userID", uID,
		"anchorID", anchorID,
		"token", token)
	return token, nil
}
