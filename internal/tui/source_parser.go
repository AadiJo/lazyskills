package tui

import (
	"net/url"
	"strings"
)

type parsedSource struct {
	Host   string
	Owner  string
	Repo   string
	Folder string
	Ref    string
}

func parseSource(source string) (parsedSource, bool) {
	src := strings.TrimSpace(source)
	if src == "" {
		return parsedSource{}, false
	}

	repoPart := src
	ref := ""
	if idx := strings.Index(repoPart, "#"); idx != -1 {
		ref = repoPart[idx+1:]
		repoPart = repoPart[:idx]
	}

	repoPart = strings.TrimPrefix(repoPart, "git+")
	host := ""

	if strings.HasPrefix(repoPart, "github:") {
		host = "github.com"
		repoPart = strings.TrimPrefix(repoPart, "github:")
	} else if strings.HasPrefix(repoPart, "git@") {
		ssh := strings.TrimPrefix(repoPart, "git@")
		if i := strings.Index(ssh, ":"); i != -1 {
			host = strings.ToLower(ssh[:i])
			if !knownSourceHost(host) {
				return parsedSource{}, false
			}
			repoPart = ssh[i+1:]
		} else {
			repoPart = ssh
		}
	} else if u, err := url.Parse(repoPart); err == nil && u.Scheme != "" && u.Host != "" {
		host = strings.ToLower(u.Host)
		if !knownSourceHost(host) {
			return parsedSource{}, false
		}
		repoPart = strings.TrimPrefix(u.Path, "/")
	}

	if decoded, err := url.PathUnescape(repoPart); err == nil {
		repoPart = decoded
	}
	repoPart = strings.TrimRight(repoPart, "/")
	repoPart = strings.ReplaceAll(repoPart, ":", "/")

	for _, knownHost := range []string{"github.com/", "gitlab.com/"} {
		if strings.HasPrefix(strings.ToLower(repoPart), knownHost) {
			host = strings.TrimSuffix(knownHost, "/")
			repoPart = repoPart[len(knownHost):]
			break
		}
	}

	parts := strings.Split(repoPart, "/")
	if len(parts) < 2 {
		return parsedSource{}, false
	}
	owner := parts[0]
	repo := strings.TrimSuffix(parts[1], ".git")
	folder := ""
	if host == "github.com" && len(parts) >= 4 && parts[2] == "tree" {
		if ref == "" {
			ref = parts[3]
		}
		if len(parts) > 4 {
			folder = strings.Join(parts[4:], "/")
		}
	} else if len(parts) > 2 {
		folder = strings.Join(parts[2:], "/")
	}

	return parsedSource{Host: host, Owner: owner, Repo: repo, Folder: folder, Ref: ref}, true
}

func knownSourceHost(host string) bool {
	switch strings.ToLower(host) {
	case "github.com", "gitlab.com":
		return true
	default:
		return false
	}
}

func (s parsedSource) repoSlug() string {
	if s.Owner == "" || s.Repo == "" {
		return ""
	}
	return s.Owner + "/" + s.Repo
}

func (s parsedSource) validRepo() bool {
	return isSafeGitHubToken(s.Owner) && isSafeGitHubToken(s.Repo)
}

func (s parsedSource) validRef() bool {
	return s.Ref == "" || isSafeGitHubRef(s.Ref)
}

func legacySourceURLDetails(source string) (repo string, folder string) {
	src := source
	src = strings.TrimPrefix(src, "git+https://")
	src = strings.TrimPrefix(src, "https://")
	src = strings.TrimPrefix(src, "http://")
	src = strings.TrimPrefix(src, "git@")
	src = strings.ReplaceAll(src, ":", "/")
	src = strings.TrimSuffix(src, ".git")
	src = strings.TrimRight(src, "/")
	for _, host := range []string{"github.com/", "gitlab.com/"} {
		src = strings.TrimPrefix(src, host)
	}
	parts := strings.Split(src, "/")
	if len(parts) >= 2 {
		repo = parts[0] + "/" + parts[1]
		if len(parts) > 2 {
			folder = strings.Join(parts[2:], "/")
		}
	} else {
		repo = src
	}
	return repo, folder
}

func escapedSourceFolder(folder string) (string, bool) {
	if folder == "" {
		return "", true
	}
	parts := strings.Split(folder, "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, "\\") {
			return "", false
		}
		escaped = append(escaped, url.PathEscape(part))
	}
	return strings.Join(escaped, "/"), true
}
