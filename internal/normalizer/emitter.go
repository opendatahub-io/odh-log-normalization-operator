package normalizer

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// LogRecord is the normalized structured output for a single log entry.
type LogRecord struct {
	Timestamp    string         `json:"timestamp"`
	Level        string         `json:"level"`
	Message      string         `json:"message"`
	SourceFormat string         `json:"source_format"`
	TemplateID   int            `json:"template_id,omitempty"`
	TraceID      string         `json:"trace_id,omitempty"`
	SpanID       string         `json:"span_id,omitempty"`
	Resource     map[string]any `json:"resource,omitempty"`
	Structured   map[string]any `json:"structured,omitempty"`
}

// Emitter writes normalized log records as JSON lines to a writer.
type Emitter struct {
	writer  io.Writer
	encoder *json.Encoder
}

// NewEmitter creates an Emitter that writes JSON-encoded records to w.
func NewEmitter(w io.Writer) *Emitter {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &Emitter{
		writer:  w,
		encoder: enc,
	}
}

// Emit writes a single LogRecord as a JSON line.
func (e *Emitter) Emit(rec LogRecord) error {
	return e.encoder.Encode(rec)
}

// ParseJSON extracts a LogRecord from a JSON log line, mapping common field names.
func ParseJSON(line string) (LogRecord, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return LogRecord{}, fmt.Errorf("parsing JSON log: %w", err)
	}

	rec := LogRecord{
		SourceFormat: string(FormatJSON),
		Structured:   raw,
	}

	rec.Timestamp = extractStringField(raw, "timestamp", "ts", "time", "@timestamp")
	rec.Level = extractStringField(raw, "level", "lvl", "severity")
	rec.Message = extractStringField(raw, "msg", "message", "body")

	return rec, nil
}

// ParseKlog extracts a LogRecord from a klog-formatted line.
func ParseKlog(line string) LogRecord {
	rec := LogRecord{
		SourceFormat: string(FormatKlog),
	}

	if len(line) == 0 {
		return rec
	}

	switch line[0] {
	case 'I':
		rec.Level = "info"
	case 'W':
		rec.Level = "warning"
	case 'E':
		rec.Level = "error"
	case 'F':
		rec.Level = "fatal"
	}

	// klog format: Lmmdd HH:MM:SS.ffffff threadid file:line] msg
	if len(line) < 6 {
		rec.Message = line
		return rec
	}

	bracketIdx := strings.Index(line, "] ")
	if bracketIdx < 0 {
		rec.Message = line[1:]
		return rec
	}

	header := line[1:bracketIdx]
	rec.Message = line[bracketIdx+2:]

	// Extract timestamp: mmdd HH:MM:SS.ffffff
	parts := strings.SplitN(header, " ", 3)
	if len(parts) >= 2 {
		rec.Timestamp = parts[0] + " " + parts[1]
	}

	return rec
}

// ParseLogfmt extracts a LogRecord from a logfmt-formatted line.
func ParseLogfmt(line string) LogRecord {
	rec := LogRecord{
		SourceFormat: string(FormatLogfmt),
		Structured:   make(map[string]any),
	}

	pairs := parseLogfmtPairs(line)
	for k, v := range pairs {
		rec.Structured[k] = v
	}

	rec.Timestamp = firstValue(pairs, "timestamp", "ts", "time", "t")
	rec.Level = firstValue(pairs, "level", "lvl", "severity")
	rec.Message = firstValue(pairs, "msg", "message")

	return rec
}

// ParseControllerRuntime extracts a LogRecord from zap's console encoder
// format: TIMESTAMP\tLEVEL\t[LOGGER\t]MESSAGE[\t{json_fields}]
func ParseControllerRuntime(line string) LogRecord {
	rec := LogRecord{
		SourceFormat: string(FormatControllerRuntime),
	}

	fields := strings.Split(line, "\t")
	if len(fields) < 3 {
		rec.Message = line
		return rec
	}

	rec.Timestamp = fields[0]
	rec.Level = strings.ToLower(fields[1])

	switch len(fields) {
	case 3:
		rec.Message = fields[2]
	case 4:
		if strings.HasPrefix(strings.TrimSpace(fields[3]), "{") {
			rec.Message = fields[2]
			rec.Structured = parseInlineJSON(fields[3])
		} else {
			rec.Message = fields[3]
			rec.Structured = map[string]any{"logger": fields[2]}
		}
	default:
		last := fields[len(fields)-1]
		if strings.HasPrefix(strings.TrimSpace(last), "{") {
			rec.Message = fields[len(fields)-2]
			rec.Structured = parseInlineJSON(last)
			if len(fields) > 4 {
				if rec.Structured == nil {
					rec.Structured = make(map[string]any)
				}
				rec.Structured["logger"] = fields[2]
			}
		} else {
			rec.Message = fields[len(fields)-1]
			if len(fields) > 3 {
				rec.Structured = map[string]any{"logger": fields[2]}
			}
		}
	}

	return rec
}

// PromoteTraceFields looks for well-known trace/span ID field names in the
// Structured map and promotes them to the top-level TraceID/SpanID fields.
func PromoteTraceFields(rec *LogRecord) {
	if rec.Structured == nil {
		return
	}
	if rec.TraceID == "" {
		rec.TraceID = extractStringField(rec.Structured,
			"trace_id", "traceID", "traceId", "trace.id",
			"otel.trace_id", "dd.trace_id", "logging.googleapis.com/trace")
	}
	if rec.SpanID == "" {
		rec.SpanID = extractStringField(rec.Structured,
			"span_id", "spanID", "spanId", "span.id",
			"otel.span_id", "dd.span_id", "logging.googleapis.com/spanId")
	}
}

func parseInlineJSON(s string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) == nil {
		return m
	}
	return nil
}

func extractStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func firstValue(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return ""
}

func parseLogfmtPairs(line string) map[string]string {
	pairs := make(map[string]string)
	i := 0
	for i < len(line) {
		// Skip whitespace
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if i >= len(line) {
			break
		}

		// Read key
		keyStart := i
		for i < len(line) && line[i] != '=' && line[i] != ' ' {
			i++
		}
		if i >= len(line) || line[i] != '=' {
			break
		}
		key := line[keyStart:i]
		i++ // skip '='

		// Read value
		var value string
		if i < len(line) && line[i] == '"' {
			i++ // skip opening quote
			valStart := i
			for i < len(line) && line[i] != '"' {
				i++
			}
			value = line[valStart:i]
			if i < len(line) {
				i++ // skip closing quote
			}
		} else {
			valStart := i
			for i < len(line) && line[i] != ' ' {
				i++
			}
			value = line[valStart:i]
		}

		pairs[key] = value
	}
	return pairs
}
