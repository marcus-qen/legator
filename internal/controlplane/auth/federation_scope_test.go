package auth

import (
	"context"
	"reflect"
	"testing"
)

func TestFederationAccessScopeFromPermissions(t *testing.T) {
	tests := []struct {
		name    string
		perms   []Permission
		want    FederationAccessScope
	}{
		{
			name:  "no grants means unrestricted",
			perms: []Permission{PermFleetRead},
			want:  FederationAccessScope{},
		},
		{
			name: "parses tenant org scope grants",
			perms: []Permission{
				PermFleetRead,
				Permission("tenant:acme"),
				Permission("org:platform"),
				Permission("scope:ops"),
			},
			want: FederationAccessScope{
				TenantIDs: []string{"acme"},
				OrgIDs:    []string{"platform"},
				ScopeIDs:  []string{"ops"},
			},
		},
		{
			name: "wildcard clears dimension restriction",
			perms: []Permission{
				PermFleetRead,
				Permission("tenant:acme"),
				Permission("tenant:*"),
				Permission("scope:prod"),
			},
			want: FederationAccessScope{
				ScopeIDs: []string{"prod"},
			},
		},
		{
			name: "supports federation-prefixed grants",
			perms: []Permission{
				PermFleetRead,
				Permission("federation:tenant:tenant-a"),
				Permission("federation:org:org-a"),
				Permission("federation:scope:scope-a"),
			},
			want: FederationAccessScope{
				TenantIDs: []string{"tenant-a"},
				OrgIDs:    []string{"org-a"},
				ScopeIDs:  []string{"scope-a"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FederationAccessScopeFromPermissions(tt.perms)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("unexpected scope grants: got=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestFederationAccessScopeFromContext_PrefersAPIKey(t *testing.T) {
	ctx := ContextWithAuthenticatedUser(context.Background(), &AuthenticatedUser{
		Username:    "viewer",
		Permissions: []Permission{Permission("scope:user")},
	})
	ctx = ContextWithAPIKey(ctx, &APIKey{
		Name:        "token",
		Permissions: []Permission{Permission("scope:key")},
	})

	got := FederationAccessScopeFromContext(ctx)
	want := FederationAccessScope{ScopeIDs: []string{"key"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected scope grants: got=%+v want=%+v", got, want)
	}
}

func TestContextWithHelpersAllowAuthDetection(t *testing.T) {
	ctx := ContextWithAPIKey(nil, &APIKey{ID: "k1", Permissions: []Permission{PermFleetRead}})
	if !IsAuthenticated(ctx) {
		t.Fatal("expected context with API key to be authenticated")
	}
	if !HasPermissionFromContext(ctx, PermFleetRead) {
		t.Fatal("expected permission from seeded API key")
	}

	ctx = ContextWithAuthenticatedUser(nil, &AuthenticatedUser{ID: "u1", Permissions: []Permission{PermAuditRead}})
	if !IsAuthenticated(ctx) {
		t.Fatal("expected context with user to be authenticated")
	}
	if !HasPermissionFromContext(ctx, PermAuditRead) {
		t.Fatal("expected permission from seeded user")
	}
}
