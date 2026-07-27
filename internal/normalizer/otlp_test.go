package normalizer_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
)

func TestMapSeverity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		expected string
	}{
		{"debug", "DEBUG"},
		{"trace", "DEBUG"},
		{"info", "INFO"},
		{"warning", "WARN"},
		{"warn", "WARN"},
		{"error", "ERROR"},
		{"fatal", "FATAL"},
		{"unknown", "INFO"},
		{"", "INFO"},
		{"ERROR", "ERROR"},
		{"Warning", "WARN"},
		{"INFO", "INFO"},
		{"Debug", "DEBUG"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			sev := normalizer.MapSeverity(tt.input)
			assert.Equal(t, tt.expected, sev.String())
		})
	}
}
