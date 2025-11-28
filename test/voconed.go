// Package test provides testing utilities for the saas-backend service,
// including test containers for mail, MongoDB, and Voconed services.
package test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// VoconedURLPath is the URL path for the Voconed API.
	VoconedURLPath = "/v2"
	// VoconedPort is the port used by the Voconed service.
	VoconedPort           = 9095
	VoconedTxCosts        = 10
	VoconedFundedAccount  = "0x032FaEf5d0F2c76bbD804215e822A5203e83385d"
	VoconedFoundedPrivKey = "d52a488fa1511a07778cc94ed9d8130fb255537783ea7c669f38292b4f53ac4f"
	VoconedFunds          = 100000000
	VoconedVolumeName     = "voconed-data-test"
	VoconedDataDir        = "/app/data"
	VocfaucetBaseRoute    = "/v2"
	VocfaucetAmounts      = 5
)

// VoconedAPIURL constructs the full URL for the Voconed API from a base URL.
func VoconedAPIURL(base string) string {
	apiURL, err := url.JoinPath(base, VocfaucetBaseRoute)
	if err != nil {
		panic(err)
	}
	return apiURL
}

// StartVoconedContainer starts a Voconed container for testing.
// It returns the container and any error encountered during startup.
// The caller is responsible for terminating the container.
func StartVoconedContainer(ctx context.Context) (testcontainers.Container, error) {
	dataDir := path.Join(os.TempDir(), "voconed-test-datadir")
	exposedPort := fmt.Sprintf("%d/tcp", VoconedPort)
	voconedCmd := []string{
		"--logLevel=debug",
		fmt.Sprintf("--dir=%s", VoconedDataDir),
		"--setTxCosts", fmt.Sprintf("--txCosts=%d", VoconedTxCosts),
		fmt.Sprintf("--fundedAccounts=%s:%d", VoconedFundedAccount, VoconedFunds),
		fmt.Sprintf("--port=%d", VoconedPort),
		fmt.Sprintf("--enableFaucet=%d", VocfaucetAmounts),
		"--blockPeriod=1",
	}

	opts := []testcontainers.ContainerCustomizer{
		testcontainers.WithImage("ghcr.io/vocdoni/vocdoni-node:main"),
		testcontainers.WithImagePlatform("linux/amd64"),
		testcontainers.WithEntrypoint("/app/voconed"),
		testcontainers.WithCmd(voconedCmd...),
		testcontainers.WithExposedPorts(exposedPort),
		testcontainers.WithWaitStrategy(wait.ForListeningPort(nat.Port(exposedPort))),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.AutoRemove = false
			// Set up a bind mount: hostPath:containerPath
			hc.Binds = append(hc.Binds, fmt.Sprintf("%s:%s", dataDir, VoconedDataDir))
		}),
		testcontainers.WithAlwaysPull(),
	}

	c, err := testcontainers.Run(ctx, "ghcr.io/vocdoni/vocdoni-node:main", opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to start voconed container: %w", err)
	}
	return c, nil
}
