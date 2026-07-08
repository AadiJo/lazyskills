package tui

import (
	"strings"
	"testing"

	xansi "github.com/charmbracelet/x/ansi"
)

func assertDisplayWidths(t *testing.T, got string, width int) {
	t.Helper()
	for _, line := range strings.Split(got, "\n") {
		if lineWidth := xansi.StringWidth(line); lineWidth > width {
			t.Fatalf("line %q has display width %d, want <= %d in %q", line, lineWidth, width, got)
		}
	}
}

func TestWrapTextPreservesStyledANSIAndDisplayWidth(t *testing.T) {
	styled := "\x1b[38;5;205malpha beta gamma\x1b[0m"
	got := wrapText(styled, 10)

	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected styled ANSI sequences to be preserved, got %q", got)
	}
	if strings.Count(got, "\n") == 0 {
		t.Fatalf("expected wrapped output, got %q", got)
	}
	assertDisplayWidths(t, got, 10)
	if stripped := strings.ReplaceAll(xansi.Strip(got), "\n", " "); stripped != "alpha beta gamma" {
		t.Fatalf("unexpected stripped wrapped text %q", stripped)
	}
}

func TestWrapUsesDisplayWidthForWideGlyphsAndEmoji(t *testing.T) {
	got := wrap("漢字 emoji 😊 wrap here", 10)

	if strings.Count(got, "\n") == 0 {
		t.Fatalf("expected wrapped output, got %q", got)
	}
	assertDisplayWidths(t, got, 10)
	if !strings.Contains(got, "漢字") || !strings.Contains(got, "😊") {
		t.Fatalf("expected wide glyphs and emoji to be preserved, got %q", got)
	}
}

func TestFormatMetaLineWrapsValueUsingDisplayWidth(t *testing.T) {
	got := formatMetaLine("Author", "alpha 漢字 beta 😊 gamma", 20)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected metadata value to wrap, got %q", got)
	}

	assertDisplayWidths(t, got, 20)
	strippedFirst := xansi.Strip(lines[0])
	if !strings.HasPrefix(strippedFirst, "Author       ") {
		t.Fatalf("expected padded metadata key, got %q", strippedFirst)
	}
	if !strings.HasPrefix(xansi.Strip(lines[1]), strings.Repeat(" ", 13)) {
		t.Fatalf("expected continuation line indentation, got %q", xansi.Strip(lines[1]))
	}
}

func TestFormatMetaLineWrapsStyledValueUsingDisplayWidth(t *testing.T) {
	value := "\x1b[31malpha beta gamma delta epsilon\x1b[0m"
	got := formatMetaLine("Status", value, 22)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected styled metadata value to wrap, got %q", got)
	}

	assertDisplayWidths(t, got, 22)
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected styled ANSI sequences to be preserved, got %q", got)
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("wrapped styled metadata contains replacement characters: %q", got)
	}
	stripped := strings.Join(strings.Fields(xansi.Strip(got)), " ")
	if stripped != "Status alpha beta gamma delta epsilon" {
		t.Fatalf("unexpected stripped metadata text %q from %q", stripped, got)
	}
	if !strings.HasPrefix(xansi.Strip(lines[1]), strings.Repeat(" ", 13)) {
		t.Fatalf("expected continuation line indentation, got %q", xansi.Strip(lines[1]))
	}
}

func TestTruncateTitleUsesDisplayWidth(t *testing.T) {
	styled := "\x1b[38;5;39mSkill 漢字 😊 Title\x1b[0m"
	got := truncateTitle(styled, 10)

	if !strings.Contains(got, "…") {
		t.Fatalf("expected ellipsis, got %q", got)
	}
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI sequences to be preserved, got %q", got)
	}
	if width := xansi.StringWidth(got); width > 10 {
		t.Fatalf("truncated title width = %d, want <= 10 for %q", width, got)
	}
}
