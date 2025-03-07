package test

import (
	"context"
	"fmt"
	"net/url"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// voconed
	VoconedURLPath        = "/v2"
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

func VoconedAPIURL(base string) string {
	apiURL, err := url.JoinPath(base, VocfaucetBaseRoute)
	if err != nil {
		panic(err)
	}
	return apiURL
}

func StartVoconedContainer(ctx context.Context) (testcontainers.Container, error) {
	exposedPort := fmt.Sprintf("%d/tcp", VoconedPort)
	voconedCmd := []string{
		"--logLevel=debug",
		fmt.Sprintf("--dir=%s", VoconedDataDir),
		"--setTxCosts", fmt.Sprintf("--txCosts=%d", VoconedTxCosts),
		fmt.Sprintf("--fundedAccounts=%s:%d", VoconedFundedAccount, VoconedFunds),
		fmt.Sprintf("--port=%d", VoconedPort),
		fmt.Sprintf("--enableFaucet=%d", VocfaucetAmounts),
		"--blockPeriod=2",
	}
	c, err := testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:         "ghcr.io/vocdoni/vocdoni-node:main",
				Entrypoint:    []string{"/app/voconed"},
				Cmd:           voconedCmd,
				ImagePlatform: "linux/amd64",
				ExposedPorts:  []string{exposedPort},
				WaitingFor:    wait.ForListeningPort(nat.Port(exposedPort)),
				Mounts: testcontainers.ContainerMounts{
					testcontainers.VolumeMount(VoconedVolumeName, VoconedDataDir),
				},
				HostConfigModifier: func(hc *container.HostConfig) {
					hc.AutoRemove = false
				},
			},
			Started: true,
		})
	if err != nil {
		return nil, err
	}
	return c, nil
}
