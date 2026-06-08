// main package in scripts/circuits/main.go is a simple script to download
// the assets of the zk circuit. It is used during the docker image build to
// include those assets inside the final image and prevent them from being
// downloaded at runtime.
package main

import (
	"fmt"
	"os"

	"go.vocdoni.io/dvote/crypto/zk/circuit"
)

func main() {
	if _, err := circuit.LoadVersion(circuit.DefaultZkCircuitVersion); err != nil {
		fmt.Fprintf(os.Stderr, "failed to prefetch zk circuits (version=%v): %v\n", circuit.DefaultZkCircuitVersion, err)
		os.Exit(1)
	}
}
