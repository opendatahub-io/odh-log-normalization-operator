package multiline

import (
	"sync"
	"time"
)

// Consolidator decides whether a log line starts a new entry or continues the previous one.
type Consolidator interface {
	IsStart(line string) bool
	IsContinuation(line string) bool
}

// Reassembler buffers multiline log entries and flushes them as complete groups.
type Reassembler struct {
	buffer        []string
	flushFunc     func(lines []string)
	maxLines      int
	flushTimeout  time.Duration
	timer         *time.Timer
	mu            sync.Mutex
	consolidators []Consolidator
}

// NewReassembler creates a Reassembler that groups continuation lines together.
func NewReassembler(maxLines int, flushTimeout time.Duration, flushFunc func([]string), consolidators ...Consolidator) *Reassembler {
	r := &Reassembler{
		buffer:        make([]string, 0, maxLines),
		flushFunc:     flushFunc,
		maxLines:      maxLines,
		flushTimeout:  flushTimeout,
		consolidators: consolidators,
	}
	return r
}

// Add appends a line to the reassembler, flushing the previous buffer if this line starts a new entry.
func (r *Reassembler) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.isStartLine(line) && len(r.buffer) > 0 {
		r.flushLocked()
	}

	r.buffer = append(r.buffer, line)

	if len(r.buffer) >= r.maxLines {
		r.flushLocked()
		return
	}

	r.resetTimer()
}

// Flush sends the current buffer to flushFunc and resets state.
func (r *Reassembler) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushLocked()
}

// Close flushes remaining lines and stops the timer.
func (r *Reassembler) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.flushLocked()
}

func (r *Reassembler) flushLocked() {
	if len(r.buffer) == 0 {
		return
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	lines := r.buffer
	r.buffer = make([]string, 0, r.maxLines)
	r.flushFunc(lines)
}

func (r *Reassembler) resetTimer() {
	if r.timer != nil {
		r.timer.Stop()
	}
	r.timer = time.AfterFunc(r.flushTimeout, func() {
		r.Flush()
	})
}

func (r *Reassembler) isStartLine(line string) bool {
	for _, c := range r.consolidators {
		if c.IsContinuation(line) {
			return false
		}
		if c.IsStart(line) {
			return true
		}
	}
	return true
}
