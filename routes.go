package aigateway

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (g *Gateway) mountRoutes(r chi.Router) {
	r.Route("/api/ai-gateway", func(r chi.Router) {
		// API keys
		r.Get("/keys", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, g.Keys())
		})
		r.Post("/keys", func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				Name          string   `json:"name"`
				AllowedModels []string `json:"allowed_models"`
				RatePerMin    int      `json:"rate_per_min"`
				MonthlyCap    float64  `json:"monthly_cap"`
				HardCap       float64  `json:"hard_cap"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			raw, rec := g.CreateKey(body.Name, Key{
				AllowedModels: body.AllowedModels, RatePerMin: body.RatePerMin,
				MonthlyCap: body.MonthlyCap, HardCap: body.HardCap,
			})
			writeJSON(w, 201, map[string]any{"key": raw, "id": rec.ID, "note": "store the key now — it is shown only once"})
		})
		r.Delete("/keys/{id}", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, map[string]bool{"ok": g.Revoke(chi.URLParam(req, "id"))})
		})

		// Prompt templates
		r.Get("/prompts", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, g.prompts.Names())
		})
		r.Get("/prompts/{name}", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, g.prompts.Versions(chi.URLParam(req, "name")))
		})
		r.Post("/prompts/{name}", func(w http.ResponseWriter, req *http.Request) {
			var body struct{ Content string `json:"content"` }
			_ = json.NewDecoder(req.Body).Decode(&body)
			writeJSON(w, 201, map[string]int{"version": g.prompts.Set(chi.URLParam(req, "name"), body.Content)})
		})

		// Usage
		r.Get("/usage", func(w http.ResponseWriter, req *http.Request) {
			writeJSON(w, 200, g.Usage(req.URL.Query().Get("key")))
		})

		// Completion (key via Authorization: Bearer <key>)
		r.Post("/complete", func(w http.ResponseWriter, req *http.Request) {
			var body Request
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				writeJSON(w, 400, map[string]string{"error": "bad request"})
				return
			}
			key := bearer(req)
			resp, err := g.Complete(req.Context(), key, body)
			if err != nil {
				writeJSON(w, statusFor(err), map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, 200, resp)
		})
	})
}

func bearer(req *http.Request) string {
	h := req.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return req.Header.Get("X-Api-Key")
}

func statusFor(err error) int {
	switch err {
	case ErrKeyInvalid:
		return 401
	case ErrModelNotAllowed:
		return 403
	case ErrRateLimited:
		return 429
	case ErrQuotaExceeded:
		return 402
	default:
		return 502
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
