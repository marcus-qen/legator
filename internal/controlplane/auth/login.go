package auth

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionMaxAgeSeconds = 86400

// LoginPageData is the template context for login.html.
type LoginPageData struct {
	Title    string
	Username string
	Error    string
}

// HandleLoginPage renders the login page.
func HandleLoginPage(templateDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		renderLoginPage(w, templateDir, LoginPageData{Title: "Legator Login"}, http.StatusOK)
	}
}

// HandleLogin processes a username/password login form.
func HandleLogin(userAuth UserAuthenticator, sessionCreator SessionCreator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if userAuth == nil || sessionCreator == nil {
			http.Error(w, `{"error":"login unavailable"}`, http.StatusServiceUnavailable)
			return
		}

		templateDir := defaultLoginTemplateDir()

		if err := r.ParseForm(); err != nil {
			renderLoginPage(w, templateDir, LoginPageData{
				Title: "Legator Login",
				Error: "Invalid login form",
			}, http.StatusBadRequest)
			return
		}

		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			renderLoginPage(w, templateDir, LoginPageData{
				Title:    "Legator Login",
				Username: username,
				Error:    "Username and password are required",
			}, http.StatusUnauthorized)
			return
		}

		user, err := userAuth.Authenticate(username, password)
		if err != nil || user == nil {
			errMsg := "Invalid username or password"
			if err != nil && strings.TrimSpace(err.Error()) != "" {
				errMsg = err.Error()
			}
			renderLoginPage(w, templateDir, LoginPageData{
				Title:    "Legator Login",
				Username: username,
				Error:    errMsg,
			}, http.StatusUnauthorized)
			return
		}

		token, err := sessionCreator.Create(user.ID)
		if err != nil || strings.TrimSpace(token) == "" {
			renderLoginPage(w, templateDir, LoginPageData{
				Title:    "Legator Login",
				Username: username,
				Error:    "Failed to create session",
			}, http.StatusInternalServerError)
			return
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
