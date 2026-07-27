package multiline

import (
	"regexp"
	"strings"
)

var (
	goGoroutineRe = regexp.MustCompile(`^goroutine \d+ \[`)
	goFuncCallRe  = regexp.MustCompile(`^[a-zA-Z_][\w.*/]*\.[a-zA-Z_]\w*\(`)
)

// GoConsolidator detects Go panic/goroutine multiline blocks.
type GoConsolidator struct{}

func (GoConsolidator) IsStart(line string) bool {
	return strings.HasPrefix(line, "panic:")
}

func (GoConsolidator) IsContinuation(line string) bool {
	if len(line) == 0 {
		return false
	}
	if line[0] == '\t' || line[0] == ' ' {
		return true
	}
	if goGoroutineRe.MatchString(line) {
		return true
	}
	return goFuncCallRe.MatchString(line)
}

var javaLogLineRe = regexp.MustCompile(`^[0-9]{4}[-/]|^\d{2}:\d{2}:\d{2}|^(TRACE|DEBUG|INFO|WARN|ERROR|FATAL)\b`)

// JavaConsolidator detects Java exception stack traces.
type JavaConsolidator struct{}

func (JavaConsolidator) IsStart(line string) bool {
	if len(line) == 0 {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' {
		return false
	}
	return javaLogLineRe.MatchString(line)
}

func (JavaConsolidator) IsContinuation(line string) bool {
	if strings.HasPrefix(line, "\tat ") {
		return true
	}
	if strings.HasPrefix(line, "Caused by:") {
		return true
	}
	if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return true
	}
	return false
}

// PythonConsolidator detects Python tracebacks.
type PythonConsolidator struct{}

func (PythonConsolidator) IsStart(line string) bool {
	return strings.HasPrefix(line, "Traceback")
}

var pyExceptionRe = regexp.MustCompile(`^\w+(\.\w+)*(Error|Exception|Warning|Exit)`)

func (PythonConsolidator) IsContinuation(line string) bool {
	if len(line) == 0 {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "  File \"") {
		return true
	}
	return pyExceptionRe.MatchString(line)
}
