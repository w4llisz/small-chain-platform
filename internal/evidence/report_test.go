package evidence

import (
	"strings"
	"testing"
)

func TestRenderMarkdownIsDeterministicAndDescriptive(t *testing.T) {
	dir := writeFixture(t)
	policy := Policy{ExpectedCommitSHA: strings.Repeat("a", 40), RequireCleanTree: true}

	first, err := RenderMarkdown(dir, policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderMarkdown(dir, policy)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("report output changed without changing its evidence")
	}
	for _, expected := range []string{
		"# Small Chain measurement evidence",
		"| Commit | " + strings.Repeat("a", 40) + " |",
		"| Admission concurrency | 1, 4 |",
		"| 1 | 1 | 3 | 3 | 11.000 | 1.000 | 2.000 | 3.000 |",
		"| 4 | 2 | 3 | 3 | 42.000 | 1.000 | 2.000 | 3.000 |",
		"| Durable succeeded jobs | 12 |",
		"| Worker attempt count snapshot | 12 |",
		"- Shared runner.",
	} {
		if !strings.Contains(first, expected) {
			t.Fatalf("report does not contain %q:\n%s", expected, first)
		}
	}
}
