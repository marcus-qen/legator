package auth

// SessionCookieName is the browser cookie used for authenticated web sessions.
const SessionCookieName = "legator_session"

// UserAuthenticator validates username/password credentials.
type UserAuthenticator interface {
	Authenticate(username, password string) (*UserInfo, error)
}

// UserInfo is the authenticated user identity returned by UserAuthenticator.
type UserInfo struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// SessionCreator creates a new session token for the user.
type SessionCreator interface {
	Create(userID string) (token string, err error)
}

// SessionValidator validates an existing session token.
type SessionValidator interface {
	Validate(token string) (*SessionInfo, error)
}

// SessionInfo is the authenticated session identity.
type SessionInfo struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// SessionDeleter invalidates an existing session token.
type SessionDeleter interface {
	Delete(token string) error
}

// UserPermissionResolver resolves role-based permissions for session users.
type UserPermissionResolver interface {
	PermissionsForRole(role string) []Permission
}

// AuthenticatedUser is stored in request context for session-authenticated users.
type AuthenticatedUser struct {
	ID          string       `json:"id"`
	Username    string       `json:"username"`
	DisplayName string       `json:"display_name,omitempty"`
	Role        string       `json:"role"`
	Permissions []Permission `json:"permissions,omitempty"`
}
