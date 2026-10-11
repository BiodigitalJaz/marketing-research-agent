package knowledge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
)

// Getter performs GET requests to an admin-configured API (fetch.Fetcher's
// Trusted). The GitHub client only ever reads.
type Getter interface {
	Trusted(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error)
}

// GitHub reads a repository through the GitHub REST API (GET only).
type GitHub struct {
	Base  string // default https://api.github.com
	Token string // fine-grained, read-only "Contents" access; empty for public repos
	Get   Getter
}

// TreeEntry is a file in a Git tree.
type TreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"` // blob | tree
	SHA  string `json:"sha"`
	Size int    `json:"size"`
}

// ErrAccess explains authentication and visibility failures.
var ErrAccess = errors.New("GitHub refused access: check the repository name and that the token has read access to it")

func (g *GitHub) get(ctx context.Context, p string, v any) error {
	base := g.Base
	if base == "" {
		base = "https://api.github.com"
	}
	headers := map[string]string{"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	if g.Token != "" {
		headers["Authorization"] = "Bearer " + g.Token
	}
	b, err := g.Get.Trusted(ctx, strings.TrimSuffix(base, "/")+p, headers)
	if err != nil {
		var se *fetch.StatusError
		if errors.As(err, &se) && (se.Code == 401 || se.Code == 403 || se.Code == 404) {
			return fmt.Errorf("%w (HTTP %d)", ErrAccess, se.Code)
		}
		return redact(err, g.Token)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func redact(err error, token string) error {
	if token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
}

func repoPath(repo string) (string, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", fmt.Errorf("repository must be owner/name, got %q", repo)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}

// Head returns the commit SHA at the tip of branch.
func (g *GitHub) Head(ctx context.Context, repo, branch string) (string, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return "", err
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err := g.get(ctx, rp+"/branches/"+url.PathEscape(branch), &out); err != nil {
		return "", err
	}
	if out.Commit.SHA == "" {
		return "", fmt.Errorf("branch %s has no commit", branch)
	}
	return out.Commit.SHA, nil
}

type treeResponse struct {
	Tree      []TreeEntry `json:"tree"`
	Truncated bool        `json:"truncated"`
}

// Files lists the files under prefix at commit, with paths relative to the
// repository root. It walks down to the prefix's subtree first, so large
// repositories don't matter.
func (g *GitHub) Files(ctx context.Context, repo, commit, prefix string) ([]TreeEntry, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	sha := commit
	walked := ""
	for _, part := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if part == "" {
			continue
		}
		var t treeResponse
		if err := g.get(ctx, rp+"/git/trees/"+url.PathEscape(sha), &t); err != nil {
			return nil, err
		}
		next := ""
		for _, e := range t.Tree {
			if e.Type == "tree" && e.Path == part {
				next = e.SHA
			}
		}
		if next == "" {
			return nil, fmt.Errorf("%s%s is not a directory at %s", walked, part, short(commit))
		}
		sha, walked = next, walked+part+"/"
	}
	var t treeResponse
	if err := g.get(ctx, rp+"/git/trees/"+url.PathEscape(sha)+"?recursive=1", &t); err != nil {
		return nil, err
	}
	if t.Truncated {
		return nil, errors.New("the docs directory is too large for one GitHub tree request")
	}
	var out []TreeEntry
	for _, e := range t.Tree {
		if e.Type == "blob" {
			e.Path = walked + e.Path
			out = append(out, e)
		}
	}
	return out, nil
}

// Blob returns a file's content by blob SHA.
func (g *GitHub) Blob(ctx context.Context, repo, sha string) ([]byte, error) {
	rp, err := repoPath(repo)
	if err != nil {
		return nil, err
	}
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := g.get(ctx, rp+"/git/blobs/"+url.PathEscape(sha), &out); err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected blob encoding %q", out.Encoding)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
}

// BlobURL links to a file at a commit on github.com, optionally at a
// heading anchor.
func BlobURL(repo, commit, filePath, anchor string) string {
	u := "https://github.com/" + strings.TrimSpace(repo) + "/blob/" + commit + "/" + filePath
	if anchor != "" {
		u += "#" + anchor
	}
	return u
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
