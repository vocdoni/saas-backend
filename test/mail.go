// Package test provides testing utilities for the saas-backend service,
// including test containers for mail, MongoDB, and Voconed services.
package test

import (
	"context"
	"fmt"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// MailSMTPPort is the SMTP port used by the mail test container.
	MailSMTPPort = "1025"
	// MailAPIPort is the API port used by the mail test container.
	MailAPIPort = "8025"
)

// StartMailService starts a MailHog container for testing email functionality.
// It returns the container and any error encountered during startup.
func StartMailService(ctx context.Context) (testcontainers.Container, error) {
	smtpPort := fmt.Sprintf("%s/tcp", MailSMTPPort)
	apiPort := fmt.Sprintf("%s/tcp", MailAPIPort)
	return testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "mailhog/mailhog",
				ExposedPorts: []string{smtpPort, apiPort},
				WaitingFor:   wait.ForListeningPort(MailSMTPPort),
			},
			Started: true,
		})
}
