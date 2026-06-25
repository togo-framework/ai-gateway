# ai-gateway — usage

## Register providers (routing order)
Implement the `Provider` interface (an adapter over a togo `ai` driver) and register:
```go
aigateway.RegisterProvider(primary)   // tried first
aigateway.RegisterProvider(fallback)  // used if primary errors
```

## Mint a metered key
```go
g, _ := aigateway.FromKernel(k)
raw, rec := g.CreateKey("svc", aigateway.Key{AllowedModels:[]string{"gpt-4o-mini"}, RatePerMin:60, MonthlyCap:50})
// `raw` is shown once — store it. Only its hash is kept.
```

## Gated completion
```go
resp, err := g.Complete(ctx, raw, aigateway.Request{Model:"gpt-4o-mini", Messages:[]aigateway.Message{{Role:"user", Content:"Hi"}}})
// errors: ErrKeyInvalid(401) ErrModelNotAllowed(403) ErrRateLimited(429) ErrQuotaExceeded(402)
```
Identical requests are served from cache (`resp.Cached`, `resp.Cost == 0`).

## Prompt templates (versioned)
```go
g.Prompts().Set("greeting", "Hi {{name}}, welcome to {{app}}") // v1 active
out, _ := g.RenderPrompt("greeting", map[string]string{"name":"Sam","app":"togo"})
g.Prompts().Set("greeting", "Hello {{name}}")  // v2 active
g.Prompts().Activate("greeting", 1)            // roll back
```

## Pricing & usage
```go
g.SetPrice("my-model", aigateway.Price{Input:0.001, Output:0.002}) // per 1K tokens
g.Usage("")        // all records; or g.Usage(keyID)
```

## REST
| Method | Path |
|---|---|
| POST | `/api/ai-gateway/complete` (Authorization: Bearer <key>) |
| GET/POST/DELETE | `/api/ai-gateway/keys[/{id}]` |
| GET/POST | `/api/ai-gateway/prompts[/{name}]` |
| GET | `/api/ai-gateway/usage?key=` |
