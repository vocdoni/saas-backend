package test

import (
	"context"
	"fmt"
	"path"

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
	// faucet
	VocfaucetPort       = 8080
	VocfaucetAmounts    = 800
	VocfaucetDatadir    = "/app/data/faucet"
	VocfaucetDBType     = "pebble"
	VocfaucetBaseRoute  = "/v2"
	VocfaucetAuth       = "open"
	VocfaucetWaitPeriod = "10m"
)

func VoconedAPIURL(containerURI string) string {
	return path.Join(containerURI, VoconedURLPath)
}

func StartVoconedContainer(ctx context.Context) (testcontainers.Container, error) {
	exposedPort := fmt.Sprintf("%d/tcp", VoconedPort)
	voconedCmd := []string{
		"--setTxCosts", fmt.Sprintf("--txCosts=%d", VoconedTxCosts),
		fmt.Sprintf("--fundedAccounts=%s:%d", VoconedFundedAccount, VoconedFunds),
	}

	return testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "ghcr.io/vocdoni/vocdoni-node:main",
				Entrypoint:   []string{"/app/voconed"},
				Cmd:          voconedCmd,
				ExposedPorts: []string{exposedPort},
				WaitingFor: wait.ForAll(
					wait.ForLog("Waiting for connections"),
					wait.ForListeningPort(nat.Port(exposedPort)),
				),
			},
			Started: true,
		})
}

func StartVocfaucetContainer(ctx context.Context) (testcontainers.Container, error) {
	exposedPort := fmt.Sprintf("%d/tcp", VocfaucetPort)
	vocfaucetCmd := []string{
		fmt.Sprintf("--amounts=%d", VocfaucetAmounts),
		fmt.Sprintf("--listenPort=%d", VocfaucetPort),
		fmt.Sprintf("--dataDir=%s", VocfaucetDatadir),
		fmt.Sprintf("--waitPeriod=%s", VocfaucetWaitPeriod),
		fmt.Sprintf("--dbType=%s", VocfaucetDBType),
		fmt.Sprintf("--baseRoute=%s", VocfaucetBaseRoute),
		fmt.Sprintf("--auth=%s", VocfaucetAuth),
		fmt.Sprintf("--privKey=%s", VoconedFoundedPrivKey),
	}

	return testcontainers.GenericContainer(ctx,
		testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "ghcr.io/vocdoni/vocfaucet:main",
				Cmd:          vocfaucetCmd,
				ExposedPorts: []string{exposedPort},
				WaitingFor: wait.ForAll(
					wait.ForLog("Waiting for connections"),
					wait.ForListeningPort(nat.Port(exposedPort)),
				),
			},
			Started: true,
		})
}
