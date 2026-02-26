package server

import (
	"fmt"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/session"
	"github.com/marcus-qen/legator/internal/controlplane/users"
)

// userAuthAdapter bridges users.Store → auth.UserAuthenticator.
type userAuthAdapter struct {
	store *users.Store
}

func (a *userAuthAdapter) Authenticate(username, password string) (*auth.UserInfo, error) {
	u, err := a.store.Authenticate(username, password)
	if err != nil {
		return nil, err
	}
	return &auth.UserInfo{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        u.Role,
	}, nil
}

// sessionAdapter bridges session.Store → auth.SessionCreator + auth.SessionValidator + auth.SessionDeleter.
type sessionAdapter struct {
	store     *session.Store
	userStore *users.Store
}

func (a *sessionAdapter) Create(userID string) (string, error) {
	sess, err := a.store.Create(userID)
	if err != nil {
		return "", err
	}
	return sess.ID, nil
}

func (a *sessionAdapter) Validate(token string) (*auth.SessionInfo, error) {
	sess, err := a.store.Validate(token)
	if err != nil {
		return nil, err
	}
	u, err := a.userStore.Get(sess.UserID)
	if err != nil {
		return nil, fmt.Errorf("lookup session user: %w", err)
	}
	if !u.Enabled {
		// Delete the session if the user has been disabled
		_ = a.store.Delete(token)
		return nil, fmt.Errorf("user account disabled")
	}
	return &auth.SessionInfo{
		Token:    sess.ID,
		UserID:   u.ID,
		Username: u.Username,
		Role:     u.Role,
	}, nil
}

func (a *sessionAdapter) Delete(token string) error {
	return a.store.Delete(token)
}

// roleResolver bridges auth.RolePermissions → auth.UserPermissionResolver.
type roleResolver struct{}

func (r *roleResolver) PermissionsForRole(role string) []auth.Permission {
	return auth.RolePermissions(auth.Role(role))
}
