package pluginio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

var errSession = errors.New("login session unavailable")

type sessionKey struct{ plugin, name string }
type savedCookie struct {
	Origin string
	Cookie http.Cookie
}
type cookieKey struct{ domain, path, name string }
type session struct {
	service *Service
	key     sessionKey
	jar     *cookiejar.Jar
	saved   map[cookieKey]savedCookie
}

func hashed(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (s *Service) path(key sessionKey) string {
	return filepath.Join(s.PluginSessionDir(key.plugin), hashed(key.name)+".dat")
}

func (s *Service) PluginSessionDir(pluginID string) string {
	return filepath.Join(s.dir, hashed(pluginID))
}

// ForgetPluginSessions waits for any in-progress save and drops cached sessions.
// The host calls it after stopping the plugin and before moving its stored data.
func (s *Service) ForgetPluginSessions(pluginID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key := range s.sessions {
		if key.plugin == pluginID {
			delete(s.sessions, key)
		}
	}
}

func (s *session) entropy() []byte {
	h := sha256.Sum256([]byte("quailcrate plugin cookies\x00" + s.key.plugin + "\x00" + s.key.name))
	return h[:]
}

func (i *Instance) CreateSession(name string) error {
	_, err := i.getSession(name)
	return err
}

func (i *Instance) ClearSession(name string) error {
	if !validID(name) {
		return ErrInvalid
	}
	s := i.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := i.ctx.Err(); err != nil {
		return err
	}
	key := sessionKey{i.plugin, name}
	if err := os.Remove(s.path(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errSession
	}
	delete(s.sessions, key)
	return nil
}

func (i *Instance) getSession(name string) (*session, error) {
	if !validID(name) {
		return nil, ErrInvalid
	}
	s := i.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := i.ctx.Err(); err != nil {
		return nil, err
	}
	key := sessionKey{i.plugin, name}
	if stored := s.sessions[key]; stored != nil {
		return stored, nil
	}
	count := 0
	for k := range s.sessions {
		if k.plugin == key.plugin {
			count++
		}
	}
	if count >= 32 {
		return nil, ErrBusy
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, errSession
	}
	ss := &session{service: s, key: key, jar: jar, saved: make(map[cookieKey]savedCookie)}
	path := s.path(key)
	fi, err := os.Stat(path)
	if err == nil {
		if fi.Size() > 2<<20 {
			return nil, errSession
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, errSession
		}
		data, err = unseal(data, ss.entropy())
		if err != nil {
			return nil, errSession
		}
		var saved []savedCookie
		if json.Unmarshal(data, &saved) != nil || len(saved) > 128 {
			return nil, errSession
		}
		for _, item := range saved {
			u, err := targetURL(item.Origin)
			if err != nil {
				return nil, errSession
			}
			k, ok := scopeCookie(u, &item.Cookie)
			if !ok || expired(item.Cookie) {
				continue
			}
			ss.saved[k] = item
			jar.SetCookies(u, []*http.Cookie{&item.Cookie})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, errSession
	}
	s.sessions[key] = ss
	return ss, nil
}

func (s *session) cookies(u *url.URL) []*http.Cookie {
	s.service.mu.Lock()
	defer s.service.mu.Unlock()
	if s.service.sessions[s.key] != s {
		return nil
	}
	return s.jar.Cookies(u)
}

func expired(c http.Cookie) bool {
	return c.MaxAge < 0 || !c.Expires.IsZero() && !c.Expires.After(time.Now())
}

func scopeCookie(u *url.URL, c *http.Cookie) (cookieKey, bool) {
	host := strings.ToLower(u.Hostname())
	domain := host
	if c.Domain != "" {
		domain = strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if domain != host && (net.ParseIP(host) != nil || !strings.HasSuffix(host, "."+domain)) {
			return cookieKey{}, false
		}
		if strings.HasSuffix(domain, ".") || domain == "" {
			return cookieKey{}, false
		}
		if suffix, _ := publicsuffix.PublicSuffix(domain); suffix == domain && domain != host {
			return cookieKey{}, false
		}
	}
	path := c.Path
	if !strings.HasPrefix(path, "/") {
		path = "/"
		if last := strings.LastIndex(u.Path, "/"); last > 0 {
			path = u.Path[:last]
		}
	}
	return cookieKey{domain, path, c.Name}, true
}

func (s *session) save(ctx context.Context, u *url.URL, cookies []*http.Cookie) error {
	service := s.service
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if service.sessions[s.key] != s {
		return errSession
	}
	next := make(map[cookieKey]savedCookie, len(s.saved))
	for k, v := range s.saved {
		if !expired(v.Cookie) {
			next[k] = v
		}
	}
	var accepted []*http.Cookie
	for _, incoming := range cookies {
		if incoming == nil || incoming.Valid() != nil {
			continue
		}
		k, ok := scopeCookie(u, incoming)
		if !ok {
			continue
		}
		c := *incoming
		c.Raw = ""
		c.Unparsed = nil
		c.RawExpires = ""
		if c.MaxAge > 0 {
			// Bound the arithmetic before translating relative Max-Age into a
			// fixed expiry, which must not slide forward on every restart.
			const maxAge = 10 * 365 * 24 * 60 * 60
			c.Expires = time.Now().Add(time.Duration(min(c.MaxAge, maxAge)) * time.Second)
			c.MaxAge = 0
		}
		accepted = append(accepted, &c)
		if expired(c) {
			delete(next, k)
			continue
		}
		c.Path = k.path
		origin := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/"}
		next[k] = savedCookie{origin.String(), c}
	}
	if len(accepted) == 0 {
		return nil
	}
	if len(next) > 128 {
		return ErrBusy
	}
	all := make([]savedCookie, 0, len(next))
	for _, v := range next {
		all = append(all, v)
	}
	data, err := json.Marshal(all)
	if err != nil || len(data) > 1<<20 {
		return errSession
	}
	data, err = seal(data, s.entropy())
	if err != nil {
		return errSession
	}
	path := service.path(s.key)
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return errSession
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		files, err := os.ReadDir(dir)
		if err != nil {
			return errSession
		}
		if len(files) >= 32 {
			return ErrBusy
		}
	}
	file, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return errSession
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	if err != nil || closeErr != nil || os.Rename(file.Name(), path) != nil {
		return errSession
	}
	s.saved = next
	s.jar.SetCookies(u, accepted)
	return nil
}
