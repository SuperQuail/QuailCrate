package winshell

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	faviconTimeout     = 2500 * time.Millisecond
	faviconHTMLCap     = 64 * 1024
	faviconImageCap    = 512 * 1024
	faviconMissTTL     = 24 * time.Hour
	faviconTargetSize  = 64
)

// LoadCachedIcon loads a cached PNG/JPEG/GIF/ICO icon file from disk into premultiplied RGBA.
func LoadCachedIcon(path string) *image.RGBA {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	return decodeIconBytes(data)
}

// FetchFavicon fetches a website's favicon directly from the target host (supports both public
// sites and intranet/localhost services, with no third-party proxy), caches it as PNG under
// cacheDir/favicons, and returns nil on failure so the UI falls back to a colored monogram badge.
func FetchFavicon(rawURL, cacheDir string, forceRefresh bool) *image.RGBA {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return nil
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil
	}
	origin := scheme + "://" + strings.ToLower(parsed.Host)
	sum := sha256.Sum256([]byte(origin))
	key := hex.EncodeToString(sum[:16])
	favDir := ""
	pngPath := ""
	missPath := ""
	if cacheDir != "" {
		favDir = filepath.Join(cacheDir, "favicons")
		pngPath = filepath.Join(favDir, key+".png")
		missPath = filepath.Join(favDir, key+".miss")
		if !forceRefresh {
			if cached := LoadCachedIcon(pngPath); cached != nil {
				return cached
			}
			if info, statErr := os.Stat(missPath); statErr == nil && time.Since(info.ModTime()) < faviconMissTTL {
				return nil
			}
		} else {
			_ = os.Remove(missPath)
			_ = os.Remove(pngPath)
		}
	}

	client := faviconHTTPClient(parsed.Hostname())
	candidates := discoverFaviconCandidates(client, parsed, origin)
	for _, candidate := range candidates {
		img := downloadAndDecodeIcon(client, candidate)
		if img == nil {
			continue
		}
		scaled := resizeIconRGBA(img, faviconTargetSize)
		if favDir != "" {
			_ = os.MkdirAll(favDir, 0700)
			_ = os.Remove(missPath)
			_, _ = writeIconPNGExact(pngPath, scaled)
		}
		return scaled
	}

	if favDir != "" {
		_ = os.MkdirAll(favDir, 0700)
		_ = os.WriteFile(missPath, []byte(origin), 0600)
	}
	return nil
}

// ClearFaviconCache removes any cached PNG or negative-cache marker for rawURL.
func ClearFaviconCache(rawURL, cacheDir string) {
	if cacheDir == "" {
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return
	}
	origin := scheme + "://" + strings.ToLower(parsed.Host)
	sum := sha256.Sum256([]byte(origin))
	key := hex.EncodeToString(sum[:16])
	favDir := filepath.Join(cacheDir, "favicons")
	_ = os.Remove(filepath.Join(favDir, key+".png"))
	_ = os.Remove(filepath.Join(favDir, key+".miss"))
}

func faviconHTTPClient(hostname string) *http.Client {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   1500 * time.Millisecond,
			KeepAlive: 0,
		}).DialContext,
		TLSHandshakeTimeout: 1500 * time.Millisecond,
		DisableKeepAlives:   true,
	}
	if isIntranetHost(hostname) {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   faviconTimeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

func isIntranetHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "localhost" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".lan") || strings.HasSuffix(h, ".internal") || !strings.Contains(h, ".") {
		return true
	}
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func discoverFaviconCandidates(client *http.Client, pageURL *url.URL, origin string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(raw string) {
		if raw == "" || strings.HasPrefix(strings.ToLower(raw), "data:") {
			return
		}
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return
		}
		resolved := pageURL.ResolveReference(ref)
		s := strings.ToLower(resolved.Scheme)
		if s != "http" && s != "https" {
			return
		}
		final := resolved.String()
		if !seen[final] {
			seen[final] = true
			out = append(out, final)
		}
	}

	req, err := http.NewRequest(http.MethodGet, pageURL.String(), nil)
	if err == nil {
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) QuailCrate/1.0")
		req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
		if resp, doErr := client.Do(req); doErr == nil {
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				ct := strings.ToLower(resp.Header.Get("Content-Type"))
				if ct == "" || strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
					body, _ := io.ReadAll(io.LimitReader(resp.Body, faviconHTMLCap))
					for _, href := range extractLinkIconHrefs(string(body)) {
						add(href)
					}
				}
			}
			_ = resp.Body.Close()
		}
	}

	add(origin + "/favicon.ico")
	add(origin + "/favicon.png")
	add(origin + "/apple-touch-icon.png")
	return out
}

