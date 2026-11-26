package pluginio

import (
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/publicsuffix"
)

const maxBody = 256 << 10

type Request struct {
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url"`
	Body         string            `json:"body,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Session      string            `json:"session,omitempty"`
	TimeoutMs    int               `json:"timeoutMs,omitempty"`
	ResponseType string            `json:"responseType,omitempty"`
}

type Result struct {
	Status   int             `json:"status"`
	Headers  http.Header     `json:"headers"`
	Body     string          `json:"body,omitempty"`
	Encoding string          `json:"encoding,omitempty"`
	JSON     json.RawMessage `json:"json,omitempty"`
}

func (r Request) validate() error {
	if len(r.URL) > 8192 || len(r.Body) > maxBody || r.TimeoutMs < 0 || r.TimeoutMs > 60000 ||
		len(r.Headers) > 32 || (r.Session != "" && !validID(r.Session)) {
		return ErrInvalid
	}
	if _, err := targetURL(r.URL); err != nil {
		return err
	}
	switch r.Method {
	case "", "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return ErrInvalid
	}
	switch r.ResponseType {
	case "", "text", "json", "base64":
	default:
		return ErrInvalid
	}
	total := 0
	for k, v := range r.Headers {
		total += len(k) + len(v)
		key := strings.ToLower(k)
		if !httpguts.ValidHeaderFieldName(k) || !httpguts.ValidHeaderFieldValue(v) || total > 16384 {
			return ErrInvalid
		}
		switch key {
		case "connection", "host", "cookie", "set-cookie", "content-length", "transfer-encoding", "trailer", "te", "upgrade", "accept-encoding", "expect", "keep-alive", "forwarded":
			return ErrInvalid
		}
		if strings.HasPrefix(key, "proxy-") || strings.HasPrefix(key, "x-forwarded-") {
			return ErrInvalid
		}
	}
	return nil
}

func targetURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" ||
		strings.Contains(u.Hostname(), "%") {
		return nil, ErrInvalid
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p <= 0 || p > 65535 {
			return nil, ErrInvalid
		}
	}
	return u, nil
}

// These special-use networks can be reachable through a machine's routing or
// translation configuration even though IsPrivate does not flag them.
var specialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}
var globalIPv6 = netip.MustParsePrefix("2000::/3")

// Transparent TUN proxies (Clash / Mihomo / sing-box / Surge Fake-IP mode)
// resolve public domain names to RFC 2544 / RFC 5180 benchmarking ranges and
// route them out to the public internet.
var fakeIPNetworks = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("2001:2::/48"),
}

func isFakeIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, prefix := range fakeIPNetworks {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func isPublicDomainHost(host string) bool {
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return false
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	return icann && suffix != "" && host != suffix && strings.HasSuffix(host, "."+suffix)
}

func permittedIP(ip netip.Addr, allowLocal bool) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.MustParseAddr("255.255.255.255") {
		return false
	}
	if allowLocal {
		return true
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.Is6() && !globalIPv6.Contains(ip) {
		return false
	}
	for _, prefix := range specialNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func permittedResolvedIP(host string, ip netip.Addr, allowLocal bool) bool {
	if permittedIP(ip, allowLocal) {
		return true
	}
	return isPublicDomainHost(host) && isFakeIP(ip)
}

func (i *Instance) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalid
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !i.allowLocal && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || (!strings.ContainsAny(host, ".:"))) {
		return nil, ErrDenied
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("host lookup failed")
	}
	for _, ip := range ips {
		if !permittedResolvedIP(host, ip, i.allowLocal) {
			return nil, ErrDenied
		}
	}
	// Prefer IPv4 first when both A and AAAA records exist so TUN Fake-IPv4 works
	// immediately even if the local tunnel does not forward Fake-IPv6.
	ordered := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			ordered = append(ordered, ip)
		}
	}
	for _, ip := range ips {
		if !ip.Unmap().Is4() {
			ordered = append(ordered, ip)
		}
	}
	d := net.Dialer{Timeout: 8 * time.Second}
	for _, ip := range ordered {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("connection failed")
}

func redirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return errors.New("redirect limit exceeded")
	}
	if _, err := targetURL(req.URL.String()); err != nil {
		return err
	}
	for _, prev := range via {
		if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
			return ErrDenied
		}
		if !strings.EqualFold(req.URL.Host, prev.URL.Host) || req.URL.Scheme != prev.URL.Scheme {
			if req.Body != nil && req.Body != http.NoBody {
				return ErrDenied
			}
			for key := range req.Header {
				switch strings.ToLower(key) {
				case "range", "accept", "user-agent":
				default:
					req.Header.Del(key)
				}
			}
		}
	}
	return nil
}

type sessionTransport struct {
	base    *http.Transport
	session *session
}

func (t sessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if t.session != nil {
		for _, c := range t.session.cookies(req.URL) {
			req.AddCookie(c)
		}
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.session != nil && len(resp.Header.Values("Set-Cookie")) > 0 {
		if err = t.session.save(req.Context(), req.URL, resp.Cookies()); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}
	resp.Header.Del("Set-Cookie")
	resp.Header.Del("Set-Cookie2")
	return resp, nil
}

type streamBody struct {
	io.ReadCloser
	finish func()
	once   sync.Once
}

func (b *streamBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.finish)
	return err
}

// Stream returns a response body owned by the caller, which must Close it.
// It supports Range requests and does not buffer or cap media length. The
// caller context or the instance lifetime can interrupt blocked reads.
func (i *Instance) Stream(ctx context.Context, r Request) (*http.Response, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	if err := i.ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case i.slots <- struct{}{}:
	default:
		return nil, ErrBusy
	}
	select {
	case i.service.slots <- struct{}{}:
	default:
		<-i.slots
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(i.ctx, cancel)
	finish := func() { stop(); cancel(); <-i.slots; <-i.service.slots }
	var jar *session
	var err error
	if r.Session != "" {
		jar, err = i.getSession(r.Session)
		if err != nil {
			finish()
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, strings.NewReader(r.Body))
	if err != nil {
		finish()
		return nil, ErrInvalid
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	tr := &http.Transport{DialContext: i.dial, TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true}
	client := http.Client{Transport: sessionTransport{tr, jar}, CheckRedirect: redirect}
	resp, err := client.Do(req)
	if err != nil {
		cause := ctx.Err()
		finish()
		if cause != nil {
			return nil, cause
		}
		// Never return the net/http URL error, which can contain tokens.
		for _, known := range []error{ErrInvalid, ErrDenied, ErrBusy, errSession} {
			if errors.Is(err, known) {
				return nil, known
			}
		}
		return nil, errors.New("HTTP request failed")
	}
	resp.Body = &streamBody{ReadCloser: resp.Body, finish: finish}
	return resp, nil
}

func (i *Instance) fetch(ctx context.Context, r Request) (Result, error) {
	resp, err := i.Stream(ctx, r)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var reader io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		z, err := gzip.NewReader(resp.Body)
		if err != nil {
			return Result{}, errors.New("invalid gzip response")
		}
		defer z.Close()
		reader = z
	default:
		return Result{}, errors.New("unsupported content encoding")
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBody+1))
	if err != nil {
		return Result{}, errors.New("response read failed")
	}
	if len(body) > maxBody {
		return Result{}, errors.New("response size limit exceeded")
	}
	result := Result{Status: resp.StatusCode, Headers: resp.Header.Clone()}
	for _, k := range []string{"Authorization", "Proxy-Authenticate", "Proxy-Authorization", "Authentication-Info"} {
		result.Headers.Del(k)
	}
	if r.ResponseType == "json" {
		if !json.Valid(body) || !utf8.Valid(body) {
			return Result{}, errors.New("invalid JSON response")
		}
		result.JSON = json.RawMessage(body)
	} else if r.ResponseType == "base64" || !utf8.Valid(body) {
		result.Encoding = "base64"
		result.Body = base64.StdEncoding.EncodeToString(body)
	} else {
		result.Encoding = "utf-8"
		result.Body = string(body)
	}
	return result, nil
}
