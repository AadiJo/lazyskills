package runner

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// StreamOptions controls non-interactive web command execution.
type StreamOptions struct {
	Timeout      time.Duration
	StallTimeout time.Duration
}

// StreamEvent is one sanitized output chunk from a running command.
type StreamEvent struct {
	Stream string `json:"stream"`
	Data   string `json:"data"`
}

type streamWriter struct {
	mu       sync.Mutex
	buffer   cappedBuffer
	sanitize incrementalSanitizer
	stream   string
	emit     func(StreamEvent)
	activity chan<- struct{}
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.buffer.Write(p)
	chunk := w.sanitize.Write(p)
	if w.emit != nil && chunk != "" {
		w.emit(StreamEvent{Stream: w.stream, Data: chunk})
	}
	w.mu.Unlock()
	select {
	case w.activity <- struct{}{}:
	default:
	}
	return len(p), nil
}

type sanitizerState uint8

const (
	sanitizeText sanitizerState = iota
	sanitizeEscape
	sanitizeEscapeIntermediate
	sanitizeCSI
	sanitizeOSC
	sanitizeOSCEscape
	sanitizeString
	sanitizeStringEscape
)

// incrementalSanitizer strips terminal control sequences without assuming
// that Write boundaries align with UTF-8 runes or ANSI escape sequences.
type incrementalSanitizer struct {
	state               sanitizerState
	pending             []byte
	stringUTF8Lead      byte
	stringUTF8Remaining int
}

func (s *incrementalSanitizer) Write(p []byte) string {
	data := make([]byte, 0, len(s.pending)+len(p))
	data = append(data, s.pending...)
	data = append(data, p...)
	s.pending = s.pending[:0]

	var output strings.Builder
	for index := 0; index < len(data); {
		value := data[index]
		if s.state != sanitizeText {
			s.consumeSequenceByte(value)
			index++
			continue
		}
		switch value {
		case 0x1b:
			s.state = sanitizeEscape
			index++
			continue
		case 0x9b:
			s.state = sanitizeCSI
			index++
			continue
		case 0x9d:
			s.state = sanitizeOSC
			index++
			continue
		case 0x90, 0x98, 0x9e, 0x9f:
			s.state = sanitizeString
			index++
			continue
		}
		if value < utf8.RuneSelf {
			if value == '\n' || value == '\t' || value >= 0x20 && value != 0x7f {
				output.WriteByte(value)
			}
			index++
			continue
		}
		if !utf8.FullRune(data[index:]) {
			s.pending = append(s.pending, data[index:]...)
			break
		}
		r, size := utf8.DecodeRune(data[index:])
		index += size
		if r == utf8.RuneError && size == 1 {
			continue
		}
		switch r {
		case 0x9b:
			s.state = sanitizeCSI
		case 0x9d:
			s.state = sanitizeOSC
		case 0x90, 0x98, 0x9e, 0x9f:
			s.state = sanitizeString
		default:
			if r >= 0xa0 {
				output.WriteRune(r)
			}
		}
	}
	return output.String()
}

func (s *incrementalSanitizer) consumeSequenceByte(value byte) {
	switch s.state {
	case sanitizeEscape:
		switch value {
		case '[':
			s.state = sanitizeCSI
		case ']':
			s.state = sanitizeOSC
		case 'P', 'X', '^', '_':
			s.state = sanitizeString
		default:
			if value >= 0x20 && value <= 0x2f {
				s.state = sanitizeEscapeIntermediate
			} else {
				s.state = sanitizeText
			}
		}
	case sanitizeEscapeIntermediate:
		if value >= 0x30 && value <= 0x7e {
			s.state = sanitizeText
		}
	case sanitizeCSI:
		if value >= 0x40 && value <= 0x7e {
			s.state = sanitizeText
		}
	case sanitizeOSC:
		if s.consumeStringTerminator(value) || value == 0x07 {
			s.resetStringUTF8()
			s.state = sanitizeText
		} else if value == 0x1b {
			s.resetStringUTF8()
			s.state = sanitizeOSCEscape
		}
	case sanitizeOSCEscape:
		if value == '\\' {
			s.state = sanitizeText
		} else if value != 0x1b {
			s.state = sanitizeOSC
		}
	case sanitizeString:
		if s.consumeStringTerminator(value) {
			s.resetStringUTF8()
			s.state = sanitizeText
		} else if value == 0x1b {
			s.resetStringUTF8()
			s.state = sanitizeStringEscape
		}
	case sanitizeStringEscape:
		if value == '\\' {
			s.state = sanitizeText
		} else if value != 0x1b {
			s.state = sanitizeString
		}
	}
}

