package plugininstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	metadataLimit   = 2 << 20
	downloadLimit   = 128 << 20
	metadataTimeout = 20 * time.Second
	downloadTimeout = 3 * time.Minute
)

type githubRelease struct {
	Tag    string        `json:"tag_name"`
	Draft  bool          `json:"draft"`
	Assets []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type archiveSource struct {
	downloads   []archiveDownload
	asset       string
	checksumURL string
	sourceCode  bool
}

type archiveDownload struct {
	address string
	ref     string
	subdir  string
}

func githubClient(base *http.Client) *http.Client {
	client := http.Client{Transport: http.DefaultTransport}
	if base != nil {
		client = *base
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || !validGitHubURL(req.URL) {
			return errors.New("invalid GitHub redirect")
		}
		if base != nil && base.CheckRedirect != nil {
			return base.CheckRedirect(req, via)
		}
		return nil
	}
	return &client
}

type githubStatus int

func (status githubStatus) Error() string {
	return fmt.Sprintf("GitHub returned HTTP %d", status)
}

func openGitHub(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, failure(ErrorNetwork, "", err)
	}
	if !validGitHubURL(req.URL) {
		return nil, failure(ErrorNetwork, "", errors.New("invalid GitHub download URL"))
	}
	req.Header.Set("User-Agent", "QuailCrate-PluginInstaller")
	if strings.EqualFold(req.URL.Host, "api.github.com") {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	res, err := client.Do(req)
	if err != nil {
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			// A redirected asset URL may contain a short-lived signature.
			err = requestErr.Err
		}
		return nil, failure(ErrorNetwork, "", err)
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		responseURL := req.URL
		if res.Request != nil {
			responseURL = res.Request.URL
		}
		if strings.EqualFold(responseURL.Host, "api.github.com") &&
			(res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusTooManyRequests) &&
			res.Header.Get("X-RateLimit-Remaining") == "0" {
			return nil, failure(ErrorRateLimit, "", githubStatus(res.StatusCode))
		}
		return nil, githubStatus(res.StatusCode)
	}
	return res, nil
}

func readGitHubMetadata(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	res, err := openGitHub(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.ContentLength > limit {
		return nil, errors.New("GitHub metadata exceeds the size limit")
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("GitHub metadata exceeds the size limit")
	}
	return body, nil
}

func resolveArchive(ctx context.Context, client *http.Client, spec SourceSpec) (archiveSource, error) {
	repository := spec.repositoryURL()
	switch spec.Kind {
	case KindDirectAsset:
		return archiveSource{downloads: []archiveDownload{{address: spec.URL, ref: spec.Ref}}, asset: spec.Asset}, nil
	case KindTreeArchive:
		return archiveSource{downloads: treeDownloads(spec), sourceCode: true}, nil
	}

	apiURL := "https://api.github.com/repos/" + spec.Owner + "/" + spec.Repo + "/releases/latest"
	if spec.Kind == KindReleaseTag {
		apiURL = "https://api.github.com/repos/" + spec.Owner + "/" + spec.Repo + "/releases/tags/" + url.PathEscape(spec.Ref)
	}
	body, err := readGitHubMetadata(ctx, client, apiURL, metadataLimit)
	if err != nil {
		if errors.Is(err, githubStatus(http.StatusNotFound)) && spec.Kind == KindRepoLatest && !spec.latestOnly {
			return archiveSource{downloads: []archiveDownload{{address: repository + "/archive/HEAD.zip", ref: "HEAD"}}, sourceCode: true}, nil
		}
		return archiveSource{}, failure(ErrorRelease, "", err)
	}
	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return archiveSource{}, failure(ErrorRelease, "", err)
	}
	if release.Draft || !validRef(release.Tag) || spec.Kind == KindReleaseTag && spec.Ref != release.Tag {
		return archiveSource{}, failure(ErrorRelease, "", errors.New("invalid GitHub release metadata"))
	}
	if spec.Kind == KindReleaseTag && len(release.Assets) == 0 {
		address := repository + "/archive/refs/tags/" + escapePath(release.Tag) + ".zip"
		return archiveSource{downloads: []archiveDownload{{address: address, ref: release.Tag}}, sourceCode: true}, nil
	}
	name, err := pickAsset(release.Assets)
	if err != nil {
		return archiveSource{}, err
	}
	source := archiveSource{downloads: []archiveDownload{{address: spec.assetURL(release.Tag, name), ref: release.Tag}}, asset: name}
	for _, asset := range release.Assets {
		if asset.available() && asset.Name == name+".sha256" {
			source.checksumURL = spec.assetURL(release.Tag, asset.Name)
			break
		}
	}
	return source, nil
}

