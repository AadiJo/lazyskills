package tui

import (
	"strings"
	"testing"
)

func TestSanitizeRegistryPreviewContentRemovesScriptsAndEventAttributes(t *testing.T) {
	input := `<div onclick="alert('attr')">Safe<script>alert('script')</script><span onmouseover="bad()"> text</span></div>`

	got := sanitizeRegistryPreviewContent(input)

	if got != "Safe text" {
		t.Fatalf("expected safe text only, got %q", got)
	}
	for _, forbidden := range []string{"script", "alert", "onclick", "onmouseover"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("expected %q to be removed from %q", forbidden, got)
		}
	}
}

func TestSanitizeRegistryPreviewContentPreservesBlockLineBreaks(t *testing.T) {
	input := "<h1>Title</h1><p>First</p><div>Second<br>line</div><ul><li>One</li><li>Two</li></ul>"

	got := sanitizeRegistryPreviewContent(input)
	want := "Title\nFirst\nSecond\nline\nOne\nTwo"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeRegistryPreviewContentDecodesEntities(t *testing.T) {
	input := "Tom &amp; Jerry &lt;3&nbsp;forever"

	got := sanitizeRegistryPreviewContent(input)
	want := "Tom & Jerry <3\u00a0forever"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeRegistryPreviewContentHandlesNestedTags(t *testing.T) {
	input := "<div>Outer <strong>bold <em>inner</em></strong> tail</div>"

	got := sanitizeRegistryPreviewContent(input)
	want := "Outer bold inner tail"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeRegistryPreviewContentKeepsMarkdownLiteralLessThan(t *testing.T) {
	input := "# Usage\n\nUse `if a < b` and keep 1 < 2 in prose."

	got := sanitizeRegistryPreviewContent(input)

	if got != input {
		t.Fatalf("expected markdown literal '<' to be preserved, got %q", got)
	}
}
