---
name: ai-gateway
description: Put an AI gateway in front of the togo AI kit — per-key rate limits & spend caps, response caching, versioned prompt templates, and provider routing/fallback. Use to expose/meter LLM access safely.
---

# togo ai-gateway

Use this skill to gate + meter LLM access in a togo app.

## Wire providers (routing order)
Adapt each togo `ai` driver to the `Provider` interface and register; order is fallback priority:
```go
aigateway.RegisterProvider(primary); aigateway.RegisterProvider(fallback)
```

## Metered keys
```go
g, _ := aigateway.FromKernel(k)
raw, _ := g.CreateKey("svc", aigateway.Key{AllowedModels:[]string{"gpt-4o-mini"}, RatePerMin:60, MonthlyCap:50})
```

## Gated completion
```go
resp, err := g.Complete(ctx, raw, aigateway.Request{Model:"gpt-4o-mini", Messages:[]aigateway.Message{{Role:"user",Content:"Hi"}}})
```
Errors map to HTTP: invalid key 401, model 403, rate 429, quota 402. Identical requests are cached (zero-billed).

## Prompts
`g.Prompts().Set(name, content)` (new active version) · `RenderPrompt(name, vars)` (`{{var}}`) · `Activate(name, version)`.

## Notes
- Set per-model prices: `g.SetPrice(model, aigateway.Price{Input, Output})` (per 1K tokens).
- Stores are in-memory by default; swap the seams for DB persistence in production.
- Use with the `billing` plugin to persist token/cost metering.
