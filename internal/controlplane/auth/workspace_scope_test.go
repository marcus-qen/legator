package auth

import (
	"context"
	"testing"
)

func TestWorkspaceScopeFromContextAPIKeyClaim(t *testing.T) {
	ctx := WithAPIKeyContext(context.Background(), &APIKey{
		ID:          "key-1",
		Permissions: []Permission{"workspace:team-a"},
	})
	scope := WorkspaceScopeFromContext(ctx)
	if !scope.Authenticated {
		t.Fatal("expected authenticated scope")
	}
	if !scope.Restricted {
		t.Fatal("expected restricted scope")
	}
	if scope.WorkspaceID != "team-a" {
		t.Fatalf("expected workspace team-a, got %q", scope.WorkspaceID)
	}
}

func TestWorkspaceScopeFromContextSessionFallback(t *testing.T) {
	ctx := WithUserContext(context.Background(), &AuthenticatedUser{ID: "user-42", Username: "alice"})
	scope := WorkspaceScopeFromContext(ctx)
	if !scope.Authenticated || !scope.Restricted {
		t.Fatalf("expected authenticated restricted scope, got %#v", scope)
	}
	if scope.WorkspaceID != "user-42" {
		t.Fatalf("expected workspace user-42, got %q", scope.WorkspaceID)
	}
}

func TestWorkspaceScopeFromContextWildcardClaim(t *testing.T) {
	ctx := WithAPIKeyContext(context.Background(), &APIKey{
		ID:          "key-1",
		Permissions: []Permission{"workspace:*"},
	})
	scope := WorkspaceScopeFromContext(ctx)
	if !scope.Authenticated {
		t.Fatal("expected authenticated scope")
	}
	if scope.Restricted {
		t.Fatalf("expected unrestricted wildcard scope, got %#v", scope)
	}
}

func TestWorkspaceScopeFromContextUnauthenticated(t *testing.T) {
	scope := WorkspaceScopeFromContext(context.Background())
	if scope.Authenticated || scope.Restricted || scope.WorkspaceID != "" {
		t.Fatalf("expected empty unauthenticated scope, got %#v", scope)
	}
}

func TestWorkspaceScopeFromContextUsesCachedScope(t *testing.T) {
	ctx := WithAPIKeyContext(context.Background(), &APIKey{
		ID:          "key-1",
		Permissions: []Permission{"workspace:team-a"},
	})
	ctx = WithWorkspaceScopeContext(ctx, WorkspaceScope{WorkspaceID: "team-cached", Authenticated: true, Restricted: true})

	scope := WorkspaceScopeFromContext(ctx)
	if scope.WorkspaceID != "team-cached" {
		t.Fatalf("expected cached workspace team-cached, got %q", scope.WorkspaceID)
	}
}
