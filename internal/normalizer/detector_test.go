package normalizer_test

import (
	"testing"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
)

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected normalizer.Format
	}{
		{
			name:     "JSON object",
			line:     `{"level":"info","msg":"server started","port":8080}`,
			expected: normalizer.FormatJSON,
		},
		{
			name:     "JSON with whitespace",
			line:     `  {"ts":"2024-01-01","message":"hello"}  `,
			expected: normalizer.FormatJSON,
		},
		{
			name:     "klog info",
			line:     "I0722 15:04:05.000000 12345 file.go:123] message here",
			expected: normalizer.FormatKlog,
		},
		{
			name:     "klog error",
			line:     "E0722 15:04:05.000000 12345 file.go:123] something failed",
			expected: normalizer.FormatKlog,
		},
		{
			name:     "klog warning",
			line:     "W0101 00:00:00.000000    1 main.go:42] warning message",
			expected: normalizer.FormatKlog,
		},
		{
			name:     "logfmt basic",
			line:     `level=info msg="hello world" ts=2024-01-01`,
			expected: normalizer.FormatLogfmt,
		},
		{
			name:     "logfmt without quotes",
			line:     `level=info caller=main.go:42 msg=starting`,
			expected: normalizer.FormatLogfmt,
		},
		{
			name:     "controller-runtime INFO with logger and json",
			line:     "2026-07-23T12:07:56Z\tINFO\tcontroller-runtime.metrics\tStarting metrics server\t{\"bindAddress\": \":8080\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "controller-runtime INFO with logger no json",
			line:     "2026-07-23T12:07:56Z\tINFO\tstarting server\t{\"name\": \"health probe\", \"addr\": \"[::]:8081\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "controller-runtime ERROR",
			line:     "2026-07-23T12:07:56Z\tERROR\tcontroller\tReconciler error\t{\"error\": \"not found\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "controller-runtime DEBUG",
			line:     "2026-07-23T12:07:56Z\tDEBUG\tevents\tleader elected",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "zap lowercase info (OTel collector)",
			line:     "2026-07-23T15:06:51.343Z\tinfo\tservice@v0.152.1/service.go:241\tStarting otelcol...\t{\"Version\": \"0.152.1\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "zap lowercase warn (OTel collector)",
			line:     "2026-07-23T15:06:51.340Z\twarn\tbuilders/builders.go:40\t\"k8sattributes\" alias is deprecated",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "zap lowercase error (OTel collector)",
			line:     "2026-07-23T15:06:51.382Z\terror\ttargetallocator/manager.go:130\tFailed to retrieve job list\t{\"error\": \"connection refused\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "zap lowercase debug",
			line:     "2026-07-23T15:06:51.365Z\tdebug\tkube/client.go:171\tk8s filtering\t{\"labelSelector\": \"\"}",
			expected: normalizer.FormatControllerRuntime,
		},
		{
			name:     "plain text",
			line:     "this is just a plain message",
			expected: normalizer.FormatPlain,
		},
		{
			name:     "plain text with single equals",
			line:     "Starting server on port=8080",
			expected: normalizer.FormatPlain,
		},
		{
			name:     "empty string",
			line:     "",
			expected: normalizer.FormatPlain,
		},
		{
			name:     "whitespace only",
			line:     "   \t  ",
			expected: normalizer.FormatPlain,
		},
		{
			name:     "JSON array is plain",
			line:     `[{"level":"info"}]`,
			expected: normalizer.FormatPlain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizer.DetectFormat(tt.line)
			if got != tt.expected {
				t.Errorf("DetectFormat(%q) = %q, want %q", tt.line, got, tt.expected)
			}
		})
	}
}

func TestIsOTLPFeedback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		line     string
		expected bool
	}{
		{
			name:     "debug exporter log output with spaces",
			line:     `2026-07-23T15:33:12.287Z	info	LogsExporter	{"otelcol.component.kind": "exporter", "otelcol.component.id": "debug", "otelcol.signal": "logs"}`,
			expected: true,
		},
		{
			name:     "debug exporter log output compact",
			line:     `2026-07-23T15:33:12.287Z	info	LogsExporter	{"otelcol.component.kind":"exporter","otelcol.component.id":"debug","otelcol.signal":"logs"}`,
			expected: true,
		},
		{
			name:     "exporter but traces signal",
			line:     `2026-07-23T15:33:12.287Z	info	TracesExporter	{"otelcol.component.kind": "exporter", "otelcol.signal": "traces"}`,
			expected: false,
		},
		{
			name:     "logs signal but receiver",
			line:     `2026-07-23T15:33:12.287Z	info	LogsReceiver	{"otelcol.component.kind": "receiver", "otelcol.signal": "logs"}`,
			expected: false,
		},
		{
			name:     "regular collector log",
			line:     `2026-07-23T15:33:12.287Z	info	service@v0.152.1/service.go:241	Starting otelcol...	{"Version": "0.152.1"}`,
			expected: false,
		},
		{
			name:     "plain text",
			line:     "just a regular log message",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizer.IsOTLPFeedback(tt.line)
			if got != tt.expected {
				t.Errorf("IsOTLPFeedback(%q) = %v, want %v", tt.line, got, tt.expected)
			}
		})
	}
}
