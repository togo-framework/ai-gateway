package aigateway

import (
	"strings"
	"sync"
	"time"
)

// --- sliding-window rate limiter (per key, requests/min) ---

type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func newLimiter() *limiter {
	return &limiter{hits: map[string][]time.Time{}, now: time.Now}
}

// allow reports whether the key may make another request this minute.
func (l *limiter) allow(key string, perMin int) bool {
	if perMin <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cutoff := now.Add(-time.Minute)
	keep := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= perMin {
		l.hits[key] = keep
		return false
	}
	l.hits[key] = append(keep, now)
	return true
}

// --- response cache (sha256 key → response, TTL) ---

type cacheEntry struct {
	resp    Response
	expires time.Time
}

type respCache struct {
	mu  sync.Mutex
	m   map[string]cacheEntry
	now func() time.Time
}

func newRespCache() *respCache {
	return &respCache{m: map[string]cacheEntry{}, now: time.Now}
}

func (c *respCache) get(key string) (Response, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || c.now().After(e.expires) {
		if ok {
			delete(c.m, key)
		}
		return Response{}, false
	}
	return e.resp, true
}

func (c *respCache) set(key string, resp Response, ttl time.Duration) {
	c.mu.Lock()
	c.m[key] = cacheEntry{resp: resp, expires: c.now().Add(ttl)}
	c.mu.Unlock()
}

// --- prompt template store (versioned, {{var}} interpolation) ---

type promptVersion struct {
	Version int    `json:"version"`
	Content string `json:"content"`
	Active  bool   `json:"active"`
}

type promptStore struct {
	mu sync.Mutex
	m  map[string][]*promptVersion
}

func newPromptStore() *promptStore {
	return &promptStore{m: map[string][]*promptVersion{}}
}

// Set creates a new version of a named template and makes it active.
func (p *promptStore) Set(name, content string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.m[name] {
		v.Active = false
	}
	ver := len(p.m[name]) + 1
	p.m[name] = append(p.m[name], &promptVersion{Version: ver, Content: content, Active: true})
	return ver
}

// Activate makes a specific version active.
func (p *promptStore) Activate(name string, version int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := false
	for _, v := range p.m[name] {
		v.Active = v.Version == version
		if v.Version == version {
			found = true
		}
	}
	return found
}

// Versions lists a template's versions.
func (p *promptStore) Versions(name string) []*promptVersion {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*promptVersion, len(p.m[name]))
	copy(out, p.m[name])
	return out
}

// Names lists all template names.
func (p *promptStore) Names() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.m))
	for n := range p.m {
		out = append(out, n)
	}
	return out
}

// Render returns the active version interpolated with {{var}} values.
func (p *promptStore) Render(name string, vars map[string]string) (string, bool) {
	p.mu.Lock()
	var content string
	found := false
	for _, v := range p.m[name] {
		if v.Active {
			content = v.Content
			found = true
			break
		}
	}
	p.mu.Unlock()
	if !found {
		return "", false
	}
	for k, val := range vars {
		content = strings.ReplaceAll(content, "{{"+k+"}}", val)
		content = strings.ReplaceAll(content, "{{ "+k+" }}", val)
	}
	return content, true
}
