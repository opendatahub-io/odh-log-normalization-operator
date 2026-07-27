package normalizer_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
)

func BenchmarkDetectFormat_JSON(b *testing.B) {
	line := `{"level":"info","msg":"request completed","latency_ms":42,"status":200}`
	b.ResetTimer()
	for b.Loop() {
		normalizer.DetectFormat(line)
	}
}

func BenchmarkDetectFormat_Klog(b *testing.B) {
	line := "I0722 15:04:05.000000 12345 controller.go:123] reconcile complete"
	b.ResetTimer()
	for b.Loop() {
		normalizer.DetectFormat(line)
	}
}

func BenchmarkDetectFormat_Logfmt(b *testing.B) {
	line := `level=info msg="request completed" latency=42ms status=200`
	b.ResetTimer()
	for b.Loop() {
		normalizer.DetectFormat(line)
	}
}

func BenchmarkDetectFormat_Plain(b *testing.B) {
	line := "this is a plain text log line with no structure"
	b.ResetTimer()
	for b.Loop() {
		normalizer.DetectFormat(line)
	}
}

func BenchmarkDrainProcess(b *testing.B) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.4,
		MaxClusters:         1000,
	})

	// Pre-seed with templates
	for i := 0; i < 50; i++ {
		miner.Process(fmt.Sprintf("user %d logged in from 10.0.%d.%d", i, i%256, (i*7)%256))
	}

	line := "user 99999 logged in from 10.0.1.1"
	b.ResetTimer()
	for b.Loop() {
		miner.Process(line)
	}
}

func BenchmarkDrainMatch(b *testing.B) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.4,
		MaxClusters:         1000,
	})

	for i := 0; i < 50; i++ {
		miner.Process(fmt.Sprintf("request completed in %dms status %d", i*10, 200+i%5))
	}

	line := "request completed in 500ms status 200"
	b.ResetTimer()
	for b.Loop() {
		miner.Match(line)
	}
}

func BenchmarkParseJSON(b *testing.B) {
	line := `{"level":"info","msg":"request completed","ts":"2024-01-01T00:00:00Z","latency_ms":42}`
	b.ResetTimer()
	for b.Loop() {
		_, _ = normalizer.ParseJSON(line)
	}
}

func BenchmarkParseKlog(b *testing.B) {
	line := "I0722 15:04:05.000000 12345 controller.go:123] reconcile complete for resource default/myresource"
	b.ResetTimer()
	for b.Loop() {
		normalizer.ParseKlog(line)
	}
}

func BenchmarkParseLogfmt(b *testing.B) {
	line := `level=info msg="request completed" latency=42ms status=200 caller=handler.go:55`
	b.ResetTimer()
	for b.Loop() {
		normalizer.ParseLogfmt(line)
	}
}

func BenchmarkEmit(b *testing.B) {
	buf := &bytes.Buffer{}
	emitter := normalizer.NewEmitter(buf)
	rec := normalizer.LogRecord{
		Timestamp:    "2024-01-01T00:00:00Z",
		Level:        "info",
		Message:      "request completed",
		SourceFormat: "json",
		TemplateID:   1,
	}
	b.ResetTimer()
	for b.Loop() {
		buf.Reset()
		_ = emitter.Emit(rec)
	}
}

func BenchmarkFullPipeline(b *testing.B) {
	buf := &bytes.Buffer{}
	emitter := normalizer.NewEmitter(buf)
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.4,
		MaxClusters:         1000,
	})

	lines := []string{
		`{"level":"info","msg":"request completed","latency_ms":42}`,
		"I0722 15:04:05.000000 12345 controller.go:123] reconcile complete",
		`level=info msg="server started" port=8080`,
		"plain text log line from some dependency",
	}

	b.ResetTimer()
	for b.Loop() {
		line := lines[b.N%len(lines)]
		format := normalizer.DetectFormat(line)
		var rec normalizer.LogRecord
		switch format {
		case normalizer.FormatJSON:
			rec, _ = normalizer.ParseJSON(line)
		case normalizer.FormatKlog:
			rec = normalizer.ParseKlog(line)
		case normalizer.FormatLogfmt:
			rec = normalizer.ParseLogfmt(line)
		default:
			rec = normalizer.LogRecord{Message: line, SourceFormat: "plain"}
		}
		if cluster := miner.Process(line); cluster != nil {
			rec.TemplateID = cluster.ID
		}
		buf.Reset()
		_ = emitter.Emit(rec)
	}
}
