package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestRunValidatesArgumentsBeforeConnecting(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "owner", want: "owner is required"},
		{name: "invalid owner", args: []string{"-owner=bad owner"}, want: "worker owner"},
		{name: "database URL", args: []string{"-owner=test-worker"}, want: "DATABASE_URL is required"},
		{name: "positionals", args: []string{"unexpected"}, want: "unexpected positional arguments"},
		{name: "unknown flag", args: []string{"-unknown"}, want: "flag provided but not defined"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := run(test.args, logger)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("run(%q) = %v, want error containing %q", test.args, err, test.want)
			}
		})
	}
}