// extractLinkIconHrefs scans HTML <link> tags for icon relations without external parser dependencies.
func extractLinkIconHrefs(html string) []string {
	lower := strings.ToLower(html)
	var icons, apple []string
	pos := 0
	for {
		idx := strings.Index(lower[pos:], "<link")
		if idx < 0 {
			break
		}
		start := pos + idx
		end := strings.IndexByte(lower[start:], '>')
		if end < 0 {
			break
		}
		tag := html[start : start+end+1]
		pos = start + end + 1
		rel := strings.ToLower(htmlAttr(tag, "rel"))
		if !strings.Contains(rel, "icon") {
			continue
		}
		href := htmlAttr(tag, "href")
		if href == "" {
			continue
		}
		if strings.HasSuffix(strings.ToLower(strings.Split(href, "?")[0]), ".svg") {
			continue
		}
		if strings.Contains(rel, "apple-touch-icon") {
			apple = append(apple, href)
		} else {
			icons = append(icons, href)
		}
	}
	return append(icons, apple...)
}

func htmlAttr(tag, name string) string {
	lower := strings.ToLower(tag)
	search := strings.ToLower(name) + "="
	idx := 0
	for {
		found := strings.Index(lower[idx:], search)
		if found < 0 {
			return ""
		}
		pos := idx + found
		if pos > 0 {
			prev := lower[pos-1]
			if prev != ' ' && prev != '\t' && prev != '\n' && prev != '\r' {
				idx = pos + len(search)
				continue
			}
		}
		valStart := pos + len(search)
		if valStart >= len(tag) {
			return ""
		}
		quote := tag[valStart]
		if quote == '"' || quote == '\'' {
			end := strings.IndexByte(tag[valStart+1:], quote)
			if end < 0 {
				return ""
			}
			return strings.TrimSpace(tag[valStart+1 : valStart+1+end])
		}
		end := valStart
		for end < len(tag) && tag[end] != ' ' && tag[end] != '\t' && tag[end] != '\n' && tag[end] != '>' && tag[end] != '/' {
			end++
		}
		return strings.TrimSpace(tag[valStart:end])
	}
}

func downloadAndDecodeIcon(client *http.Client, iconURL string) *image.RGBA {
	req, err := http.NewRequest(http.MethodGet, iconURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) QuailCrate/1.0")
	req.Header.Set("Accept", "image/png,image/x-icon,image/jpeg,image/gif,*/*;q=0.5")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, faviconImageCap+1))
	if err != nil || len(data) == 0 || len(data) > faviconImageCap {
		return nil
	}
	return decodeIconBytes(data)
}

