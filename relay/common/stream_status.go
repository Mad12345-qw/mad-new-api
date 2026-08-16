package common

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type StreamEndReason string

const (
	StreamEndReasonNone        StreamEndReason = ""
	StreamEndReasonDone        StreamEndReason = "done"
	StreamEndReasonTimeout     StreamEndReason = "timeout"
	StreamEndReasonClientGone  StreamEndReason = "client_gone"
	StreamEndReasonScannerErr  StreamEndReason = "scanner_error"
	StreamEndReasonHandlerStop StreamEndReason = "handler_stop"
	StreamEndReasonEOF         StreamEndReason = "eof"
	StreamEndReasonPanic       StreamEndReason = "panic"
	StreamEndReasonPingFail    StreamEndReason = "ping_fail"
)

const maxStreamErrorEntries = 20

type StreamErrorEntry struct {
	Message   string
	Timestamp time.Time
}

type StreamStatus struct {
	EndReason StreamEndReason
	EndError  error
	endOnce   sync.Once

	mu                     sync.Mutex
	Errors                 []StreamErrorEntry
	ErrorCount             int
	requireTerminalEvent   bool
	responsesTerminalEvent string
}

func NewStreamStatus() *StreamStatus {
	return &StreamStatus{}
}

func (s *StreamStatus) SetEndReason(reason StreamEndReason, err error) {
	if s == nil {
		return
	}
	s.endOnce.Do(func() {
		s.EndReason = reason
		s.EndError = err
	})
}

func (s *StreamStatus) RecordError(msg string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ErrorCount++
	if len(s.Errors) < maxStreamErrorEntries {
		s.Errors = append(s.Errors, StreamErrorEntry{
			Message:   msg,
			Timestamp: time.Now(),
		})
	}
}

func (s *StreamStatus) HasErrors() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount > 0
}

func (s *StreamStatus) TotalErrorCount() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ErrorCount
}

func (s *StreamStatus) SetResponsesTerminalRequirement(required bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.requireTerminalEvent = required
	s.mu.Unlock()
}

func (s *StreamStatus) MarkResponsesTerminalEvent(eventType string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.responsesTerminalEvent == "" {
		s.responsesTerminalEvent = eventType
	}
	s.mu.Unlock()
}

func (s *StreamStatus) ResponsesTerminalEvent() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.responsesTerminalEvent
}

func (s *StreamStatus) RequiresResponsesTerminalEvent() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requireTerminalEvent
}

func (s *StreamStatus) IsNormalEnd() bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	requireTerminalEvent := s.requireTerminalEvent
	terminalEvent := s.responsesTerminalEvent
	s.mu.Unlock()
	if requireTerminalEvent && terminalEvent != "response.completed" {
		return false
	}
	return s.EndReason == StreamEndReasonDone ||
		s.EndReason == StreamEndReasonEOF ||
		s.EndReason == StreamEndReasonHandlerStop
}

func (s *StreamStatus) Summary() string {
	if s == nil {
		return "StreamStatus<nil>"
	}
	b := &strings.Builder{}
	fmt.Fprintf(b, "reason=%s", s.EndReason)
	if s.EndError != nil {
		fmt.Fprintf(b, " end_error=%q", s.EndError.Error())
	}
	s.mu.Lock()
	if s.ErrorCount > 0 {
		fmt.Fprintf(b, " soft_errors=%d", s.ErrorCount)
	}
	if s.requireTerminalEvent {
		if s.responsesTerminalEvent == "" {
			fmt.Fprint(b, " terminal_event=missing")
		} else {
			fmt.Fprintf(b, " terminal_event=%s", s.responsesTerminalEvent)
		}
	}
	s.mu.Unlock()
	return b.String()
}
