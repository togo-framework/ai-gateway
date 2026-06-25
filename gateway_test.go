package aigateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type fakeProvider struct {
	name    string
	fail    bool
	calls   int32
	in, out int
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Complete(ctx context.Context, req Request) (Response, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.fail {
		return Response{}, errors.New("provider down")
	}
	return Response{Content: "hi from " + f.name, Model: req.Model, InputTokens: f.in, OutputTokens: f.out}, nil
}

func setProviders(ps ...Provider) {
	provMu.Lock()
	providers = ps
	provMu.Unlock()
}

func sampleReq() Request {
	return Request{Model: "gpt-4o-mini", Messages: []Message{{Role: "user", Content: "hi"}}}
}

func TestInvalidKey(t *testing.T) {
	setProviders(&fakeProvider{name: "p"})
	g := New()
	if _, err := g.Complete(context.Background(), "nope", sampleReq()); err != ErrKeyInvalid {
		t.Fatalf("got %v, want ErrKeyInvalid", err)
	}
}

func TestRateLimit(t *testing.T) {
	setProviders(&fakeProvider{name: "p"})
	g := New()
	raw, _ := g.CreateKey("t", Key{RatePerMin: 2})
	ctx := context.Background()
	if _, err := g.Complete(ctx, raw, sampleReq()); err != nil {
		t.Fatalf("call 1: %v", err)
	}
	// vary the request so the cache doesn't absorb call 2/3
	r2 := sampleReq()
	r2.Messages[0].Content = "two"
	if _, err := g.Complete(ctx, raw, r2); err != nil {
		t.Fatalf("call 2: %v", err)
	}
	r3 := sampleReq()
	r3.Messages[0].Content = "three"
	if _, err := g.Complete(ctx, raw, r3); err != ErrRateLimited {
		t.Fatalf("call 3 = %v, want ErrRateLimited", err)
	}
}

func TestSpendCap(t *testing.T) {
	setProviders(&fakeProvider{name: "p", in: 1000, out: 1000})
	g := New()
	g.SetPrice("m", Price{Input: 1, Output: 1}) // $2 per call (1000+1000 tokens)
	raw, _ := g.CreateKey("t", Key{MonthlyCap: 1.5})
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "x"}}}
	if _, err := g.Complete(context.Background(), raw, req); err != nil {
		t.Fatalf("first call should pass: %v", err)
	}
	req2 := req
	req2.Messages = []Message{{Role: "user", Content: "y"}}
	if _, err := g.Complete(context.Background(), raw, req2); err != ErrQuotaExceeded {
		t.Fatalf("second call = %v, want ErrQuotaExceeded", err)
	}
}

func TestCacheHitIsZeroBilled(t *testing.T) {
	fp := &fakeProvider{name: "p", in: 100, out: 100}
	setProviders(fp)
	g := New()
	raw, key := g.CreateKey("t", Key{})
	req := sampleReq()
	if _, err := g.Complete(context.Background(), raw, req); err != nil {
		t.Fatal(err)
	}
	spendAfterFirst := key.Spend
	resp, err := g.Complete(context.Background(), raw, req) // identical → cache hit
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Cached || resp.Cost != 0 {
		t.Fatalf("expected cached+zero-cost, got %+v", resp)
	}
	if atomic.LoadInt32(&fp.calls) != 1 {
		t.Fatalf("provider called %d times; cache miss", fp.calls)
	}
	if key.Spend != spendAfterFirst {
		t.Fatalf("cache hit changed spend: %v -> %v", spendAfterFirst, key.Spend)
	}
}

func TestModelNotAllowed(t *testing.T) {
	setProviders(&fakeProvider{name: "p"})
	g := New()
	raw, _ := g.CreateKey("t", Key{AllowedModels: []string{"only-this"}})
	if _, err := g.Complete(context.Background(), raw, sampleReq()); err != ErrModelNotAllowed {
		t.Fatalf("got %v, want ErrModelNotAllowed", err)
	}
}

func TestFallbackRouting(t *testing.T) {
	primary := &fakeProvider{name: "down", fail: true}
	secondary := &fakeProvider{name: "up"}
	setProviders(primary, secondary)
	g := New()
	raw, _ := g.CreateKey("t", Key{})
	resp, err := g.Complete(context.Background(), raw, sampleReq())
	if err != nil {
		t.Fatalf("fallback should succeed: %v", err)
	}
	if resp.Provider != "up" {
		t.Fatalf("expected fallback to 'up', got %q", resp.Provider)
	}
	if atomic.LoadInt32(&primary.calls) != 1 || atomic.LoadInt32(&secondary.calls) != 1 {
		t.Fatalf("calls: primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestPromptVersioningAndRender(t *testing.T) {
	g := New()
	g.Prompts().Set("greet", "Hi {{name}}")
	got, ok := g.RenderPrompt("greet", map[string]string{"name": "Sam"})
	if !ok || got != "Hi Sam" {
		t.Fatalf("v1 render = %q,%v", got, ok)
	}
	g.Prompts().Set("greet", "Hello {{ name }}!") // v2 becomes active
	got, _ = g.RenderPrompt("greet", map[string]string{"name": "Sam"})
	if got != "Hello Sam!" {
		t.Fatalf("v2 render = %q", got)
	}
	if !g.Prompts().Activate("greet", 1) {
		t.Fatal("activate v1 failed")
	}
	got, _ = g.RenderPrompt("greet", map[string]string{"name": "Sam"})
	if got != "Hi Sam" {
		t.Fatalf("after reactivating v1 = %q", got)
	}
	if len(g.Prompts().Versions("greet")) != 2 {
		t.Fatalf("expected 2 versions")
	}
}
