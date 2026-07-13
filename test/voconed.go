// Package test provides testing utilities for the saas-backend service,
// including test containers for mail and MongoDB, and an in-process Voconed chain.
package test

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/httprouter"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
	"go.vocdoni.io/dvote/vocone"
	"go.vocdoni.io/proto/build/go/models"
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

// Voconed is a running in-process Vocone chain for tests. It embeds *vocone.Vocone (so
// Close() stops it) and exposes the base API URL.
type Voconed struct {
	*vocone.Vocone
	Endpoint string
}

var (
	sharedVoconed     *Voconed
	sharedVoconedErr  error
	sharedVoconedOnce sync.Once
)

// SharedVoconed returns the process-wide in-process Vocone chain, starting it on first call
// and returning the same instance to every later caller. vocone registers global Prometheus
// metrics, so only one instance may exist per test binary; this enforces that. The instance
// lives for the lifetime of the test process (started with context.Background()); the OS
// reclaims it on exit, so callers must not Close() it.
func SharedVoconed() (*Voconed, error) {
	sharedVoconedOnce.Do(func() {
		sharedVoconed, sharedVoconedErr = startVoconed(context.Background())
	})
	return sharedVoconed, sharedVoconedErr
}

// startVoconed brings up an in-process Vocone chain built from the pinned dvote dependency
// (so it always matches the client, including the batch-transaction endpoint) and returns
// its base API URL together with the running instance. Callers go through SharedVoconed so
// exactly one instance runs per process; the chain stops when ctx is canceled. Unlike a
// pulled container image, this cannot drift from the version the code is compiled against.
func startVoconed(ctx context.Context) (*Voconed, error) {
	dataDir, err := os.MkdirTemp("", "voconed-test-*")
	if err != nil {
		return nil, fmt.Errorf("could not create voconed data dir: %w", err)
	}

	keymng := &ethereum.SignKeys{}
	if err := keymng.AddHexKey(VoconedFoundedPrivKey); err != nil {
		return nil, fmt.Errorf("could not load voconed key: %w", err)
	}

	// IPFS is disabled: it binds fixed ports (colliding with a local daemon or parallel
	// test binaries) and the SaaS never uses the chain's IPFS-backed census — it runs an
	// off-chain (CSP) census. We therefore also enable only the API handlers the SaaS uses
	// (see enableVoconeAPI); census/SIK need the IPFS census DB and are skipped.
	vc, err := vocone.NewVocone(dataDir, keymng, true, "", nil)
	if err != nil {
		return nil, fmt.Errorf("could not create vocone: %w", err)
	}
	vc.App.SetChainID("test-vocone")
	vc.App.SetBlockTimeTarget(time.Second)

	go func() {
		if err := vc.Start(ctx); err != nil && ctx.Err() == nil {
			log.Warnw("vocone stopped with error", "error", err)
		}
	}()

	// freePort only reserves a port momentarily, so another process could grab it before the API
	// binds. Retry with a fresh port on a bind collision (enableVoconeAPI builds a new router each
	// call, so a retry is clean) to avoid a flaky TOCTOU startup failure.
	var port int
	for range 10 {
		port, err = freePort()
		if err != nil {
			return nil, err
		}
		if err = enableVoconeAPI(vc, "127.0.0.1", port, VoconedURLPath); err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("could not enable vocone api: %w", err)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, VoconedURLPath)

	// wait until the chain has produced its first block before touching state
	if err := waitForVocone(endpoint); err != nil {
		return nil, err
	}

	if err := vc.SetBulkTxCosts(VoconedTxCosts, true); err != nil {
		return nil, fmt.Errorf("could not set tx costs: %w", err)
	}
	if err := vc.SetElectionPrice(); err != nil {
		return nil, fmt.Errorf("could not set election price: %w", err)
	}
	if err := vc.CreateAccount(common.HexToAddress(VoconedFundedAccount), &state.Account{
		Account: models.Account{Balance: VoconedFunds},
	}); err != nil {
		return nil, fmt.Errorf("could not fund voconed account: %w", err)
	}
	return &Voconed{Vocone: vc, Endpoint: endpoint}, nil
}

// enableVoconeAPI serves the Vocone API on host:port/urlPath with only the handlers the
// SaaS uses. It mirrors vocone.EnableAPI but omits the census and SIK handlers, which
// require the IPFS-backed census DB that is intentionally not initialized here.
func enableVoconeAPI(vc *vocone.Vocone, host string, port int, urlPath string) error {
	vc.Router = new(httprouter.HTTProuter)
	if err := vc.Router.Init(host, port); err != nil {
		return err
	}
	uAPI, err := api.NewAPI(vc.Router, urlPath, vc.Config.DataDir, db.TypePebble)
	if err != nil {
		return err
	}
	uAPI.Attach(vc.App, vc.Stats, vc.Indexer, vc.Storage, vc.CensusDB)
	return uAPI.EnableHandlers(
		api.ElectionHandler,
		api.VoteHandler,
		api.ChainHandler,
		api.WalletHandler,
		api.AccountHandler,
	)
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

// waitForVocone blocks until the Vocone API is reachable and has produced at least one block.
func waitForVocone(endpoint string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cli, err := apiclient.New(endpoint); err == nil {
			if info, err := cli.ChainInfo(); err == nil && info.Height >= 1 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for in-process vocone at %s", endpoint)
}
