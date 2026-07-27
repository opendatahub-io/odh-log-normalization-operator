package normalizer_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
)

func TestPromoteTraceFields_JSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		line            string
		expectedTraceID string
		expectedSpanID  string
	}{
		{
			name:            "otel standard fields",
			line:            `{"level":"info","msg":"request","trace_id":"abc123","span_id":"def456"}`,
			expectedTraceID: "abc123",
			expectedSpanID:  "def456",
		},
		{
			name:            "camelCase fields",
			line:            `{"level":"info","msg":"request","traceID":"trace-1","spanID":"span-1"}`,
			expectedTraceID: "trace-1",
			expectedSpanID:  "span-1",
		},
		{
			name:            "otel prefixed fields",
			line:            `{"level":"info","msg":"request","otel.trace_id":"ot-1","otel.span_id":"os-1"}`,
			expectedTraceID: "ot-1",
			expectedSpanID:  "os-1",
		},
		{
			name:            "datadog fields",
			line:            `{"level":"info","msg":"request","dd.trace_id":"dd-1","dd.span_id":"dd-2"}`,
			expectedTraceID: "dd-1",
			expectedSpanID:  "dd-2",
		},
		{
			name:            "no trace fields",
			line:            `{"level":"info","msg":"no trace context here"}`,
			expectedTraceID: "",
			expectedSpanID:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec, err := normalizer.ParseJSON(tt.line)
			require.NoError(t, err)
			normalizer.PromoteTraceFields(&rec)
			assert.Equal(t, tt.expectedTraceID, rec.TraceID)
			assert.Equal(t, tt.expectedSpanID, rec.SpanID)
		})
	}
}

func TestPromoteTraceFields_Logfmt(t *testing.T) {
	t.Parallel()

	line := `level=info msg="handling request" trace_id=abc123 span_id=def456`
	rec := normalizer.ParseLogfmt(line)
	normalizer.PromoteTraceFields(&rec)

	assert.Equal(t, "abc123", rec.TraceID)
	assert.Equal(t, "def456", rec.SpanID)
}

func TestPromoteTraceFields_NilStructured(t *testing.T) {
	t.Parallel()

	rec := normalizer.LogRecord{Message: "plain text"}
	normalizer.PromoteTraceFields(&rec)
	assert.Empty(t, rec.TraceID)
	assert.Empty(t, rec.SpanID)
}

func TestLogRecord_ResourceSerialization(t *testing.T) {
	t.Parallel()

	rec := normalizer.LogRecord{
		Timestamp:    "2026-07-23T12:00:00Z",
		Level:        "info",
		Message:      "test",
		SourceFormat: "json",
		Resource: map[string]any{
			"k8s.pod.name":       "my-pod",
			"k8s.namespace.name": "default",
		},
	}

	var buf bytes.Buffer
	emitter := normalizer.NewEmitter(&buf)
	require.NoError(t, emitter.Emit(rec))

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	resource, ok := out["resource"].(map[string]any)
	require.True(t, ok, "resource field should be a map")
	assert.Equal(t, "my-pod", resource["k8s.pod.name"])
	assert.Equal(t, "default", resource["k8s.namespace.name"])
}

func TestLogRecord_TraceFieldsSerialization(t *testing.T) {
	t.Parallel()

	rec := normalizer.LogRecord{
		Timestamp:    "2026-07-23T12:00:00Z",
		Level:        "info",
		Message:      "test",
		SourceFormat: "json",
		TraceID:      "abc123",
		SpanID:       "def456",
	}

	var buf bytes.Buffer
	emitter := normalizer.NewEmitter(&buf)
	require.NoError(t, emitter.Emit(rec))

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	assert.Equal(t, "abc123", out["trace_id"])
	assert.Equal(t, "def456", out["span_id"])
}

func TestParseControllerRuntime_LowercaseLevels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		line          string
		expectedLevel string
		expectedMsg   string
		hasStructured bool
	}{
		{
			name:          "lowercase info with logger and json",
			line:          "2026-07-23T15:06:51.343Z\tinfo\tservice@v0.152.1/service.go:241\tStarting otelcol...\t{\"Version\": \"0.152.1\"}",
			expectedLevel: "info",
			expectedMsg:   "Starting otelcol...",
			hasStructured: true,
		},
		{
			name:          "lowercase warn with logger",
			line:          "2026-07-23T15:06:51.340Z\twarn\tbuilders/builders.go:40\talias is deprecated",
			expectedLevel: "warn",
			expectedMsg:   "alias is deprecated",
		},
		{
			name:          "lowercase error with logger and json",
			line:          "2026-07-23T15:06:51.382Z\terror\tmanager.go:130\tFailed to retrieve\t{\"error\": \"refused\"}",
			expectedLevel: "error",
			expectedMsg:   "Failed to retrieve",
			hasStructured: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := normalizer.ParseControllerRuntime(tt.line)
			assert.Equal(t, tt.expectedLevel, rec.Level)
			assert.Equal(t, tt.expectedMsg, rec.Message)
			assert.Equal(t, "controller-runtime", rec.SourceFormat)
			if tt.hasStructured {
				assert.NotNil(t, rec.Structured)
			}
		})
	}
}

func TestLogRecord_OmitsEmptyTraceFields(t *testing.T) {
	t.Parallel()

	rec := normalizer.LogRecord{
		Timestamp:    "2026-07-23T12:00:00Z",
		Level:        "info",
		Message:      "no trace",
		SourceFormat: "plain",
	}

	var buf bytes.Buffer
	emitter := normalizer.NewEmitter(&buf)
	require.NoError(t, emitter.Emit(rec))

	var out map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &out))

	_, hasTraceID := out["trace_id"]
	_, hasSpanID := out["span_id"]
	_, hasResource := out["resource"]
	assert.False(t, hasTraceID, "trace_id should be omitted when empty")
	assert.False(t, hasSpanID, "span_id should be omitted when empty")
	assert.False(t, hasResource, "resource should be omitted when nil")
}
