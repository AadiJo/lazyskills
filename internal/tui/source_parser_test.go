package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestSourceParserMatrix(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		wantRepo   string
		wantFolder string
		wantNorm   string
		remoteOK   bool
		wantRemote string
		wantRef    string
	}{
		{name: "slug", source: "owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "github shorthand", source: "github:owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "https", source: "https://github.com/owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "http", source: "http://github.com/owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "git https", source: "git+https://github.com/owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "ssh", source: "git@github.com:owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "dot git", source: "https://github.com/owner/repo.git", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo"},
		{name: "ref", source: "owner/repo#feature/ref", wantRepo: "owner/repo", wantNorm: "owner/repo", remoteOK: true, wantRemote: "https://github.com/owner/repo", wantRef: "feature/ref"},
		{name: "folder", source: "owner/repo/skills/build", wantRepo: "owner/repo", wantFolder: "skills/build", wantNorm: "owner/repo/skills/build"},
		{name: "host folder", source: "https://github.com/owner/repo/skills/build", wantRepo: "owner/repo", wantFolder: "skills/build", wantNorm: "owner/repo/skills/build"},
		{name: "github tree folder", source: "https://github.com/owner/repo/tree/main/packages/skill", wantRepo: "owner/repo", wantFolder: "packages/skill", wantNorm: "owner/repo/packages/skill"},
		{name: "gitlab display only", source: "https://gitlab.com/owner/repo", wantRepo: "owner/repo", wantNorm: "owner/repo"},
		{name: "unknown host keeps host context", source: "https://example.com/owner/repo", wantRepo: "example.com/owner", wantFolder: "repo", wantNorm: "example.com/owner/repo"},
		{name: "unknown ssh host keeps host context", source: "git@example.com:owner/repo", wantRepo: "example.com/owner", wantFolder: "repo", wantNorm: "example.com/owner/repo"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, folder := parseSourceURLDetails(tc.source)
			if repo != tc.wantRepo || folder != tc.wantFolder {
				t.Fatalf("parseSourceURLDetails() = %q, %q; want %q, %q", repo, folder, tc.wantRepo, tc.wantFolder)
			}
			if got := normalizeSource(tc.source); got != tc.wantNorm {
				t.Fatalf("normalizeSource() = %q; want %q", got, tc.wantNorm)
			}
			remote, ref, ok := parseRemoteGitHubSource(tc.source)
			if ok != tc.remoteOK || remote != tc.wantRemote || ref != tc.wantRef {
				t.Fatalf("parseRemoteGitHubSource() = %q, %q, %v; want %q, %q, %v", remote, ref, ok, tc.wantRemote, tc.wantRef, tc.remoteOK)
			}
		})
	}
}

func TestSourceParserRejectsInvalidRemoteDiscovery(t *testing.T) {
	cases := []string{
		"-owner/repo",
		"owner/-repo",
		"owner/repo#--ref",
		"owner/repo#branch..name",
		"owner/repo;-somecmd",
		"owner/repo/sub",
		"https://gitlab.com/owner/repo",
	}
	for _, source := range cases {
		if remote, ref, ok := parseRemoteGitHubSource(source); ok {
			t.Fatalf("parseRemoteGitHubSource(%q) = %q, %q, true; want rejected", source, remote, ref)
		}
	}
}

func TestDeriveRawGitHubURLsUsesParsedSource(t *testing.T) {
	got := deriveRawGitHubURLs("git+https://github.com/owner/repo.git/skills/build")
	want := []string{
		"https://raw.githubusercontent.com/owner/repo/main/skills/build/SKILL.md",
		"https://raw.githubusercontent.com/owner/repo/main/skills/build/README.md",
		"https://raw.githubusercontent.com/owner/repo/main/skills/build/README",
		"https://raw.githubusercontent.com/owner/repo/master/skills/build/SKILL.md",
		"https://raw.githubusercontent.com/owner/repo/master/skills/build/README.md",
		"https://raw.githubusercontent.com/owner/repo/master/skills/build/README",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deriveRawGitHubURLs() = %#v; want %#v", got, want)
	}

	if got := deriveRawGitHubURLs("https://gitlab.com/owner/repo"); got != nil {
		t.Fatalf("expected GitLab raw preview unsupported, got %#v", got)
	}
}

func TestDeriveRawGitHubURLsEscapesFolderSegments(t *testing.T) {
	got := deriveRawGitHubURLs("https://github.com/owner/repo/skills/a%23b/folder with spaces")
	if len(got) == 0 {
		t.Fatal("expected raw preview URLs for escaped folder")
	}
	for _, u := range got {
		if strings.Contains(u, "#") || strings.Contains(u, " ") {
			t.Fatalf("raw URL should escape fragments and spaces, got %q", u)
		}
		if !strings.Contains(u, "/skills/a%23b/folder%20with%20spaces/") {
			t.Fatalf("raw URL missing escaped folder path, got %q", u)
		}
	}

	for _, source := range []string{
		"https://github.com/owner/repo/../secret",
		"https://github.com/owner/repo/skills/../secret",
		"https://github.com/owner/repo/skills\\secret",
		"owner/repo#--ref",
		"owner/repo#bad ref",
		"https://github.com/owner/repo/tree/--ref/packages/skill",
	} {
		if got := deriveRawGitHubURLs(source); got != nil {
			t.Fatalf("expected unsafe source %q to be rejected, got %#v", source, got)
		}
	}
}

func TestDeriveRawGitHubURLsUsesTreeRefAndFolder(t *testing.T) {
	got := deriveRawGitHubURLs("https://github.com/owner/repo/tree/dev/packages/skill")
	want := []string{
		"https://raw.githubusercontent.com/owner/repo/dev/packages/skill/SKILL.md",
		"https://raw.githubusercontent.com/owner/repo/dev/packages/skill/README.md",
		"https://raw.githubusercontent.com/owner/repo/dev/packages/skill/README",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deriveRawGitHubURLs() = %#v; want %#v", got, want)
	}
}
