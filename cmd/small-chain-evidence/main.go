// Command small-chain-evidence validates a collected raw measurement bundle.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/w4llisz/small-chain-platform/internal/evidence"
)

func main() {
	dir := flag.String("dir", "artifacts/measurement", "measurement evidence directory")
	expectedCommit := flag.String("expected-commit", "", "full commit SHA the evidence must describe (required)")
	requireClean := flag.Bool("require-clean", true, "reject evidence collected from a dirty tracked tree")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *expectedCommit == "" {
		fmt.Fprintln(os.Stderr, "-expected-commit is required")
		os.Exit(2)
	}
	summary, err := evidence.ValidateWithPolicy(*dir, evidence.Policy{
		ExpectedCommitSHA: *expectedCommit,
		RequireCleanTree:  *requireClean,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "measurement evidence invalid: %v\n", err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(summary); err != nil {
		fmt.Fprintf(os.Stderr, "write summary: %v\n", err)
		os.Exit(1)
	}
}
