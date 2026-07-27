package normalizer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/trace"
)

// OTLPEmitter sends normalized log records to an OTel Collector via OTLP gRPC.
type OTLPEmitter struct {
	provider *sdklog.LoggerProvider
	logger   otellog.Logger
}

// NewOTLPEmitter creates an OTLPEmitter that sends logs to the given gRPC endpoint.
// Resource attributes from the provided map are attached to all exported records.
func NewOTLPEmitter(ctx context.Context, endpoint string, res map[string]any) (*OTLPEmitter, error) {
	exp, err := otlploggrpc.New(ctx,
		otlploggrpc.WithEndpoint(endpoint),
		otlploggrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP log exporter: %w", err)
	}

	var attrs []attribute.KeyValue
	for k, v := range res {
		attrs = append(attrs, attribute.String(k, fmt.Sprint(v)))
	}

	provider := sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
		sdklog.WithResource(sdkresource.NewSchemaless(attrs...)),
	)

	return &OTLPEmitter{
		provider: provider,
		logger:   provider.Logger("log-normalizer"),
	}, nil
}

// Emit converts a LogRecord to an OTel log record and sends it via OTLP.
func (e *OTLPEmitter) Emit(rec LogRecord) {
	var r otellog.Record

	if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
		r.SetTimestamp(t)
	}
	r.SetObservedTimestamp(time.Now())
	r.SetSeverityText(rec.Level)
	r.SetSeverity(MapSeverity(rec.Level))
	r.SetBody(otellog.StringValue(rec.Message))

	var attrs []otellog.KeyValue
	attrs = append(attrs, otellog.String("source_format", rec.SourceFormat))
	if rec.TemplateID > 0 {
		attrs = append(attrs, otellog.Int("template_id", rec.TemplateID))
	}
	for k, v := range rec.Structured {
		attrs = append(attrs, structuredToAttr(k, v))
	}
	r.AddAttributes(attrs...)

	ctx := context.Background()
	if rec.TraceID != "" || rec.SpanID != "" {
		ctx = contextWithTraceIDs(ctx, rec.TraceID, rec.SpanID)
	}

	e.logger.Emit(ctx, r)
}

// Shutdown flushes pending records and releases resources.
func (e *OTLPEmitter) Shutdown(ctx context.Context) error {
	return e.provider.Shutdown(ctx)
}

func contextWithTraceIDs(ctx context.Context, traceID, spanID string) context.Context {
	cfg := trace.SpanContextConfig{TraceFlags: trace.FlagsSampled}
	if tid, err := trace.TraceIDFromHex(traceID); err == nil {
		cfg.TraceID = tid
	}
	if sid, err := trace.SpanIDFromHex(spanID); err == nil {
		cfg.SpanID = sid
	}
	return trace.ContextWithSpanContext(ctx, trace.NewSpanContext(cfg))
}

func structuredToAttr(k string, v any) otellog.KeyValue {
	switch val := v.(type) {
	case string:
		return otellog.String(k, val)
	case float64:
		return otellog.Float64(k, val)
	case bool:
		return otellog.Bool(k, val)
	default:
		return otellog.String(k, fmt.Sprint(val))
	}
}

// MapSeverity converts a log level string to an OTel severity.
func MapSeverity(level string) otellog.Severity {
	switch strings.ToLower(level) {
	case "debug", "trace":
		return otellog.SeverityDebug
	case "info":
		return otellog.SeverityInfo
	case "warning", "warn":
		return otellog.SeverityWarn
	case "error":
		return otellog.SeverityError
	case "fatal":
		return otellog.SeverityFatal
	default:
		return otellog.SeverityInfo
	}
}