func treeDownloads(spec SourceSpec) []archiveDownload {
	var downloads []archiveDownload
	archive := spec.repositoryURL() + "/archive/"
	ref, subdir := spec.Ref, spec.Subdir
	kinds := []string{"heads", "tags"}
	if spec.treeRefKind != "" {
		kinds = []string{spec.treeRefKind}
	}
	// GitHub tree URLs also use literal slashes in ref names. Try the shortest
	// ref first so ordinary branch/subdirectory links need one archive request.
	tryLongerRefs := !strings.Contains(ref, "/")
	for attempt := 0; attempt < 8; attempt++ {
		escaped := escapePath(ref) + ".zip"
		for _, kind := range kinds {
			address := archive + "refs/" + kind + "/" + escaped
			downloads = append(downloads, archiveDownload{address, ref, subdir})
		}
		if spec.treeRefKind == "" && commitPattern.MatchString(ref) {
			downloads = append(downloads, archiveDownload{archive + escaped, ref, subdir})
		}
		if subdir == "" || !tryLongerRefs {
			break
		}
		next, rest, _ := strings.Cut(subdir, "/")
		ref, subdir = ref+"/"+next, rest
		if !validRef(ref) {
			break
		}
	}
	return downloads
}

func (a githubAsset) available() bool {
	return (a.State == "uploaded" || a.State == "") && validAssetName(a.Name)
}

func pickAsset(assets []githubAsset) (string, error) {
	var best []string
	priority := 0
	for _, asset := range assets {
		if !asset.available() || !zipName(asset.Name) {
			continue
		}
		name := strings.ToLower(asset.Name)
		rank := 1
		switch {
		case strings.HasSuffix(name, ".qcplugin.zip"):
			rank = 3
		case strings.HasSuffix(name, "-windows-amd64.zip"), strings.HasSuffix(name, "_windows_amd64.zip"):
			rank = 2
		}
		if rank > priority {
			priority, best = rank, best[:0]
		}
		if rank == priority {
			best = append(best, asset.Name)
		}
	}
	switch len(best) {
	case 0:
		return "", failure(ErrorAssets, "", errors.New("release has no uploaded ZIP assets"))
	case 1:
		return best[0], nil
	default:
		sort.Strings(best)
		return "", failure(ErrorAmbiguousAssets, strings.Join(best, ", "), errors.New("release has multiple matching ZIP assets"))
	}
}

func downloadArchive(ctx context.Context, client *http.Client, source archiveSource, dst io.Writer) (archiveDownload, error) {
	var checksum []byte
	if source.checksumURL != "" {
		body, err := readGitHubMetadata(ctx, client, source.checksumURL, 8<<10)
		if err != nil {
			return archiveDownload{}, failure(ErrorChecksum, "", err)
		}
		fields := strings.Fields(string(body))
		if len(fields) == 0 {
			return archiveDownload{}, failure(ErrorChecksum, "", errors.New("the release checksum is empty"))
		}
		checksum, err = hex.DecodeString(fields[0])
		if err != nil || len(checksum) != sha256.Size {
			return archiveDownload{}, failure(ErrorChecksum, "", errors.New("invalid SHA-256 checksum"))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	for i, download := range source.downloads {
		res, err := openGitHub(ctx, client, download.address)
		if errors.Is(err, githubStatus(http.StatusNotFound)) && i+1 < len(source.downloads) {
			continue
		}
		if err != nil {
			return archiveDownload{}, failure(ErrorDownload, "", err)
		}
		defer res.Body.Close()
		if res.ContentLength > downloadLimit {
			return archiveDownload{}, failure(ErrorDownload, "", errors.New("ZIP download exceeds the 128 MiB limit"))
		}
		hash := sha256.New()
		writer := dst
		if checksum != nil {
			writer = io.MultiWriter(dst, hash)
		}
		size, err := io.Copy(writer, io.LimitReader(res.Body, downloadLimit))
		if err != nil {
			return archiveDownload{}, failure(ErrorDownload, "", err)
		}
		if size == downloadLimit {
			n, err := io.CopyN(io.Discard, res.Body, 1)
			if n != 0 {
				return archiveDownload{}, failure(ErrorDownload, "", errors.New("ZIP download exceeds the 128 MiB limit"))
			}
			if err != io.EOF {
				return archiveDownload{}, failure(ErrorDownload, "", err)
			}
		}
		if checksum != nil && !bytes.Equal(checksum, hash.Sum(nil)) {
			return archiveDownload{}, failure(ErrorChecksum, "", errors.New("downloaded ZIP does not match the release SHA-256 checksum"))
		}
		return download, nil
	}
	return archiveDownload{}, failure(ErrorDownload, "", errors.New("no ZIP download URL"))
}
