package staffaccess_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"aster/billing/internal/staffaccess"
)

const staffID = "00000000000000000000000000000700"

func TestEmptySnapshotHasNoImplicitPermissionAndBindsIdentity(t *testing.T) {
	snapshot, err := staffaccess.NewSnapshot(staffID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Permissions()) != 0 || snapshot.Allows("orders.read_global") || snapshot.Allows("*") {
		t.Fatal("a staff identity without memberships gained permission")
	}
	if len(snapshot.Seal()) != 64 || !snapshot.MatchesSeal(snapshot.Seal()) {
		t.Fatal("a valid snapshot did not match its issued seal")
	}
	other, err := staffaccess.NewSnapshot("00000000000000000000000000000701", nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.MatchesSeal(snapshot.Seal()) {
		t.Fatal("a session seal was transferable to another staff identity")
	}
	var zero staffaccess.Snapshot
	if zero.MatchesSeal(snapshot.Seal()) || zero.MatchesSeal("") || zero.Allows("orders.read_global") {
		t.Fatal("an uninitialized snapshot authorized a request")
	}
}

func mustSnapshot(t *testing.T, memberships ...staffaccess.MembershipPolicy) staffaccess.Snapshot {
	t.Helper()
	snapshot, err := staffaccess.NewSnapshot(staffID, memberships)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestVersionOneSealKnownVectors(t *testing.T) {
	// Independently worked with Python hashlib and compact JSON, not the Go
	// implementation. Persisted seals must be stable across binary upgrades.
	if got := mustSnapshot(t).Seal(); got != "c1fb9447effa66d4fa5d2a3d5c278d65a3ff68fb6e95aff4bf32d1b84eef0c6c" {
		t.Fatalf("empty version-one seal changed: %s", got)
	}
	policy := membership("00000000000000000000000000000001", "orders.read_global", "audit.read_global")
	if got := mustSnapshot(t, policy).Seal(); got != "dfcbecf189d4fa76a8965495203ebcdad71f1af389106a16dc495122652ea6c4" {
		t.Fatalf("version-one policy seal changed: %s", got)
	}
}

func TestRemovedMembershipCannotReviveZeroGroupSession(t *testing.T) {
	old := mustSnapshot(t)
	policy := membership("00000000000000000000000000000001", "orders.read_global")
	if mustSnapshot(t, policy).MatchesSeal(old.Seal()) {
		t.Fatal("joining a group preserved the zero-group session")
	}
	policy.MembershipEnabled = false
	policy.MembershipRevision = 2
	removed := mustSnapshot(t, policy)
	if removed.MatchesSeal(old.Seal()) || len(removed.Permissions()) != 0 {
		t.Fatal("removing the group revived the original zero-group session")
	}
	policy.MembershipEnabled = true
	policy.MembershipRevision = 3
	if mustSnapshot(t, policy).MatchesSeal(removed.Seal()) {
		t.Fatal("rejoining revived a removed-membership session")
	}
}

func TestDisableEnableDisableCannotReviveOldGroupPolicy(t *testing.T) {
	policy := membership("00000000000000000000000000000001", "orders.read_global")
	policy.GroupEnabled = false
	old := mustSnapshot(t, policy)
	policy.GroupEnabled = true
	policy.PolicyRevision = 2
	enabled := mustSnapshot(t, policy)
	if enabled.MatchesSeal(old.Seal()) || !enabled.Allows("orders.read_global") {
		t.Fatal("enabled policy reused a disabled session seal or failed to grant")
	}
	policy.GroupEnabled = false
	policy.PolicyRevision = 3
	final := mustSnapshot(t, policy)
	if final.MatchesSeal(old.Seal()) || final.MatchesSeal(enabled.Seal()) || final.Allows("orders.read_global") {
		t.Fatal("disabling again revived a previous session or retained its grant")
	}
}

func TestRemovedMembershipStillBindsLaterGroupPolicy(t *testing.T) {
	policy := membership("00000000000000000000000000000001", "orders.read_global")
	policy.MembershipEnabled = false
	old := mustSnapshot(t, policy)
	policy.PolicyRevision++
	policy.Permissions = []string{"audit.read_global"}
	current := mustSnapshot(t, policy)
	if current.MatchesSeal(old.Seal()) || len(current.Permissions()) != 0 {
		t.Fatal("inactive historical membership dropped policy history or granted access")
	}
}

func TestRestoringPermissionsDoesNotRestoreOldSession(t *testing.T) {
	policy := membership("00000000000000000000000000000001", "orders.read_global")
	old := mustSnapshot(t, policy)
	policy.Permissions = nil
	policy.PolicyRevision = 2
	if mustSnapshot(t, policy).MatchesSeal(old.Seal()) {
		t.Fatal("permission removal did not revoke the issued seal")
	}
	policy.Permissions = []string{"orders.read_global"}
	policy.PolicyRevision = 3
	if mustSnapshot(t, policy).MatchesSeal(old.Seal()) {
		t.Fatal("restoring the grant revived an old session")
	}
}

func TestAll205MembershipsAreBoundAndOrderingIsIrrelevant(t *testing.T) {
	policies := make([]staffaccess.MembershipPolicy, 205)
	for i := range policies {
		policies[i] = membership(fmt.Sprintf("%032x", i+1), "orders.read_global")
	}
	old := mustSnapshot(t, policies...)
	policies[204].MembershipRevision++
	if mustSnapshot(t, policies...).MatchesSeal(old.Seal()) {
		t.Fatal("last membership was truncated after the old 64/100-item ceilings")
	}
	policies[204].MembershipRevision--
	for i, j := 0, len(policies)-1; i < j; i, j = i+1, j-1 {
		policies[i], policies[j] = policies[j], policies[i]
	}
	if !mustSnapshot(t, policies...).MatchesSeal(old.Seal()) {
		t.Fatal("storage query ordering changed authorization")
	}
}

func TestSnapshotOwnsInputsAndReturnedPermissions(t *testing.T) {
	policies := []staffaccess.MembershipPolicy{
		membership("00000000000000000000000000000002", "orders.read_global", "audit.read_global"),
		membership("00000000000000000000000000000001", "services.read_global"),
	}
	snapshot := mustSnapshot(t, policies...)
	if policies[0].GroupID != "00000000000000000000000000000002" || policies[0].Permissions[0] != "orders.read_global" {
		t.Fatal("constructor mutated caller input ordering")
	}
	seal := snapshot.Seal()
	policies[0].Permissions[0] = "customers.read_global"
	policies[1].GroupEnabled = false
	returned := snapshot.Permissions()
	returned[0] = "customers.read_global"
	if !snapshot.MatchesSeal(seal) || snapshot.Allows("customers.read_global") || !snapshot.Allows("services.read_global") {
		t.Fatal("caller mutation changed an issued authorization snapshot")
	}
}

func TestNilAndEmptyGrantsHaveSameSeal(t *testing.T) {
	policy := membership("00000000000000000000000000000001")
	old := mustSnapshot(t, policy)
	policy.Permissions = []string{}
	if !mustSnapshot(t, policy).MatchesSeal(old.Seal()) {
		t.Fatal("nil and empty grant storage representations differed")
	}
}

func TestExactClosedPermissionCatalog(t *testing.T) {
	want := []string{"audit.read_global", "catalog.read_global", "customers.read_global", "invoices.read_global", "orders.read_global", "reports.read_global", "services.read_global"}
	catalog := staffaccess.Catalog()
	if !reflect.DeepEqual(catalog, want) {
		t.Fatalf("catalog=%v, want=%v", catalog, want)
	}
	catalog[0] = "*"
	if !reflect.DeepEqual(staffaccess.Catalog(), want) {
		t.Fatal("a caller rewrote the global permission catalog")
	}
	policy := membership("00000000000000000000000000000001", want...)
	snapshot := mustSnapshot(t, policy)
	for _, permission := range want {
		if !snapshot.Allows(permission) {
			t.Fatalf("explicit catalog permission rejected: %s", permission)
		}
	}
}

func TestInvalidIdentityAndSessionSealFailClosed(t *testing.T) {
	for _, id := range []string{"", "1", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("0", 31), staffID + " ", " " + staffID} {
		t.Run("identity-"+id, func(t *testing.T) {
			snapshot, err := staffaccess.NewSnapshot(id, nil)
			if !errors.Is(err, staffaccess.ErrInvalidSnapshot) || snapshot.Seal() != "" || len(snapshot.Permissions()) != 0 {
				t.Fatal("malformed staff identity yielded a usable snapshot")
			}
		})
	}
	snapshot := mustSnapshot(t)
	for _, seal := range []string{"", strings.Repeat("g", 64), strings.ToUpper(snapshot.Seal()), snapshot.Seal() + " ", snapshot.Seal()[:63], strings.Repeat("0", 64)} {
		t.Run("seal-"+seal, func(t *testing.T) {
			if snapshot.MatchesSeal(seal) {
				t.Fatal("malformed or foreign issued seal was accepted")
			}
		})
	}
}

func TestInvalidPoliciesRejectEntireSnapshotIncludingDisabledRows(t *testing.T) {
	valid := membership("00000000000000000000000000000001", "orders.read_global")
	mutations := map[string]func(*staffaccess.MembershipPolicy){
		"group-id":            func(p *staffaccess.MembershipPolicy) { p.GroupID = "unknown" },
		"uppercase-group":     func(p *staffaccess.MembershipPolicy) { p.GroupID = strings.Repeat("A", 32) },
		"zero-membership":     func(p *staffaccess.MembershipPolicy) { p.MembershipRevision = 0 },
		"negative-membership": func(p *staffaccess.MembershipPolicy) { p.MembershipRevision = -1 },
		"overflow-membership": func(p *staffaccess.MembershipPolicy) { p.MembershipRevision = staffaccess.MaxRevision + 1 },
		"zero-policy":         func(p *staffaccess.MembershipPolicy) { p.PolicyRevision = 0 },
		"overflow-policy":     func(p *staffaccess.MembershipPolicy) { p.PolicyRevision = staffaccess.MaxRevision + 1 },
		"wrong-scope":         func(p *staffaccess.MembershipPolicy) { p.Scope = "assigned_customers" },
		"wildcard-scope":      func(p *staffaccess.MembershipPolicy) { p.Scope = "*" },
		"unknown-permission":  func(p *staffaccess.MembershipPolicy) { p.Permissions = []string{"users.manage"} },
		"wildcard-permission": func(p *staffaccess.MembershipPolicy) { p.Permissions = []string{"*"} },
		"trailing-space":      func(p *staffaccess.MembershipPolicy) { p.Permissions = []string{"orders.read_global "} },
		"duplicate-permission": func(p *staffaccess.MembershipPolicy) {
			p.Permissions = []string{"orders.read_global", "orders.read_global"}
		},
	}
	for name, mutate := range mutations {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enabled=%t", name, enabled), func(t *testing.T) {
				invalid := valid
				invalid.GroupID = "00000000000000000000000000000002"
				invalid.MembershipEnabled = enabled
				invalid.GroupEnabled = enabled
				mutate(&invalid)
				snapshot, err := staffaccess.NewSnapshot(staffID, []staffaccess.MembershipPolicy{valid, invalid})
				if !errors.Is(err, staffaccess.ErrInvalidSnapshot) || snapshot.Seal() != "" || len(snapshot.Permissions()) != 0 {
					t.Fatal("invalid policy produced partial or silently filtered authorization")
				}
			})
		}
	}
	if _, err := staffaccess.NewSnapshot(staffID, []staffaccess.MembershipPolicy{valid, valid}); !errors.Is(err, staffaccess.ErrInvalidSnapshot) {
		t.Fatal("duplicate membership was accepted")
	}
	valid.PolicyRevision = staffaccess.MaxRevision
	valid.MembershipRevision = staffaccess.MaxRevision
	if !mustSnapshot(t, valid).Allows("orders.read_global") {
		t.Fatal("maximum exact browser revision was rejected")
	}
}