// consumeStringTerminator distinguishes a raw C1 ST byte or the exact UTF-8
// encoding C2 9C from 9C used as a continuation byte by another rune.
func (s *incrementalSanitizer) consumeStringTerminator(value byte) bool {
	if s.stringUTF8Remaining > 0 {
		if value >= 0x80 && value <= 0xbf {
			lead := s.stringUTF8Lead
			s.stringUTF8Remaining--
			complete := s.stringUTF8Remaining == 0
			if complete {
				s.stringUTF8Lead = 0
			}
			return complete && lead == 0xc2 && value == 0x9c
		}
		s.resetStringUTF8()
	}
	if value == 0x9c {
		return true
	}
	switch {
	case value >= 0xc2 && value <= 0xdf:
		s.stringUTF8Lead, s.stringUTF8Remaining = value, 1
	case value >= 0xe0 && value <= 0xef:
		s.stringUTF8Lead, s.stringUTF8Remaining = value, 2
	case value >= 0xf0 && value <= 0xf4:
		s.stringUTF8Lead, s.stringUTF8Remaining = value, 3
	}
	return false
}

func (s *incrementalSanitizer) resetStringUTF8() {
	s.stringUTF8Lead = 0
	s.stringUTF8Remaining = 0
}

func (w *streamWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return sanitizeOutput(w.buffer.String())
}

func (w *streamWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Truncated
}

// RunStreaming executes a command with stdin closed, streams sanitized output,
// enforces a hard timeout, and kills the complete child process group on stop.
func RunStreaming(parent context.Context, spec ExecSpec, opts StreamOptions, emit func(StreamEvent)) Result {
	result := Result{Program: spec.Program, Args: append([]string{}, spec.Args...), Cwd: spec.Cwd, ExitCode: -1}
	if spec.Program == "" {
		result.Err = "missing program"
		return result
	}
	if parent == nil {
		parent = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Minute
	}
	if opts.StallTimeout <= 0 {
		opts.StallTimeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, opts.Timeout)
	defer cancel()

	cmd := exec.Command(spec.Program, spec.Args...)
	cmd.Dir = spec.Cwd
	cmd.Stdin = strings.NewReader("")
	configureProcessGroup(cmd)
	activity := make(chan struct{}, 1)
	stdout := &streamWriter{stream: "stdout", emit: emit, activity: activity}
	stderr := &streamWriter{stream: "stderr", emit: emit, activity: activity}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		result.Err = err.Error()
		return result
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stall := time.NewTimer(opts.StallTimeout)
	defer stall.Stop()
	var runErr error
	stalled := false
	wait := true
	for wait {
		select {
		case runErr = <-done:
			wait = false
		case <-activity:
			if !stall.Stop() {
				select {
				case <-stall.C:
				default:
				}
			}
			stall.Reset(opts.StallTimeout)
		case <-stall.C:
			stalled = true
			killProcessTree(cmd)
			runErr = <-done
			wait = false
		case <-ctx.Done():
			killProcessTree(cmd)
			runErr = <-done
			wait = false
		}
	}

	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	result.Truncated = stdout.Truncated() || stderr.Truncated()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stalled {
		result.Err = "this command appears to be waiting for input"
		if emit != nil {
			emit(StreamEvent{Stream: "diagnostic", Data: result.Err})
		}
		return result
	}
	if ctx.Err() != nil {
		result.Err = ctx.Err().Error()
		return result
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			result.Err = runErr.Error()
		}
	}
	return result
}

var _ io.Writer = (*streamWriter)(nil)
