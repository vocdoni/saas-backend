// main package in scripts/circuits/main.go is a simple script to download
// the assets of the zk circuit. It is used during the docker image build to
// include those assets inside the final image and prevent them from being
// downloaded at runtime.
package main

import (
	"fmt"
	"os"
	"time"

	"go.vocdoni.io/dvote/crypto/zk/circuit"
)

func main() {
	// The assets are fetched over the network (raw.githubusercontent.com), which occasionally
	// returns transient errors (e.g. HTTP 503) that would otherwise abort the whole docker build.
	// Retry with a linear backoff and only fail once every attempt has been exhausted.
	const attempts = 5
	var err error
	for i := range attempts {
		if _, err = circuit.LoadVersion(circuit.DefaultZkCircuitVersion); err == nil {
			return // assets fetched (and cached under ~/.cache/vocdoni/zkCircuits)
		}
		fmt.Fprintf(os.Stderr, "attempt %d/%d: failed to prefetch zk circuits (version=%v): %v\n",
			i+1, attempts, circuit.DefaultZkCircuitVersion, err)
		if i < attempts-1 {
			time.Sleep(time.Duration(i+1) * 5 * time.Second) // 5s, 10s, 15s, 20s
		}
	}
	fmt.Fprintf(os.Stderr, "giving up prefetching zk circuits after %d attempts: %v\n", attempts, err)
	os.Exit(1)
}