func membership(id string, permissions ...string) staffaccess.MembershipPolicy {
	return staffaccess.MembershipPolicy{
		GroupID: id, MembershipRevision: 1, MembershipEnabled: true,
		PolicyRevision: 1, GroupEnabled: true, Scope: staffaccess.AllCustomers,
		Permissions: permissions,
	}
}

func TestEffectivePermissionsRequireBothEnabledSides(t *testing.T) {
	active := membership("00000000000000000000000000000001", "orders.read_global", "customers.read_global")
	removed := membership("00000000000000000000000000000002", "audit.read_global")
	removed.MembershipEnabled = false
	disabled := membership("00000000000000000000000000000003", "services.read_global")
	disabled.GroupEnabled = false
	second := membership("00000000000000000000000000000004", "reports.read_global", "orders.read_global")
	snapshot, err := staffaccess.NewSnapshot(staffID, []staffaccess.MembershipPolicy{second, removed, active, disabled})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"customers.read_global", "orders.read_global", "reports.read_global"}; !reflect.DeepEqual(snapshot.Permissions(), want) {
		t.Fatalf("permissions=%v, want=%v", snapshot.Permissions(), want)
	}
	for _, denied := range []string{"audit.read_global", "services.read_global", "*", "orders.write", "orders.read_global "} {
		if snapshot.Allows(denied) {
			t.Fatalf("granted absent or non-exact permission %q", denied)
		}
	}
	if !snapshot.Allows("orders.read_global") {
		t.Fatal("active explicit grant was not effective")
	}
}
