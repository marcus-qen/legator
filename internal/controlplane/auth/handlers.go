package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HandleListKeys returns all API keys (no hashes).
func HandleListKeys(store *KeyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keys := store.List()
		if keys == nil {
			keys = []APIKey{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys, "total": len(keys)})
	}
}

// HandleCreateKey creates a new API key and returns the plaintext once.
func HandleCreateKey(store *KeyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name        string       `json:"name"`
			Permissions []Permission `json:"permissions"`
			ExpiresIn   string       `json:"expires_in,omitempty"` // e.g. "720h" for 30 days
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}
		if body.Name == "" {
			http.Error(w, `{"error":"name required"}`, http.StatusBadRequest)
			return
		}
		if len(body.Permissions) == 0 {
			http.Error(w, `{"error":"at least one permission required"}`, http.StatusBadRequest)
			return
		}

		var expiresAt *time.Time
		if body.ExpiresIn != "" {
			d, err := time.ParseDuration(body.ExpiresIn)
			if err != nil {
				http.Error(w, fmt.Sprintf(`{"error":"invalid expires_in: %s"}`, err.Error()), http.StatusBadRequest)
				return
			}
			t := time.Now().UTC().Add(d)
			expiresAt = &t
		}

		key, plainKey, err := store.Create(body.Name, body.Permissions, expiresAt)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"key":       key,
			"plain_key": plainKey,
			"warning":   "Store this key securely. It will not be shown again.",
		})
	}
}

// HandleDeleteKey revokes and deletes a key.
func HandleDeleteKey(store *KeyStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			http.Error(w, `{"error":"key id required"}`, http.StatusBadRequest)
			return
		}

		// Revoke first, then delete
		_ = store.Revoke(id)
		if err := store.Delete(id); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
