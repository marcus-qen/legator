package auth

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus-qen/legator/internal/controlplane/audit"
)

const sessionMaxAgeSeconds = 86400

// LoginPageOptions configures optional login-page features.
type LoginPageOptions struct {
	OIDCEnabled      bool
	OIDCProviderName string
}

// LoginPageData is the template context for login.html.
type LoginPageData struct {
	Title            string
	Username         string
	Error            string
	OIDCEnabled      bool
	OIDCProviderName string
}

// LoginAuditRecorder records login audit events.
type LoginAuditRecorder interface {
	Record(evt audit.Event)
}

// HandleLoginPage renders the login page.
func HandleLoginPage(templateDir string, opts ...LoginPageOptions) http.HandlerFunc {
	options := resolveLoginOptions(opts...)
	return func(w http.ResponseWriter, r *http.Request) {
		renderLoginPage(w, templateDir, LoginPageData{
			Title:            "Legator Login",
			OIDCEnabled:      options.OIDCEnabled,
			OIDCProviderName: options.OIDCProviderName,
		}, http.StatusOK)
	}
}

// HandleLogin processes a username/password login form (no audit).
func HandleLogin(userAuth UserAuthenticator, sessionCreator SessionCreator, opts ...LoginPageOptions) http.HandlerFunc {
	return HandleLoginWithAudit(userAuth, sessionCreator, nil, opts...)
}

// HandleLoginWithAudit processes a login form and records audit events.
func HandleLoginWithAudit(userAuth UserAuthenticator, sessionCreator SessionCreator, auditor LoginAuditRecorder, opts ...LoginPageOptions) http.HandlerFunc {
	options := resolveLoginOptions(opts...)
	return func(w http.ResponseWriter, r *http.Request) {
		if userAuth == nil || sessionCreator == nil {
			http.Error(w, `{"error":"login unavailable"}`, http.StatusServiceUnavailable)
			return
		}

		templateDir := defaultLoginTemplateDir()

		if err := r.ParseForm(); err != nil {
			renderLoginPage(w, templateDir, LoginPageData{
				Title:            "Legator Login",
				Error:            "Invalid login form",
				OIDCEnabled:      options.OIDCEnabled,
				OIDCProviderName: options.OIDCProviderName,
			}, http.StatusBadRequest)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			renderLoginPage(w, templateDir, LoginPageData{
				Title:            "Legator Login",
				Username:         username,
				Error:            "Username and password are required",
				OIDCEnabled:      options.OIDCEnabled,
				OIDCProviderName: options.OIDCProviderName,
			}, http.StatusUnauthorized)
			return
		}

		user, err := userAuth.Authenticate(username, password)
		if err != nil || user == nil {
			errMsg := "Invalid username or password"
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				errMsg = err.Error()
			}
			if auditor != nil {
				auditor.Record(audit.Event{
					Timestamp: time.Now().UTC(),
					Type:      audit.EventLoginFailed,
					Actor:     username,
					Summary:   "Login failed for " + username + " (local)",
					Detail:    map[string]string{"method": "local", "remote_addr": r.RemoteAddr},
				})
			}
			renderLoginPage(w, templateDir, LoginPageData{
				Title:            "Legator Login",
				Username:         username,
				Error:            errMsg,
				OIDCEnabled:      options.OIDCEnabled,
				OIDCProviderName: options.OIDCProviderName,
			}, http.StatusUnauthorized)
			return
		}

		token, err := sessionCreator.Create(user.ID)
		if err != nil || strings.TrimSpace(token) == "" {
			renderLoginPage(w, templateDir, LoginPageData{
				Title:            "Legator Login",
				Username:         username,
				Error:            "Failed to create session",
				OIDCEnabled:      options.OIDCEnabled,
				OIDCProviderName: options.OIDCProviderName,
			}, http.StatusInternalServerError)
			return
		}

		if auditor != nil {
			auditor.Record(audit.Event{
				Timestamp: time.Now().UTC(),
				Type:      audit.EventLoginSuccess,
				Actor:     user.ID,
				Summary:   "Login succeeded for " + username + " (local)",
				Detail:    map[string]string{"method": "local", "user_id": user.ID, "username": username, "remote_addr": r.RemoteAddr},
			})
		}

		http.SetCookie(w, &http.Cookie{
			Name:     SessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   sessionMaxAgeSeconds,
			Expires:  time.Now().Add(sessionMaxAgeSeconds * time.Second),
		})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// HandleLogout invalidates current session and clears the session cookie.
func HandleLogout(sessionDeleter SessionDeleter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(SessionCookieName); err == nil && cookie != nil && cookie.Value != "" && sessionDeleter != nil {
			_ = sessionDeleter.Delete(cookie.Value)
		}

		http.SetCookie(w, &http.Cookie{
			Name:     SessionCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
			Expires:  time.Unix(0, 0),
		})
		http.Redirect(w, r, "/login", http.StatusFound)
	}
}

// HandleMe returns current auth identity (session user or API key identity).
func HandleMe() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if user := UserFromContext(r.Context()); user != nil {
			_ = json.NewEncoder(w).Encode(user)
			return
		}

		if key := FromContext(r.Context()); key != nil {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":           key.ID,
				"username":     key.Name,
				"display_name": key.Name,
				"role":         "api_key",
				"permissions":  key.Permissions,
			})
			return
		}

		http.Error(w, `{"error":"authentication required"}`, http.StatusUnauthorized)
	}
}

func renderLoginPage(w http.ResponseWriter, templateDir string, data LoginPageData, status int) {
	tmplPath := filepath.Join(templateDir, "login.html")
	tmpl, err := template.ParseFiles(tmplPath)
	if err != nil {
		http.Error(w, "failed to render login page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = tmpl.Execute(w, data)
}

func defaultLoginTemplateDir() string {
	candidates := []string{
		filepath.Join("web", "templates"),
		filepath.Join("..", "..", "..", "web", "templates"),
		filepath.Join("..", "..", "..", "..", "web", "templates"),
	}

	for _, dir := range candidates {
		if _, err := os.Stat(filepath.Join(dir, "login.html")); err == nil {
			return dir
		}
	}
	return filepath.Join("web", "templates")
}

func resolveLoginOptions(opts ...LoginPageOptions) LoginPageOptions {
	if len(opts) == 0 {
		return LoginPageOptions{}
	}
	resolved := opts[0]
	if strings.TrimSpace(resolved.OIDCProviderName) == "" {
		resolved.OIDCProviderName = "OIDC"
	}
	return resolved
}
