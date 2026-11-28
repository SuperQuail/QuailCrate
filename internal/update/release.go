package update

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	releaseURL         = "https://github.com/SuperQuail/QuailCrate/releases/"
	releasesAPI        = "https://api.github.com/repos/SuperQuail/QuailCrate/releases?per_page=100"
	maxReleaseResponse = 8 << 20
)

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

type candidate struct {
	version   string
	asset     Asset
	manifest  []byte
	signature []byte
}

func newHTTPClient(base *http.Client) *http.Client {
	client := http.Client{Timeout: 5 * time.Minute}
	if base != nil {
		client = *base
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Scheme != "https" || req.URL.Port() != "" || req.URL.User != nil {
			return errors.New("invalid update redirect")
		}
		switch req.URL.Hostname() {
		case "github.com":
			if !strings.HasPrefix(req.URL.Path, "/SuperQuail/QuailCrate/releases/") {
				return errors.New("update redirect left the release repository")
			}
		case "release-assets.githubusercontent.com", "objects.githubusercontent.com", "github-releases.githubusercontent.com":
		default:
			return errors.New("update redirect left GitHub")
		}
		if base != nil && base.CheckRedirect != nil {
			return base.CheckRedirect(req, via)
		}
		return nil
	}
	return &client
}

func discover(ctx context.Context, client *http.Client, current, assetType string, key ed25519.PublicKey) (*candidate, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := fetch(ctx, client, releasesAPI, maxReleaseResponse)
	if err != nil {
		return nil, err
	}
	var releases []release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, failure(ErrorRelease, fmt.Errorf("read GitHub releases: %w", err))
	}
	var newest string
	for _, entry := range releases {
		if !entry.Draft && strings.HasPrefix(entry.Tag, "v") && CanFollow(entry.Tag, current) &&
			entry.Prerelease == (Channel(entry.Tag) == "alpha") &&
			(newest == "" || Newer(entry.Tag, newest)) && entry.hasUpdateAssets(assetType) {
			newest = entry.Tag
		}
	}
	if newest != "" {
		ver := strings.TrimPrefix(newest, "v")
		manifestData, err := fetch(ctx, client, assetURL(ver, ManifestName), maxManifestSize)
		if err != nil {
			return nil, err
		}
		signature, err := fetch(ctx, client, assetURL(ver, SignatureName), maxSignatureSize)
		if err != nil {
			return nil, err
		}
		manifest, err := VerifyManifest(manifestData, signature, key)
		if err != nil {
			return nil, failure(ErrorSignature, err)
		}
		asset, available := manifest.Asset(assetType)
		if manifest.Version != ver || !available {
			return nil, failure(ErrorRelease, errors.New("signed manifest does not match the release tag and package type"))
		}
		return &candidate{version: ver, asset: asset, manifest: manifestData, signature: signature}, nil
	}
	return nil, nil
}

func (r release) hasUpdateAssets(assetType string) bool {
	var manifest, signature, packageFile bool
	for _, asset := range r.Assets {
		switch asset.Name {
		case ManifestName:
			manifest = true
		case SignatureName:
			signature = true
		case PackageName(assetType):
			packageFile = true
		}
	}
	return manifest && signature && packageFile
}

func releasePage(version string) string {
	return releaseURL + "tag/v" + url.PathEscape(version)
}

func assetURL(version, name string) string {
	return releaseURL + "download/v" + url.PathEscape(version) + "/" + url.PathEscape(name)
}

func openRemote(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, failure(ErrorNetwork, err)
	}
	req.Header.Set("User-Agent", "QuailCrate-Updater")
	if req.URL.Hostname() == "api.github.com" {
		req.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, failure(ErrorNetwork, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, failure(ErrorNetwork, fmt.Errorf("update request returned HTTP %d", resp.StatusCode))
	}
	return resp, nil
}

func fetch(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	resp, err := openRemote(ctx, client, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := readLimited(resp.Body, limit)
	if err != nil {
		return nil, failure(ErrorRelease, err)
	}
	return body, nil
}

func download(ctx context.Context, client *http.Client, candidate *candidate, destination io.Writer) error {
	resp, err := openRemote(ctx, client, assetURL(candidate.version, candidate.asset.Name))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.ContentLength >= 0 && resp.ContentLength != candidate.asset.Size {
		return failure(ErrorIntegrity, errors.New("response size does not match the signed package size"))
	}
	size, err := io.Copy(destination, io.LimitReader(resp.Body, candidate.asset.Size+1))
	if err != nil {
		return failure(ErrorDownload, err)
	}
	if size != candidate.asset.Size {
		return failure(ErrorIntegrity, errors.New("downloaded size does not match the signed package size"))
	}
	return nil
}
