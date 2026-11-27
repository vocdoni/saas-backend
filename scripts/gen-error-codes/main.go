// Command gen-error-codes generates docs/api.md from the errors package source.
// Called by scripts/generate-swagger.sh before swag init so the error catalog
// is embedded in the swagger description.
//
//	Usage: go run ./scripts/gen-error-codes [--errors-dir errors] [--output docs/api.md]
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vocdoni/saas-backend/internal/errorcatalog"
)

func main() {
	errorsDir := flag.String("errors-dir", "errors", "path to the errors package directory")
	output := flag.String("output", "docs/api.md", "output file path")
	flag.Parse()

	entries, err := errorcatalog.ParseDir(*errorsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gen-error-codes: %v\n", err)
		os.Exit(1)
	}

	md := errorcatalog.RenderMarkdown(entries)

	if err := os.WriteFile(*output, []byte(md), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "gen-error-codes: writing %s: %v\n", *output, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d entries)\n", *output, len(entries))
}
