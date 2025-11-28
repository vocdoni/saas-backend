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
// The caller is responsible for terminating the container.
func StartMailService(ctx context.Context) (testcontainers.Container, error) {
	smtpPort := fmt.Sprintf("%s/tcp", MailSMTPPort)
	apiPort := fmt.Sprintf("%s/tcp", MailAPIPort)

	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithImage("mailhog/mailhog"),
		testcontainers.WithExposedPorts(smtpPort, apiPort),
		testcontainers.WithWaitStrategy(wait.ForListeningPort(MailSMTPPort)),
	}

	container, err := testcontainers.Run(ctx, "mailhog/mailhog", opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to start mail container: %w", err)
	}
	return container, nil
}