func decodeIconBytes(data []byte) *image.RGBA {
	if len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 {
		if ico := decodeICO(data); ico != nil {
			return ico
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil || img == nil {
		return nil
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > 1024 || b.Dy() > 1024 {
		return nil
	}
	if rgba, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) {
		return rgba
	}
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// decodeICO extracts the sharpest icon frame from a Windows .ico container (PNG or 32/24-bit DIB).
func decodeICO(data []byte) *image.RGBA {
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count <= 0 || count > 64 || len(data) < 6+count*16 {
		return nil
	}
	type entry struct {
		w, h, bpp int
		size, off uint32
	}
	bestIdx := -1
	bestScore := -1
	entries := make([]entry, 0, count)
	for i := 0; i < count; i++ {
		base := 6 + i*16
		w := int(data[base])
		if w == 0 {
			w = 256
		}
		h := int(data[base+1])
		if h == 0 {
			h = 256
		}
		bpp := int(binary.LittleEndian.Uint16(data[base+6 : base+8]))
		sz := binary.LittleEndian.Uint32(data[base+8 : base+12])
		off := binary.LittleEndian.Uint32(data[base+12 : base+16])
		if uint64(off)+uint64(sz) > uint64(len(data)) || sz < 16 {
			continue
		}
		entries = append(entries, entry{w: w, h: h, bpp: bpp, size: sz, off: off})
		// Prefer >= 32px icons closest to 64px, with higher bpp preferred.
		score := min(w, 64)*100 + bpp
		if w >= 32 {
			score += 5000
		}
		if score > bestScore {
			bestScore = score
			bestIdx = len(entries) - 1
		}
	}
	if bestIdx < 0 {
		return nil
	}
	tryOrder := make([]entry, 0, len(entries))
	tryOrder = append(tryOrder, entries[bestIdx])
	for i, e := range entries {
		if i != bestIdx {
			tryOrder = append(tryOrder, e)
		}
	}
	for _, e := range tryOrder {
		chunk := data[e.off : e.off+e.size]
		if len(chunk) >= 8 && bytes.Equal(chunk[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
			if img, err := png.Decode(bytes.NewReader(chunk)); err == nil {
				b := img.Bounds()
				out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
				draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
				return out
			}
			continue
		}
		if img := decodeICOBMP(chunk, e.w, e.h); img != nil {
			return img
		}
	}
	return nil
}

func decodeICOBMP(chunk []byte, fallbackW, fallbackH int) *image.RGBA {
	if len(chunk) < 40 {
		return nil
	}
	hdrSize := int(binary.LittleEndian.Uint32(chunk[0:4]))
	if hdrSize < 40 || hdrSize > len(chunk) {
		return nil
	}
	w := int(int32(binary.LittleEndian.Uint32(chunk[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(chunk[8:12]))) / 2
	if w <= 0 {
		w = fallbackW
	}
	if h <= 0 {
		h = fallbackH
	}
	bpp := int(binary.LittleEndian.Uint16(chunk[14:16]))
	compression := binary.LittleEndian.Uint32(chunk[16:20])
	if compression != 0 || w <= 0 || h <= 0 || w > 512 || h > 512 {
		return nil
	}
	if bpp != 32 && bpp != 24 {
		return nil
	}
	pixelData := chunk[hdrSize:]
	rowStride := ((w*bpp + 31) / 32) * 4
	if len(pixelData) < rowStride*h {
		return nil
	}
	maskOffset := rowStride * h
	maskStride := ((w + 31) / 32) * 4
	hasMask := len(pixelData) >= maskOffset+maskStride*h

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	if bpp == 32 {
		for y := 0; y < h; y++ {
			row := pixelData[y*rowStride : y*rowStride+w*4]
			for x := 0; x < w; x++ {
				if row[x*4+3] != 0 {
					anyAlpha = true
					break
				}
			}
			if anyAlpha {
				break
			}
		}
	}
	for y := 0; y < h; y++ {
		srcY := h - 1 - y
		row := pixelData[srcY*rowStride:]
		for x := 0; x < w; x++ {
			var r, g, b, a byte
			if bpp == 32 {
				b, g, r, a = row[x*4], row[x*4+1], row[x*4+2], row[x*4+3]
				if !anyAlpha {
					a = 255
				}
			} else {
				b, g, r, a = row[x*3], row[x*3+1], row[x*3+2], 255
			}
			if (!anyAlpha || bpp == 24) && hasMask {
				maskByte := pixelData[maskOffset+srcY*maskStride+x/8]
				if (maskByte & (0x80 >> uint(x%8))) != 0 {
					a = 0
				}
			}
			if a < 255 {
				r = uint8(uint16(r) * uint16(a) / 255)
				g = uint8(uint16(g) * uint16(a) / 255)
				b = uint8(uint16(b) * uint16(a) / 255)
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: a})
		}
	}
	return img
}

func resizeIconRGBA(src *image.RGBA, target int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == target && sh == target && b.Min == (image.Point{}) {
		return src
	}
	if sw <= target && sh <= target && sw == sh {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, target, target))
	for y := 0; y < target; y++ {
		sy := (float64(y)+0.5)*float64(sh)/float64(target) - 0.5
		y0 := max(0, min(sh-1, int(sy)))
		y1 := max(0, min(sh-1, y0+1))
		fy := sy - float64(y0)
		if fy < 0 {
			fy = 0
		}
		for x := 0; x < target; x++ {
			sx := (float64(x)+0.5)*float64(sw)/float64(target) - 0.5
			x0 := max(0, min(sw-1, int(sx)))
			x1 := max(0, min(sw-1, x0+1))
			fx := sx - float64(x0)
			if fx < 0 {
				fx = 0
			}
			c00 := src.RGBAAt(b.Min.X+x0, b.Min.Y+y0)
			c10 := src.RGBAAt(b.Min.X+x1, b.Min.Y+y0)
			c01 := src.RGBAAt(b.Min.X+x0, b.Min.Y+y1)
			c11 := src.RGBAAt(b.Min.X+x1, b.Min.Y+y1)
			lerp := func(v00, v10, v01, v11 uint8) uint8 {
				top := float64(v00)*(1-fx) + float64(v10)*fx
				bot := float64(v01)*(1-fx) + float64(v11)*fx
				v := top*(1-fy) + bot*fy + 0.5
				if v < 0 {
					return 0
				}
				if v > 255 {
					return 255
				}
				return uint8(v)
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: lerp(c00.R, c10.R, c01.R, c11.R),
				G: lerp(c00.G, c10.G, c01.G, c11.G),
				B: lerp(c00.B, c10.B, c01.B, c11.B),
				A: lerp(c00.A, c10.A, c01.A, c11.A),
			})
		}
	}
	return dst
}

func writeIconPNGExact(dest string, img image.Image) (string, error) {
	tmp := dest + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	encodeErr := png.Encode(f, img)
	closeErr := f.Close()
	if encodeErr != nil {
		_ = os.Remove(tmp)
		return "", encodeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return "", closeErr
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dest, nil
}
