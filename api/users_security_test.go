package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
)

// TestRecoveryCooldownKeepsCode guards the recovery anti-flood behavior: once a code has been
// delivered, an immediate second recovery request is silently accepted without replacing the
// stored code, for both the verified (password reset) and the unverified (account verification)
// branches — the code from the first email must keep working.
func TestRecoveryCooldownKeepsCode(t *testing.T) {
	c := qt.New(t)
	originalCooldown := testAPI.otpCooldown
	testAPI.otpCooldown = time.Hour
	defer func() {
		testAPI.otpCooldown = originalCooldown
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// --- unverified branch: recovery right after registration must not mint a new
	// verification code (the registration delivery started the cooldown)
	mail := fmt.Sprintf("%d-cooldown@test.com", internal.RandomInt(100000))
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail, Password: testPass, FirstName: testFirstName, LastName: testLastName},
		usersEndpoint)
	mailBody := waitForEmail(t, mail)
	verifyCode := verificationCodeRgx.FindStringSubmatch(mailBody)
	c.Assert(len(verifyCode) > 1, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)

	// the registration code still verifies the account: it was not replaced
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserVerification{Email: mail, Code: verifyCode[1]}, verifyUserEndpoint)

	// --- verified branch: a second recovery request during the cooldown is silently
	// accepted and the first reset code keeps working
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)
	mailBody = waitForEmail(t, mail)
	resetCode := passwordResetRgx.FindStringSubmatch(mailBody)
	c.Assert(len(resetCode) > 1, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "",
		&apicommon.UserInfo{Email: mail}, usersRecoveryPasswordEndpoint)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, "", &apicommon.UserPasswordReset{
		Email:       mail,
		Code:        resetCode[1],
		NewPassword: "cooldownpass123",
	}, usersResetPasswordEndpoint)
}
