package auth

import (
	"errors"
	"testing"
)

func TestWorkspaceIDFromPermissions(t *testing.T) {
	tests := []struct {
		name    string
		perms   []Permission
		want    string
		wantErr error
	}{
		{
			name:    "missing workspace scope",
			perms:   []Permission{PermAuditRead},
			wantErr: ErrWorkspaceScopeMissing,
		},
		{
			name:  "single workspace scope",
			perms: []Permission{PermCommandExec, Permission("workspace:workspace-a")},
			want:  "workspace-a",
		},
		{
			name:  "wildcard workspace scope",
			perms: []Permission{PermCommandExec, Permission("workspace:*")},
			want:  "*",
		},
		{
			name:  "admin implies wildcard",
			perms: []Permission{PermAdmin},
			want:  "*",
		},
		{
			name:    "ambiguous workspace scope",
			perms:   []Permission{Permission("workspace:a"), Permission("workspace:b")},
			wantErr: ErrWorkspaceScopeAmbiguous,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WorkspaceIDFromPermissions(tt.perms)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("unexpected error: got=%v want=%v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("workspace mismatch: got=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestWorkspaceIDFromContext_PrefersAPIKey(t *testing.T) {
	ctx := ContextWithAuthenticatedUser(nil, &AuthenticatedUser{
		Username:    "operator",
		Permissions: []Permission{Permission("workspace:user")},
	})
	ctx = ContextWithAPIKey(ctx, &APIKey{Permissions: []Permission{Permission("workspace:key")}})

	got, err := WorkspaceIDFromContext(ctx)
	if err != nil {
		t.Fatalf("workspace from context: %v", err)
	}
	if got != "key" {
		t.Fatalf("workspace mismatch: got=%q want=%q", got, "key")
	}
}
