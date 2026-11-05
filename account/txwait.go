package account

import (
	"context"
	"fmt"
	"time"

	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
	dvotetypes "go.vocdoni.io/dvote/types"
)

// TxPollInterval is how often waitTxMined asks the chain whether a transaction is in a block, and
// twice how long it lets the block settle once it is. It defaults to apiclient.PollInterval, which
// is a const, so that production waits exactly as apiclient.WaitUntilTxIsMined does; test suites
// shorten it against their fast test chain. Do not change it in production code.
var TxPollInterval = apiclient.PollInterval

// waitTxMined blocks until the transaction with the given hash is included in a block or ctx ends.
// It is apiclient.WaitUntilTxIsMined with TxPollInterval in place of the const poll interval: poll
// every TxPollInterval, then sleep TxPollInterval/2 so the block is committed before the caller
// reads state (the indexer answers a tx reference slightly before the state tree is saved).
func waitTxMined(ctx context.Context, cli *apiclient.HTTPclient, hash dvotetypes.HexBytes) error {
	start := time.Now()
	for {
		if _, err := cli.TransactionReference(hash); err == nil {
			time.Sleep(TxPollInterval / 2)
			log.Infow("transaction mined", "tx", hash.String(), "duration", time.Since(start).String())
			return nil
		}
		select {
		case <-time.After(TxPollInterval):
		case <-ctx.Done():
			return fmt.Errorf("transaction %s never mined after %s: %w", hash.String(), time.Since(start), ctx.Err())
		}
	}
}
