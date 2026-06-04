package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestValidateCommand(t *testing.T) {
	allowlist := []string{"echo", "ls", "python3"}

	tests := []struct {
		name        string
		command     string
		sandboxMode bool
		expectErr   bool
		errSubstr   string
	}{
		{
			name:        "allowlisted simple command",
			command:     "echo hello",
			sandboxMode: true,
			expectErr:   false,
		},
		{
			name:        "allowlisted command with path prefix",
			command:     "/usr/bin/echo hello",
			sandboxMode: true,
			expectErr:   false,
		},
		{
			name:        "disallowed simple command",
			command:     "rm -rf /",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "not in the allowed list",
		},
		{
			name:        "sandbox disabled allows anything",
			command:     "rm -rf /",
			sandboxMode: false,
			expectErr:   false,
		},
		{
			name:        "empty command",
			command:     "",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "empty command",
		},
		{
			name:        "chained command with semicolon",
			command:     "echo hello; rm -rf /",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "rm",
		},
		{
			name:        "chained command with double-ampersand",
			command:     "echo hello && rm -rf /",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "rm",
		},
		{
			name:        "chained command with pipe",
			command:     "echo hello | rm -rf /",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "rm",
		},
		{
			name:        "redirection output",
			command:     "echo hello > /tmp/malicious.txt",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "redirection operators",
		},
		{
			name:        "redirection input",
			command:     "cat < /etc/passwd",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "redirection operators",
		},
		{
			name:        "subshell execution dollar parenthesis",
			command:     "echo $(whoami)",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "subshell execution syntax",
		},
		{
			name:        "subshell execution backticks",
			command:     "echo `whoami`",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "subshell execution syntax",
		},
		{
			name:        "contains null byte",
			command:     "echo\x00hello",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "contains null byte",
		},
		{
			name:        "contains control character",
			command:     "echo \x01 hello",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "illegal control character",
		},
		{
			name:        "contains unicode bidirectional override",
			command:     "echo \u202E hello",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "Unicode bidirectional control character",
		},
		{
			name:        "contains private-use unicode",
			command:     "echo \uE000 hello",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "private-use Unicode character",
		},
		{
			name:        "process substitution",
			command:     "echo <(ls)",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "redirection operators",
		},
		{
			name:        "path traversal dot-dot in args",
			command:     "ls ../../../../etc",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "path traversal attempt",
		},
		{
			name:        "path traversal bare dot-dot in args",
			command:     "ls ..",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "path traversal attempt",
		},
		{
			name:        "unpermitted absolute path",
			command:     "ls /etc/shadow",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "absolute path",
		},
		{
			name:        "permitted absolute path",
			command:     "ls /dev/null",
			sandboxMode: true,
			expectErr:   false,
		},
		{
			name:        "exceeds argument count cap",
			command:     "echo 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60 61 62 63 64 65",
			sandboxMode: true,
			expectErr:   true,
			errSubstr:   "exceeds maximum argument count",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCommand(tc.command, allowlist, tc.sandboxMode)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("expected error containing %q, got %v", tc.errSubstr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

func TestWorkerSandboxAndTimeout(t *testing.T) {
	log := zap.NewNop()
	worker := NewWorker(log)
	allowlist := []string{"echo", "sleep"}

	t.Run("successful run within allowlist", func(t *testing.T) {
		output, err := worker.Run(context.Background(), "echo 'hello openfabric'", nil, true, allowlist, 5*time.Second)
		if err != nil {
			t.Fatalf("expected command to succeed, got %v", err)
		}
		if !strings.Contains(output, "hello openfabric") {
			t.Errorf("expected output to contain 'hello openfabric', got %q", output)
		}
	})

	t.Run("enforces execution timeout", func(t *testing.T) {
		start := time.Now()
		_, err := worker.Run(context.Background(), "sleep 10", nil, true, allowlist, 100*time.Millisecond)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("expected command to time out and return error, got nil")
		}
		if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "failed") {
			t.Errorf("expected timeout error message, got %v", err)
		}
		if elapsed >= 2*time.Second {
			t.Errorf("expected command to be aborted quickly, but took %v", elapsed)
		}
	})
}

func TestValidateEnv(t *testing.T) {
	tests := []struct {
		name        string
		env         []string
		sandboxMode bool
		expectErr   bool
	}{
		{
			name:        "safe environment",
			env:         []string{"FOO=bar", "BAZ=qux"},
			sandboxMode: true,
			expectErr:   false,
		},
		{
			name:        "invalid format",
			env:         []string{"FOO"},
			sandboxMode: true,
			expectErr:   true,
		},
		{
			name:        "dangerous variable LD_PRELOAD",
			env:         []string{"LD_PRELOAD=/tmp/evil.so"},
			sandboxMode: true,
			expectErr:   true,
		},
		{
			name:        "dangerous variable PATH",
			env:         []string{"PATH=/usr/bin"},
			sandboxMode: true,
			expectErr:   true,
		},
		{
			name:        "illegal characters in key",
			env:         []string{"FO;O=bar"},
			sandboxMode: true,
			expectErr:   true,
		},
		{
			name:        "sandbox disabled permits anything",
			env:         []string{"LD_PRELOAD=/tmp/evil.so", "FOO"},
			sandboxMode: false,
			expectErr:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEnv(tc.env, tc.sandboxMode)
			if tc.expectErr && err == nil {
				t.Fatalf("expected error but got nil")
			}
			if !tc.expectErr && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
		})
	}
}
