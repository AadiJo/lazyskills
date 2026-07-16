package runner

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCappedBufferTruncatesOutput(t *testing.T) {
	var b cappedBuffer
	input := strings.Repeat("x", MaxOutputBytes+10)
	if n, err := b.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatalf("unexpected write result n=%d err=%v", n, err)
	}
	if !b.Truncated || !strings.Contains(b.String(), "[output truncated]") {
		t.Fatalf("expected truncation marker")
	}
}

func TestRunStreamingEmitsAndCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	var chunks []string
	var chunksMu sync.Mutex
	result := RunStreaming(context.Background(), ExecSpec{Program: "sh", Args: []string{"-c", "printf hello; printf warning >&2"}}, StreamOptions{}, func(event StreamEvent) {
		chunksMu.Lock()
		defer chunksMu.Unlock()
		chunks = append(chunks, event.Data)
	})
	chunksMu.Lock()
	defer chunksMu.Unlock()
	joined := strings.Join(chunks, "")
	if result.ExitCode != 0 || result.Stdout != "hello" || result.Stderr != "warning" || !strings.Contains(joined, "hello") || !strings.Contains(joined, "warning") {
		t.Fatalf("unexpected streaming result: %+v chunks=%q", result, chunks)
	}
}

func TestRunStreamingDiagnosesStall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	started := time.Now()
	result := RunStreaming(context.Background(), ExecSpec{Program: "sh", Args: []string{"-c", "printf 'Proceed? '; sleep 5"}}, StreamOptions{Timeout: time.Second, StallTimeout: 40 * time.Millisecond}, nil)
	if result.Err != "this command appears to be waiting for input" || time.Since(started) > time.Second {
		t.Fatalf("expected fast stall diagnosis, got %+v", result)
	}
}

func TestSanitizeOutputStripsControls(t *testing.T) {
	out := sanitizeOutput("hello\x1b[31m red\n")
	if strings.ContainsRune(out, '\x1b') || out != "hello red" {
		t.Fatalf("unexpected sanitized output %q", out)
	}
}

func TestStreamingChunksPreserveBoundaries(t *testing.T) {
	var chunks []string
	writer := &streamWriter{stream: "stdout", activity: make(chan struct{}, 1), emit: func(event StreamEvent) {
		chunks = append(chunks, event.Data)
	}}
	_, _ = writer.Write([]byte("hello "))
	_, _ = writer.Write([]byte("world\nnext"))
	if got := strings.Join(chunks, ""); got != "hello world\nnext" {
		t.Fatalf("stream boundaries changed output: %q", got)
	}
}

func TestStreamingChunksPreserveSplitUTF8AndStripSplitANSI(t *testing.T) {
	var chunks []string
	writer := &streamWriter{stream: "stdout", activity: make(chan struct{}, 1), emit: func(event StreamEvent) {
		chunks = append(chunks, event.Data)
	}}
	text := []byte("café")
	_, _ = writer.Write(text[:4])
	_, _ = writer.Write(append(text[4:], []byte(" \x1b[")...))
	_, _ = writer.Write([]byte("31mred\x1b"))
	_, _ = writer.Write([]byte("[0m done"))
	if got := strings.Join(chunks, ""); got != "café red done" {
		t.Fatalf("split UTF-8 or ANSI sequence was corrupted: %q", got)
	}
}

func TestStreamingChunksStripSplitOSC(t *testing.T) {
	var chunks []string
	writer := &streamWriter{stream: "stdout", activity: make(chan struct{}, 1), emit: func(event StreamEvent) {
		chunks = append(chunks, event.Data)
	}}
	_, _ = writer.Write([]byte("before\x1b]8;;https://example.com"))
	_, _ = writer.Write([]byte("\x1b\\link\x1b]8;;"))
	_, _ = writer.Write([]byte("\x1b\\after"))
	if got := strings.Join(chunks, ""); got != "beforelinkafter" {
		t.Fatalf("split OSC sequence leaked into output: %q", got)
	}
}

func TestStreamingChunksRecognizeC1StringTerminators(t *testing.T) {
	tests := []struct {
		name       string
		introducer []byte
		terminator [][]byte
	}{
		{name: "raw OSC and ST", introducer: []byte{0x9d}, terminator: [][]byte{{0x9c}}},
		{name: "raw DCS and ST", introducer: []byte{0x90}, terminator: [][]byte{{0x9c}}},
		{name: "UTF-8 OSC and split ST", introducer: []byte{0xc2, 0x9d}, terminator: [][]byte{{0xc2}, {0x9c}}},
		{name: "UTF-8 DCS and split ST", introducer: []byte{0xc2, 0x90}, terminator: [][]byte{{0xc2}, {0x9c}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var chunks []string
			writer := &streamWriter{stream: "stdout", activity: make(chan struct{}, 1), emit: func(event StreamEvent) {
				chunks = append(chunks, event.Data)
			}}
			_, _ = writer.Write([]byte("before"))
			_, _ = writer.Write(append(test.introducer, []byte("hidden")...))
			for _, part := range test.terminator {
				_, _ = writer.Write(part)
			}
			_, _ = writer.Write([]byte("after"))
			if got := strings.Join(chunks, ""); got != "beforeafter" {
				t.Fatalf("C1 string terminator swallowed or leaked output: %q", got)
			}
		})
	}
}

func TestStreamingOSCDoesNotTreatUTF8ContinuationAsST(t *testing.T) {
	var chunks []string
	writer := &streamWriter{stream: "stdout", activity: make(chan struct{}, 1), emit: func(event StreamEvent) {
		chunks = append(chunks, event.Data)
	}}
	_, _ = writer.Write([]byte("before\x1b]0;M\xc3"))
	_, _ = writer.Write([]byte("\x9cnchen\x07after"))
	if got := strings.Join(chunks, ""); got != "beforeafter" {
		t.Fatalf("UTF-8 continuation byte ended OSC early: %q", got)
	}
}
