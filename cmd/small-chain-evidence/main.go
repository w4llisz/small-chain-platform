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
	format := flag.String("format", "json", "output format: json or markdown")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *expectedCommit == "" {
		fmt.Fprintln(os.Stderr, "-expected-commit is required")
		os.Exit(2)
	}
	policy := evidence.Policy{
		ExpectedCommitSHA: *expectedCommit,
		RequireCleanTree:  *requireClean,
	}
	if *format != "json" && *format != "markdown" {
		fmt.Fprintln(os.Stderr, "-format must be json or markdown")
		os.Exit(2)
	}
	if *format == "markdown" {
		report, err := evidence.RenderMarkdown(*dir, policy)
		if err != nil {
			fmt.Fprintf(os.Stderr, "measurement evidence invalid: %v\n", err)
			os.Exit(1)
		}
		if _, err := fmt.Fprint(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		return
	}
	summary, err := evidence.ValidateWithPolicy(*dir, policy)
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
