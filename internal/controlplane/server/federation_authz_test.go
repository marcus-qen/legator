package server

import (
	"reflect"
	"testing"

	"github.com/marcus-qen/legator/internal/controlplane/auth"
	"github.com/marcus-qen/legator/internal/controlplane/fleet"
)

func TestApplyFederationAccessFilter(t *testing.T) {
	requested := fleet.FederationFilter{TenantID: "Tenant-A", OrgID: "Org-A", ScopeID: "Scope-A"}
	access := auth.FederationAccessScope{
		TenantIDs: []string{"tenant-a"},
		OrgIDs:    []string{"org-a"},
		ScopeIDs:  []string{"scope-a"},
	}

	effective, err := applyFederationAccessFilter(requested, access)
	if err != nil {
		t.Fatalf("unexpected authz error: %v", err)
	}
	if effective.TenantID != "tenant-a" || effective.OrgID != "org-a" || effective.ScopeID != "scope-a" {
		t.Fatalf("expected normalized requested scope values, got %+v", effective)
	}
	if !reflect.DeepEqual(effective.AllowedTenantIDs, []string{"tenant-a"}) || !reflect.DeepEqual(effective.AllowedOrgIDs, []string{"org-a"}) || !reflect.DeepEqual(effective.AllowedScopeIDs, []string{"scope-a"}) {
		t.Fatalf("expected allowed scope lists to be populated, got %+v", effective)
	}
}

func TestApplyFederationAccessFilter_DeniesUnauthorizedScope(t *testing.T) {
	requested := fleet.FederationFilter{TenantID: "tenant-b", ScopeID: "scope-b"}
	access := auth.FederationAccessScope{
		TenantIDs: []string{"tenant-a"},
		ScopeIDs:  []string{"scope-a"},
	}

	_, err := applyFederationAccessFilter(requested, access)
	if err == nil {
		t.Fatal("expected scope authorization error")
	}
	if err.dimension != "tenant" {
		t.Fatalf("expected tenant denial first, got %+v", err)
	}
}

func TestApplyFederationAccessFilter_UsesAllowedListsWhenRequestUnscoped(t *testing.T) {
	effective, err := applyFederationAccessFilter(fleet.FederationFilter{}, auth.FederationAccessScope{
		TenantIDs: []string{"tenant-a", "tenant-b", "tenant-a"},
		ScopeIDs:  []string{"scope-a", "scope-b"},
	})
	if err != nil {
		t.Fatalf("unexpected authz error: %v", err)
	}
	if len(effective.AllowedTenantIDs) != 2 || effective.AllowedTenantIDs[0] != "tenant-a" || effective.AllowedTenantIDs[1] != "tenant-b" {
		t.Fatalf("unexpected allowed tenant list: %+v", effective.AllowedTenantIDs)
	}
	if len(effective.AllowedScopeIDs) != 2 || effective.AllowedScopeIDs[0] != "scope-a" || effective.AllowedScopeIDs[1] != "scope-b" {
		t.Fatalf("unexpected allowed scope list: %+v", effective.AllowedScopeIDs)
	}
}
