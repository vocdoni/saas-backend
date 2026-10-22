// Package test provides testing utilities for the saas-backend service,
// including test containers for mail and MongoDB, and vochain-sequencer test chains.
package test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"go.vocdoni.io/dvote/apiclient"
)

const (
	// VoconedURLPath is the URL path for the Voconed API.
	VoconedURLPath = "/v2"
	// VoconedTxCosts is the flat per-transaction cost set on the test chain.
	VoconedTxCosts = 10
	// VoconedFundedAccount is the well-known account funded on the test chain (the SaaS
	// service account used to sign and fund transactions).
	VoconedFundedAccount = "0x032FaEf5d0F2c76bbD804215e822A5203e83385d"
	// VoconedFoundedPrivKey is the private key of VoconedFundedAccount.
	VoconedFoundedPrivKey = "d52a488fa1511a07778cc94ed9d8130fb255537783ea7c669f38292b4f53ac4f"
	// VoconedFunds is the initial balance minted to VoconedFundedAccount.
	VoconedFunds = 100000000
)

// sequencerModuleDir is the nested Go module that pins vochain-sequencer for the tests.
func sequencerModuleDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "sequencer")
}

// BuildVoconed builds the voconed binary of the vochain-sequencer pinned by the nested test module
// test/sequencer into dir, and returns its path. The production module does not depend on
// vochain-sequencer; only this build does.
func BuildVoconed(ctx context.Context, dir string) (string, error) {
	bin := filepath.Join(dir, "voconed")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/vocdoni/vochain-sequencer/cmd/voconed")
	cmd.Dir = sequencerModuleDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("could not build voconed: %w: %s", err, out)
	}
	return bin, nil
}

// VoconedConfig configures a test chain.
type VoconedConfig struct {
	// Binary is the voconed binary, as returned by BuildVoconed.
	Binary string
	// ChainID is the chain id of the test chain.
	ChainID string
}

// Voconed is a vochain-sequencer voconed chain running as a subprocess, with
// VoconedFundedAccount funded and every transaction costing VoconedTxCosts.
type Voconed struct {
	Endpoint string
	cmd      *exec.Cmd
	dataDir  string
	// exited is closed once the subprocess has exited and been reaped.
	exited chan struct{}
}

// voconedStartAttempts bounds how many times StartVoconed launches the chain: freePort only
// reserves a port momentarily, so another process can take it before voconed binds it.
const voconedStartAttempts = 10

// StartVoconed starts a test chain and waits until it has produced its first block. A start that
// fails (for example because the port was taken) is retried on a fresh port. The caller must
// Close it.
func StartVoconed(cfg VoconedConfig) (*Voconed, error) {
	var err error
	for range voconedStartAttempts {
		var v *Voconed
		if v, err = startVoconedOnce(cfg); err == nil {
			return v, nil
		}
	}
	return nil, fmt.Errorf("voconed did not start after %d attempts: %w", voconedStartAttempts, err)
}

// startVoconedOnce launches voconed once on a free port and waits until it has produced its first
// block, stopping it and removing its data on failure.
func startVoconedOnce(cfg VoconedConfig) (*Voconed, error) {
	dataDir, err := os.MkdirTemp("", "voconed-test-*")
	if err != nil {
		return nil, fmt.Errorf("could not create voconed data dir: %w", err)
	}
	port, err := freePort()
	if err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, err
	}
	cmd := exec.Command(cfg.Binary,
		"--dir", dataDir,
		"--chainID", cfg.ChainID,
		"--port", strconv.Itoa(port),
		"--urlPath", VoconedURLPath,
		"--blockPeriod", "1",
		"--logLevel", "warn",
		"--fundedAccounts", fmt.Sprintf("%s:%d", VoconedFundedAccount, VoconedFunds),
		"--setTxCosts", "--txCosts", strconv.Itoa(VoconedTxCosts),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dataDir)
		return nil, fmt.Errorf("could not start voconed: %w", err)
	}
	v := &Voconed{
		Endpoint: fmt.Sprintf("http://127.0.0.1:%d%s", port, VoconedURLPath),
		cmd:      cmd,
		dataDir:  dataDir,
		exited:   make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(v.exited)
	}()
	if err := waitForVocone(v.Endpoint, cfg.ChainID, v.exited); err != nil {
		v.Close()
		return nil, err
	}
	return v, nil
}

// Close stops the chain and removes its data.
func (v *Voconed) Close() {
	_ = v.cmd.Process.Kill()
	<-v.exited
	_ = os.RemoveAll(v.dataDir)
}

// freePort returns an available local TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("could not find a free port: %w", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForVocone blocks until the API of chain chainID is reachable and has produced at least one
// block. It fails early when exited is closed: the chain process is gone.
func waitForVocone(endpoint, chainID string, exited <-chan struct{}) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return fmt.Errorf("voconed at %s exited before producing a block", endpoint)
		default:
		}
		if cli, err := apiclient.New(endpoint); err == nil && cli.ChainID() == chainID {
			if info, err := cli.ChainInfo(); err == nil && info.Height >= 1 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for voconed at %s", endpoint)
}
