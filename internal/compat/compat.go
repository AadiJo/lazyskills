package compat

import (
	"regexp"
	"strings"

	"github.com/alvinunreal/lazyskills/internal/model"
	xansi "github.com/charmbracelet/x/ansi"
)

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9._]+`)
var trimNameChars = regexp.MustCompile(`^[.\-]+|[.\-]+$`)

// SanitizeName mirrors vercel-labs/skills installer.ts sanitizeName.
func SanitizeName(name string) string {
	s := strings.ToLower(name)
	s = unsafeNameChars.ReplaceAllString(s, "-")
	s = trimNameChars.ReplaceAllString(s, "")
	if len(s) > 255 {
		s = s[:255]
	}
	if s == "" {
		return "unnamed-skill"
	}
	return s
}

func NormalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

var newlineRE = regexp.MustCompile(`[\r\n]+`)

func StripTerminalEscapes(str string) string {
	return stripUnsafeControls(xansi.Strip(str))

}

func stripUnsafeControls(str string) string {
	var b strings.Builder
	for _, r := range str {
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r < 32 || r == 127 || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func SanitizeMetadata(str string) string {
	return strings.TrimSpace(newlineRE.ReplaceAllString(StripTerminalEscapes(str), " "))
}

// SanitizePreviewContent strips terminal controls while preserving markdown line breaks.
func SanitizePreviewContent(str string) string {
	return StripTerminalEscapes(str)
}

// FirstNonEmpty returns the first value that is not the empty string, or "" if none are set.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type LocalLockDisplay struct {
	Source     string `json:"source,omitempty"`
	Ref        string `json:"ref,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	SkillPath  string `json:"skillPath,omitempty"`
}

type GlobalLockDisplay struct {
	Source     string `json:"source,omitempty"`
	SourceType string `json:"sourceType,omitempty"`
	SourceURL  string `json:"sourceUrl,omitempty"`
	Ref        string `json:"ref,omitempty"`
	SkillPath  string `json:"skillPath,omitempty"`
	PluginName string `json:"pluginName,omitempty"`
}

func SanitizeLocalLockDisplay(entry model.LocalLockEntry) LocalLockDisplay {
	return LocalLockDisplay{
		Source:     SanitizeMetadata(entry.Source),
		Ref:        SanitizeMetadata(entry.Ref),
		SourceType: SanitizeMetadata(entry.SourceType),
		SkillPath:  SanitizeMetadata(entry.SkillPath),
	}
}

func SanitizeGlobalLockDisplay(entry model.GlobalLockEntry) GlobalLockDisplay {
	return GlobalLockDisplay{
		Source:     SanitizeMetadata(entry.Source),
		SourceType: SanitizeMetadata(entry.SourceType),
		SourceURL:  SanitizeMetadata(entry.SourceURL),
		Ref:        SanitizeMetadata(entry.Ref),
		SkillPath:  SanitizeMetadata(entry.SkillPath),
		PluginName: SanitizeMetadata(entry.PluginName),
	}
}
