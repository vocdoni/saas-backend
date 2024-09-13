package test

import (
	"context"
	"fmt"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	MailSMTPPort = "1025"
	MailAPIPort  = "8025"
)

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
