<div align="center">
  <img src=".github/assets/togo-mark.svg" alt="togo" height="64" />
  <h1>togo-framework/ai-gateway</h1>
  <p>
    <a href="https://to-go.dev/marketplace"><img src="https://img.shields.io/badge/marketplace-to--go.dev-1FC7DC" alt="marketplace" /></a>
    <a href="https://pkg.go.dev/github.com/togo-framework/ai-gateway"><img src="https://pkg.go.dev/badge/github.com/togo-framework/ai-gateway.svg" alt="pkg.go.dev" /></a>
    <img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT" />
  </p>
  <p><strong>An AI gateway for <a href="https://to-go.dev">togo</a> — rate limits, spend caps, caching, prompt management & provider fallback.</strong></p>
</div>

## Install

```bash
togo install togo-framework/ai-gateway
```

A control plane in front of LLM providers (Cortex-style). Wrap the togo AI kit with **per-key rate limits + spend caps**, **response caching**, **prompt-template management**, and **provider routing/fallback** so you can expose AI safely to many consumers and bill it.

## Quick start

```go
g, _ := aigateway.FromKernel(k)

// Bridge your togo `ai` drivers to the gateway (order = routing priority).
aigateway.RegisterProvider(myOpenAIAdapter)
aigateway.RegisterProvider(myAnthropicAdapter) // fallback if the first errors

// Mint a metered key.
raw, _ := g.CreateKey("mobile-app", aigateway.Key{
    AllowedModels: []string{"gpt-4o-mini"},
    RatePerMin:    60,
    MonthlyCap:    50.00, // USD
})

resp, err := g.Complete(ctx, raw, aigateway.Request{
    Model:    "gpt-4o-mini",
    Messages: []aigateway.Message{{Role: "user", Content: "Hi"}},
})
```

## Features

- **Per-key rate limiting** — sliding window, requests/min (`429` over limit).
- **Spend caps** — monthly + hard caps enforced *before* the provider call (`402` over quota), with a per-model price book and accumulated spend.
- **Response caching** — SHA-256 over `(model | messages | temperature | max_tokens)`, TTL (default 1h); cache hits are returned instantly and **billed at zero**.
- **Prompt management** — named, **versioned** templates with `{{var}}` interpolation (`RenderPrompt`), activate any version.
- **Provider routing + fallback** — tries registered providers in order; falls back to the next on error.
- **API keys** — hashed (SHA-256), per-key allowed-models + limits, revocation; raw key shown once.

## Provider interface

The gateway is decoupled from any SDK — implement a tiny adapter over your togo `ai` driver:

```go
type Provider interface {
    Name() string
    Complete(ctx context.Context, req aigateway.Request) (aigateway.Response, error)
}
```

## REST API

`/complete` reads the key from `Authorization: Bearer <key>`.

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/ai-gateway/complete` | gated completion |
| `GET/POST/DELETE` | `/api/ai-gateway/keys[/{id}]` | API-key management |
| `GET/POST` | `/api/ai-gateway/prompts[/{name}]` | prompt templates + versions |
| `GET` | `/api/ai-gateway/usage?key=` | usage/spend records |

## Configuration

No required env. Set per-model prices with `g.SetPrice(model, aigateway.Price{Input, Output})`; default cache TTL is 1h. Swap the in-memory stores via the exported seams for DB persistence.

---

<div align="center">
  <h3>Premium sponsors</h3>
  <p>
    <a href="https://id8media.com"><strong>ID8 Media</strong></a> &nbsp;·&nbsp;
    <a href="https://one-studio.co"><strong>One Studio</strong></a>
  </p>
  <p><sub>Support togo — <a href="https://github.com/sponsors/fadymondy">become a sponsor</a>.</sub></p>
</div>
