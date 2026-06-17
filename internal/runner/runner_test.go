package runner

import (
	"strings"
	"testing"
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

func TestSanitizeOutputStripsControls(t *testing.T) {
	out := sanitizeOutput("hello\x1b[31m red\n")
	if strings.ContainsRune(out, '\x1b') || out != "hello red" {
		t.Fatalf("unexpected sanitized output %q", out)
	}
}
