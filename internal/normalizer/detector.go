package normalizer

import "strings"

// Format represents a detected log line format.
type Format string

const (
	FormatJSON              Format = "json"
	FormatLogfmt            Format = "logfmt"
	FormatKlog              Format = "klog"
	FormatControllerRuntime Format = "controller-runtime"
	FormatPlain             Format = "plain"
)

// DetectFormat identifies the format of a log line using fast byte/string checks.
func DetectFormat(line string) Format {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) == 0 {
		return FormatPlain
	}

	if trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' {
		return FormatJSON
	}

	if isKlog(trimmed) {
		return FormatKlog
	}

	if isControllerRuntime(trimmed) {
		return FormatControllerRuntime
	}

	if isLogfmt(trimmed) {
		return FormatLogfmt
	}

	return FormatPlain
}

func isKlog(line string) bool {
	if len(line) < 5 {
		return false
	}
	first := line[0]
	if first != 'I' && first != 'W' && first != 'E' && first != 'F' {
		return false
	}
	for i := 1; i < 5; i++ {
		if line[i] < '0' || line[i] > '9' {
			return false
		}
	}
	return true
}

// isControllerRuntime detects zap's console encoder format used by
// controller-runtime and OTel Collector: TIMESTAMP\tLEVEL\t[LOGGER\t]MESSAGE[\t{json}]
// Accepts both uppercase (INFO) and lowercase (info) levels.
func isControllerRuntime(line string) bool {
	firstTab := strings.IndexByte(line, '\t')
	if firstTab < 10 {
		return false
	}
	secondTab := strings.IndexByte(line[firstTab+1:], '\t')
	if secondTab < 0 {
		return false
	}
	level := strings.ToUpper(line[firstTab+1 : firstTab+1+secondTab])
	return level == "INFO" || level == "WARN" || level == "ERROR" ||
		level == "DEBUG" || level == "DPANIC" || level == "FATAL"
}

// IsOTLPFeedback returns true if the line is output from the OTel Collector's
// debug exporter logging received log telemetry. Filtering these prevents a
// feedback loop when the normalizer runs inside a collector pod.
func IsOTLPFeedback(line string) bool {
	return containsAny(line, "\"otelcol.component.kind\": \"exporter\"", "\"otelcol.component.kind\":\"exporter\"") &&
		containsAny(line, "\"otelcol.signal\": \"logs\"", "\"otelcol.signal\":\"logs\"")
}

func containsAny(s string, variants ...string) bool {
	for _, v := range variants {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

func isLogfmt(line string) bool {
	count := 0
	i := 0
	for i < len(line) {
		eqIdx := strings.IndexByte(line[i:], '=')
		if eqIdx < 0 {
			break
		}
		eqPos := i + eqIdx
		if eqPos > i && line[eqPos-1] != ' ' && eqPos+1 < len(line) {
			count++
			if count >= 2 {
				return true
			}
		}
		i = eqPos + 1
	}
	return false
}
