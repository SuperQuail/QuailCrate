package plugininstall

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type SourceKind uint8

const (
	KindRepoLatest SourceKind = iota + 1
	KindReleaseTag
	KindDirectAsset
	KindTreeArchive
)

type SourceSpec struct {
	Kind   SourceKind
	URL    string
	Owner  string
	Repo   string
	Ref    string
	Subdir string
	Asset  string

	latestOnly  bool
	treeRefKind string
}

var (
	ownerPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,38}$`)
	repoPattern   = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)
	commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
)

// ParseSource converts a GitHub repository or release link into a download specification.
func ParseSource(rawURL string) (SourceSpec, error) {
	badSource := func() (SourceSpec, error) {
		return SourceSpec{}, failure(ErrorSource, "", errors.New("unsupported GitHub repository, release, ZIP asset, or tree URL"))
	}
	rawURL = strings.TrimSpace(rawURL)
	if len(rawURL) == 0 || len(rawURL) > 4096 {
		return badSource()
	}
	shorthand := !strings.Contains(rawURL, "://")
	if shorthand {
		rawURL = "https://github.com/" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || !validGitHubURL(u) || !strings.EqualFold(u.Host, "github.com") {
		return badSource()
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(u.EscapedPath(), "/"), "/"), "/")
	for i, part := range parts {
		p, err := url.PathUnescape(part)
		if err != nil || p == "" || !utf8.ValidString(p) || strings.ContainsFunc(p, unicode.IsControl) {
			return badSource()
		}
		parts[i] = p
	}
	if len(parts) < 2 || shorthand && len(parts) != 2 || !ownerPattern.MatchString(parts[0]) ||
		!repoPattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == ".." {
		return badSource()
	}
	spec := SourceSpec{Owner: strings.ToLower(parts[0]), Repo: strings.ToLower(parts[1])}
	base := spec.repositoryURL()
	switch {
	case len(parts) == 2:
		spec.Kind, spec.URL = KindRepoLatest, base
	case len(parts) == 3 && parts[2] == "releases",
		len(parts) == 4 && parts[2] == "releases" && parts[3] == "latest":
		spec.Kind, spec.URL, spec.latestOnly = KindRepoLatest, base+"/releases/latest", true
	case len(parts) >= 5 && parts[2] == "releases" && parts[3] == "tag":
		spec.Kind, spec.Ref = KindReleaseTag, strings.Join(parts[4:], "/")
		if !validRef(spec.Ref) {
			return badSource()
		}
		spec.URL = base + "/releases/tag/" + escapePath(spec.Ref)
	case len(parts) >= 6 && parts[2] == "releases" && parts[3] == "download":
		spec.Kind, spec.Ref, spec.Asset = KindDirectAsset, strings.Join(parts[4:len(parts)-1], "/"), parts[len(parts)-1]
		if !validRef(spec.Ref) || !validAssetName(spec.Asset) || !zipName(spec.Asset) {
			return badSource()
		}
		spec.URL = spec.assetURL(spec.Ref, spec.Asset)
	case len(parts) >= 4 && parts[2] == "tree":
		spec.Kind, spec.Ref = KindTreeArchive, parts[3]
		subdir := parts[4:]
		if len(parts) >= 6 && parts[3] == "refs" && (parts[4] == "heads" || parts[4] == "tags") {
			spec.treeRefKind = parts[4]
			spec.Ref = parts[5]
			subdir = parts[6:]
		}
		if !validRef(spec.Ref) {
			return badSource()
		}
		for _, part := range subdir {
			if !validArchiveComponent(part) {
				return badSource()
			}
		}
		spec.Subdir = strings.Join(subdir, "/")
		ref := url.PathEscape(spec.Ref)
		if spec.treeRefKind != "" {
			ref = "refs/" + spec.treeRefKind + "/" + ref
		}
		spec.URL = base + "/tree/" + ref
		if spec.Subdir != "" {
			spec.URL += "/" + escapePath(spec.Subdir)
		}
	default:
		return badSource()
	}
	return spec, nil
}

func (s SourceSpec) repositoryURL() string {
	return "https://github.com/" + s.Owner + "/" + s.Repo
}

func (s SourceSpec) assetURL(tag, name string) string {
	return s.repositoryURL() + "/releases/download/" + escapePath(tag) + "/" + url.PathEscape(name)
}

func validGitHubURL(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Port() != "" {
		return false
	}
	switch strings.ToLower(u.Host) {
	case "github.com", "api.github.com", "codeload.github.com", "objects.githubusercontent.com",
		"release-assets.githubusercontent.com", "github-releases.githubusercontent.com":
		return true
	default:
		return false
	}
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func validRef(ref string) bool {
	if ref == "" || ref == "@" || !utf8.ValidString(ref) || strings.HasSuffix(ref, ".") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "@{") ||
		strings.ContainsAny(ref, " ~^:?*[\\") || strings.ContainsFunc(ref, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func validAssetName(name string) bool {
	return name != "" && name != "." && name != ".." && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\\") && !strings.ContainsFunc(name, unicode.IsControl)
}

func zipName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".zip")
}
