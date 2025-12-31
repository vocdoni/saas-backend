package notifications

import (
	"context"
	"regexp"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

func TestNotificationChallengeQueue(t *testing.T) {
	c := qt.New(t)
	// create a notification without to address to force an error during the
	// sending
	c.Assert(mailtemplates.Load(), qt.IsNil)
	notification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).ExecPlain(struct {
		Code         string
		Organization string
	}{"123456", testOrgName})
	c.Assert(err, qt.IsNil)

	c.Run("retries reached", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, time.Minute, time.Second, testMailService, nil)
		go queue.Start()
		c.Assert(queue.Push(&NotificationChallenge{
			Type:         EmailChallenge,
			UserID:       []byte("user"),
			BundleID:     []byte("bundle"),
			Notification: notification,
			CreatedAt:    time.Now(),
			Retries:      0,
			Success:      false,
		}), qt.IsNil)

	outer:
		for {
			select {
			case errCh := <-queue.NotificationsSent:
				c.Assert(errCh.Success, qt.IsFalse)
				c.Assert(errCh.Retries, qt.Equals, DefaultQueueMaxRetries)
				break outer
			case <-time.After(DefaultQueueMaxRetries * time.Second * 2):
				// wait for the retries to be reached
				c.Fail()
				return
			}
		}
	})

	c.Run("ttl reached", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, time.Second*10, time.Second*15, testMailService, nil)
		go queue.Start()

		c.Assert(queue.Push(&NotificationChallenge{
			Type:         EmailChallenge,
			UserID:       []byte("user"),
			BundleID:     []byte("bundle"),
			Notification: notification,
			CreatedAt:    time.Now(),
			Retries:      0,
			Success:      false,
		}), qt.IsNil)

	outer:
		for {
			select {
			case errCh := <-queue.NotificationsSent:
				c.Assert(errCh.Success, qt.IsFalse)
				c.Assert(errCh.Retries, qt.Equals, 0)
				break outer
			case <-time.After(time.Second * 25):
				// wait for the ttl to be reached
				c.Fail()
				return
			}
		}
	})

	c.Run("success", func(c *qt.C) {
		c.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, time.Second*10, time.Second, testMailService, nil)
		go queue.Start()

		c.Assert(mailtemplates.Load(), qt.IsNil)
		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgMeta, testRemainingTime)
		c.Assert(err, qt.IsNil)
		c.Assert(queue.Push(nc), qt.IsNil)
	outer:
		for {
			select {
			case res := <-queue.NotificationsSent:
				c.Assert(res.Success, qt.IsTrue)
				// get the verification code from the email
				mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
				c.Assert(err, qt.IsNil)
				// parse the email body to get the verification code
				seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
					ExecPlain(struct {
						Code         string
						Organization string
					}{`(.{6})`, testOrgName})
				c.Assert(err, qt.IsNil)
				rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
				// verify the user
				mailCode := rgxNotification.FindStringSubmatch(mailBody)
				c.Assert(mailCode, qt.HasLen, 2)
				c.Assert(mailCode[1], qt.Equals, "123456")
				break outer
			case <-time.After(time.Second * 25):
				// wait for the ttl to be reached
				c.Fail()
				return
			}
		}
	})
}
