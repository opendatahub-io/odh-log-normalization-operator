package multiline_test

import (
	"testing"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer/multiline"
)

func TestGoConsolidator(t *testing.T) {
	c := multiline.GoConsolidator{}

	tests := []struct {
		name           string
		line           string
		expectStart    bool
		expectContinue bool
	}{
		{
			name:        "panic start",
			line:        "panic: runtime error: index out of range [3] with length 2",
			expectStart: true,
		},
		{
			name:           "goroutine header",
			line:           "goroutine 1 [running]:",
			expectContinue: true,
		},
		{
			name:           "tab-indented frame",
			line:           "\tmain.main()",
			expectContinue: true,
		},
		{
			name:           "file location",
			line:           "\t/home/user/app/main.go:42 +0x1a4",
			expectContinue: true,
		},
		{
			name:           "space-indented",
			line:           "  created by main.startServer",
			expectContinue: true,
		},
		{
			name:           "bare function call",
			line:           "main.main()",
			expectContinue: true,
		},
		{
			name: "regular log line",
			line: "2024-01-01 INFO server started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsStart(tt.line); got != tt.expectStart {
				t.Errorf("IsStart(%q) = %v, want %v", tt.line, got, tt.expectStart)
			}
			if got := c.IsContinuation(tt.line); got != tt.expectContinue {
				t.Errorf("IsContinuation(%q) = %v, want %v", tt.line, got, tt.expectContinue)
			}
		})
	}
}

func TestJavaConsolidator(t *testing.T) {
	c := multiline.JavaConsolidator{}

	tests := []struct {
		name           string
		line           string
		expectStart    bool
		expectContinue bool
	}{
		{
			name:        "timestamp log line",
			line:        "2024-01-15 10:30:45.123 ERROR com.example.App - NullPointerException",
			expectStart: true,
		},
		{
			name:        "level-prefixed line",
			line:        "ERROR com.example.service.UserService - Failed to process",
			expectStart: true,
		},
		{
			name:           "stack frame",
			line:           "\tat com.example.App.main(App.java:42)",
			expectContinue: true,
		},
		{
			name:           "caused by",
			line:           "Caused by: java.io.IOException: Connection refused",
			expectContinue: true,
		},
		{
			name:           "indented continuation",
			line:           "    at java.base/java.lang.Thread.run(Thread.java:829)",
			expectContinue: true,
		},
		{
			name: "plain text no match",
			line: "some unrelated plain text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsStart(tt.line); got != tt.expectStart {
				t.Errorf("IsStart(%q) = %v, want %v", tt.line, got, tt.expectStart)
			}
			if got := c.IsContinuation(tt.line); got != tt.expectContinue {
				t.Errorf("IsContinuation(%q) = %v, want %v", tt.line, got, tt.expectContinue)
			}
		})
	}
}

func TestPythonConsolidator(t *testing.T) {
	c := multiline.PythonConsolidator{}

	tests := []struct {
		name           string
		line           string
		expectStart    bool
		expectContinue bool
	}{
		{
			name:        "traceback header",
			line:        "Traceback (most recent call last):",
			expectStart: true,
		},
		{
			name:           "file reference",
			line:           `  File "/app/main.py", line 42, in <module>`,
			expectContinue: true,
		},
		{
			name:           "indented code",
			line:           "    result = process(data)",
			expectContinue: true,
		},
		{
			name:           "exception line at end",
			line:           "ValueError: invalid literal for int() with base 10: 'abc'",
			expectContinue: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.IsStart(tt.line); got != tt.expectStart {
				t.Errorf("IsStart(%q) = %v, want %v", tt.line, got, tt.expectStart)
			}
			if got := c.IsContinuation(tt.line); got != tt.expectContinue {
				t.Errorf("IsContinuation(%q) = %v, want %v", tt.line, got, tt.expectContinue)
			}
		})
	}
}
