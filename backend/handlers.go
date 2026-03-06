package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ============================================================================
// Handler — HTTP API for the proofreading tool
// ============================================================================

type Handler struct {
	store  *Store
	auth   *Auth
	pusher *Pusher
}

func NewHandler(store *Store, auth *Auth, pusher *Pusher) *Handler {
	return &Handler{store: store, auth: auth, pusher: pusher}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/categories", h.requireAuth(h.handleCategories))
	mux.HandleFunc("/api/entries", h.requireAuth(h.handleEntries))
	mux.HandleFunc("/api/entry", h.requireAuth(h.handleUpdateEntry))
	mux.HandleFunc("/api/push", h.requireAuth(h.handlePush))
	mux.HandleFunc("/api/status", h.requireAuth(h.handleStatus))
}

// requireAuth wraps a handler with authentication.
func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		user := h.auth.Verify(token)
		if user == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		r.Header.Set("X-Username", user)
		next(w, r)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if !h.auth.Authenticate(req.Username, req.Password) {
		http.Error(w, `{"error":"invalid credentials"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token":    h.auth.Token(req.Username),
		"username": req.Username,
	})
}

func (h *Handler) handleCategories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.store.GetCategories())
}

func (h *Handler) handleEntries(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	field := r.URL.Query().Get("field")
	source := r.URL.Query().Get("source")

	if category == "" || field == "" {
		http.Error(w, `{"error":"category and field required"}`, http.StatusBadRequest)
		return
	}
	if !IsValidCategory(category) {
		http.Error(w, fmt.Sprintf(`{"error":"unsupported category: %s"}`, category), http.StatusBadRequest)
		return
	}

	entries := h.store.GetEntries(category, field, source)
	if entries == nil {
		entries = []EntryWithKey{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (h *Handler) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Category string `json:"category"`
		Field    string `json:"field"`
		Key      string `json:"key"`
		Text     string `json:"text"`
		Source   string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}
	if !IsValidCategory(req.Category) {
		http.Error(w, `{"error":"unsupported category"}`, http.StatusBadRequest)
		return
	}

	user := r.Header.Get("X-Username")
	status, err := h.store.UpdateEntry(req.Category, req.Field, req.Key, req.Text, req.Source)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}

	if status != "noop" {
		fmt.Printf("[edit] %s/%s: %q -> %q (%s) by %s\n", req.Category, req.Field, req.Key, req.Text, req.Source, user)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	user := r.Header.Get("X-Username")
	if err := h.pusher.PushAll(h.store, user); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.pusher.Status())
}
