// Package aigateway is an AI gateway for togo — a control plane in front of LLM
// providers (Cortex-style). It wraps completions with per-key rate limiting and
// spend caps, response caching, prompt-template management, and provider
// routing/fallback, so the AI kit can be exposed safely to many consumers.
//
// Providers implement the small Provider interface (an adapter bridges the togo
// `ai` plugin's drivers), so the gateway stays decoupled and testable offline.
package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/togo-framework/togo"
)

// Errors returned by the gateway.
var (
	ErrKeyInvalid     = errors.New("ai-gateway: invalid or revoked API key")
	ErrRateLimited    = errors.New("ai-gateway: rate limit exceeded")
	ErrQuotaExceeded  = errors.New("ai-gateway: spend quota exceeded")
	ErrModelNotAllowed = errors.New("ai-gateway: model not allowed for this key")
	ErrNoProvider     = errors.New("ai-gateway: no provider could handle the request")
)

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a completion request.
type Request struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// Response is a completion result.
type Response struct {
	Content      string `json:"content"`
	Model        string `json:"model"`
	Provider     string `json:"provider,omitempty"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	Cached       bool   `json:"cached"`
	Cost         float64 `json:"cost"`
}

// Provider is implemented by an LLM backend (or an adapter over the togo ai kit).
type Provider interface {
	Name() string
	Complete(ctx context.Context, req Request) (Response, error)
}

var (
	provMu    sync.RWMutex
	providers []Provider
)

// RegisterProvider adds a provider to the routing chain (order = priority).
func RegisterProvider(p Provider) {
	provMu.Lock()
	providers = append(providers, p)
	provMu.Unlock()
}

// Price is the per-1K-token cost for a model.
type Price struct{ Input, Output float64 }

// Gateway is the runtime stored on the kernel (k.Get("ai-gateway")).
type Gateway struct {
	k        *togo.Kernel
	mu       sync.Mutex
	keys     map[string]*Key // keyed by hash
	limiter  *limiter
	cache    *respCache
	prompts  *promptStore
	prices   map[string]Price
	cacheTTL time.Duration
	usage    []UsageRecord
}

// Key is an API key with per-key limits + accumulated spend.
type Key struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	hash          string
	AllowedModels []string  `json:"allowed_models,omitempty"`
	RatePerMin    int       `json:"rate_per_min"` // 0 = unlimited
	MonthlyCap    float64   `json:"monthly_cap"`  // USD, 0 = unlimited
	HardCap       float64   `json:"hard_cap"`
	Spend         float64   `json:"spend"`
	CreatedAt     time.Time `json:"created_at"`
	Revoked       bool      `json:"revoked"`
}

func (k *Key) allowsModel(model string) bool {
	if len(k.AllowedModels) == 0 {
		return true
	}
	for _, m := range k.AllowedModels {
		if m == model {
			return true
		}
	}
	return false
}

// UsageRecord is one billed request.
type UsageRecord struct {
	KeyID    string    `json:"key_id"`
	Model    string    `json:"model"`
	Input    int       `json:"input_tokens"`
	Output   int       `json:"output_tokens"`
	Cost     float64   `json:"cost"`
	Cached   bool      `json:"cached"`
	At       time.Time `json:"at"`
}

func init() {
	togo.RegisterProviderFunc("ai-gateway", togo.PriorityLate+10, func(k *togo.Kernel) error {
		g := New()
		g.k = k
		k.Set("ai-gateway", g)
		if k.Router != nil {
			g.mountRoutes(k.Router)
		}
		return nil
	})
}

// New builds a Gateway with default pricing + a 1-hour cache.
func New() *Gateway {
	return &Gateway{
		keys:     map[string]*Key{},
		limiter:  newLimiter(),
		cache:    newRespCache(),
		prompts:  newPromptStore(),
		prices:   defaultPrices(),
		cacheTTL: time.Hour,
	}
}

// FromKernel returns the gateway.
func FromKernel(k *togo.Kernel) (*Gateway, bool) {
	v, ok := k.Get("ai-gateway")
	if !ok {
		return nil, false
	}
	g, ok := v.(*Gateway)
	return g, ok
}

func defaultPrices() map[string]Price {
	return map[string]Price{
		"gpt-4o":            {Input: 0.005, Output: 0.015},
		"gpt-4o-mini":       {Input: 0.00015, Output: 0.0006},
		"claude-sonnet-4-5": {Input: 0.003, Output: 0.015},
		"gemini-2.0-flash":  {Input: 0.0001, Output: 0.0004},
	}
}

// SetPrice sets the per-1K-token price for a model.
func (g *Gateway) SetPrice(model string, p Price) {
	g.mu.Lock()
	g.prices[model] = p
	g.mu.Unlock()
}

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateKey mints an API key, returning the raw key once (only its hash is kept).
func (g *Gateway) CreateKey(name string, k Key) (raw string, rec *Key) {
	g.mu.Lock()
	defer g.mu.Unlock()
	raw = "agw_" + hashKey(fmt.Sprintf("%s-%d-%d", name, len(g.keys)+1, time.Now().UnixNano()))[:32]
	k.ID = "key_" + fmt.Sprint(len(g.keys)+1)
	k.Name = name
	k.hash = hashKey(raw)
	k.CreatedAt = time.Now()
	g.keys[k.hash] = &k
	return raw, &k
}

// Keys lists the API keys (without secrets).
func (g *Gateway) Keys() []*Key {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*Key, 0, len(g.keys))
	for _, k := range g.keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Revoke disables a key by id.
func (g *Gateway) Revoke(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, k := range g.keys {
		if k.ID == id {
			k.Revoked = true
			return true
		}
	}
	return false
}

func (g *Gateway) authKey(raw string) (*Key, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k, ok := g.keys[hashKey(raw)]
	if !ok || k.Revoked {
		return nil, false
	}
	return k, true
}

// Prompts exposes the prompt-template store.
func (g *Gateway) Prompts() *promptStore { return g.prompts }

// RenderPrompt renders a stored template's active version with vars.
func (g *Gateway) RenderPrompt(name string, vars map[string]string) (string, bool) {
	return g.prompts.Render(name, vars)
}

// Complete is the gateway entrypoint: authenticate the key, enforce rate limit +
// spend caps, serve from cache, route to a provider (with fallback), bill, cache.
func (g *Gateway) Complete(ctx context.Context, rawKey string, req Request) (Response, error) {
	key, ok := g.authKey(rawKey)
	if !ok {
		return Response{}, ErrKeyInvalid
	}
	if !key.allowsModel(req.Model) {
		return Response{}, ErrModelNotAllowed
	}
	if key.RatePerMin > 0 && !g.limiter.allow(key.ID, key.RatePerMin) {
		return Response{}, ErrRateLimited
	}
	g.mu.Lock()
	overMonthly := key.MonthlyCap > 0 && key.Spend >= key.MonthlyCap
	overHard := key.HardCap > 0 && key.Spend >= key.HardCap
	g.mu.Unlock()
	if overMonthly || overHard {
		return Response{}, ErrQuotaExceeded
	}

	ck := cacheKey(req)
	if resp, ok := g.cache.get(ck); ok {
		resp.Cached = true
		resp.Cost = 0
		g.recordUsage(key.ID, resp)
		return resp, nil
	}

	resp, err := g.route(ctx, req)
	if err != nil {
		return Response{}, err
	}
	resp.Cost = g.price(resp)
	g.mu.Lock()
	key.Spend += resp.Cost
	g.mu.Unlock()
	g.cache.set(ck, resp, g.cacheTTL)
	g.recordUsage(key.ID, resp)
	return resp, nil
}

// route tries each registered provider in order, returning the first success.
func (g *Gateway) route(ctx context.Context, req Request) (Response, error) {
	provMu.RLock()
	ps := append([]Provider{}, providers...)
	provMu.RUnlock()
	if len(ps) == 0 {
		return Response{}, ErrNoProvider
	}
	var lastErr error
	for _, p := range ps {
		resp, err := p.Complete(ctx, req)
		if err == nil {
			resp.Provider = p.Name()
			if resp.Model == "" {
				resp.Model = req.Model
			}
			return resp, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoProvider
	}
	return Response{}, fmt.Errorf("ai-gateway: all providers failed: %w", lastErr)
}

func (g *Gateway) price(r Response) float64 {
	g.mu.Lock()
	p, ok := g.prices[r.Model]
	g.mu.Unlock()
	if !ok {
		return 0
	}
	return float64(r.InputTokens)/1000*p.Input + float64(r.OutputTokens)/1000*p.Output
}

func (g *Gateway) recordUsage(keyID string, r Response) {
	g.mu.Lock()
	g.usage = append(g.usage, UsageRecord{
		KeyID: keyID, Model: r.Model, Input: r.InputTokens, Output: r.OutputTokens,
		Cost: r.Cost, Cached: r.Cached, At: time.Now(),
	})
	if len(g.usage) > 10000 {
		g.usage = g.usage[len(g.usage)-10000:]
	}
	g.mu.Unlock()
}

// Usage returns recorded usage, optionally filtered by key id.
func (g *Gateway) Usage(keyID string) []UsageRecord {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := []UsageRecord{}
	for _, u := range g.usage {
		if keyID == "" || u.KeyID == keyID {
			out = append(out, u)
		}
	}
	return out
}

func cacheKey(req Request) string {
	b, _ := json.Marshal(struct {
		M string
		Msgs []Message
		T float64
		Mx int
	}{req.Model, req.Messages, req.Temperature, req.MaxTokens})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
