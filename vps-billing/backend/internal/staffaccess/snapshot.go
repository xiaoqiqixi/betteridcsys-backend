// Package staffaccess constructs the authorization snapshot used to bind a
// staff session to its complete membership history. It does not authenticate
// a caller or authorize SQL; callers must supply a consistent, untruncated
// database snapshot and check it inside the actual authorized transaction.
package staffaccess

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

const AllCustomers = "all_customers"
const MaxRevision int64 = 9007199254740991

var ErrInvalidSnapshot = errors.New("invalid staff authorization snapshot")

var permissionCatalog = [...]string{
	"audit.read_global", "catalog.read_global", "customers.read_global",
	"invoices.read_global", "orders.read_global", "reports.read_global", "services.read_global",
}

func Catalog() []string { return append([]string{}, permissionCatalog[:]...) }

// MembershipPolicy includes disabled relationships and their current group
// policy. Ordinary group name/revision is deliberately not authorization data.
type MembershipPolicy struct {
	GroupID            string   `json:"group_id"`
	MembershipRevision int64    `json:"membership_revision"`
	MembershipEnabled  bool     `json:"membership_enabled"`
	PolicyRevision     int64    `json:"policy_revision"`
	GroupEnabled       bool     `json:"group_enabled"`
	Scope              string   `json:"scope"`
	Permissions        []string `json:"permissions"`
}

// Snapshot owns its data. Its seal is non-secret, but is not an authentication
// credential: only a server-issued session may supply the stored comparison.
type Snapshot struct {
	seal        string
	permissions []string
}

func NewSnapshot(userID string, memberships []MembershipPolicy) (Snapshot, error) {
	if !lowerHex(userID, 32) {
		return Snapshot{}, ErrInvalidSnapshot
	}
	records := append([]MembershipPolicy{}, memberships...)
	sort.Slice(records, func(i, j int) bool { return records[i].GroupID < records[j].GroupID })
	hash := sha256.New()
	hash.Write([]byte("aster.staff-access/1\x00" + userID + "\x00"))
	encoder := json.NewEncoder(hash)
	effective := make(map[string]bool)
	previousID := ""
	for _, record := range records {
		if !lowerHex(record.GroupID, 32) || record.GroupID == previousID ||
			record.MembershipRevision < 1 || record.MembershipRevision > MaxRevision ||
			record.PolicyRevision < 1 || record.PolicyRevision > MaxRevision || record.Scope != AllCustomers {
			return Snapshot{}, ErrInvalidSnapshot
		}
		previousID = record.GroupID
		record.Permissions = append([]string{}, record.Permissions...)
		sort.Strings(record.Permissions)
		previousPermission := ""
		for _, permission := range record.Permissions {
			index := sort.SearchStrings(permissionCatalog[:], permission)
			if index == len(permissionCatalog) || permissionCatalog[index] != permission || permission == previousPermission {
				return Snapshot{}, ErrInvalidSnapshot
			}
			previousPermission = permission
			if record.MembershipEnabled && record.GroupEnabled {
				effective[permission] = true
			}
		}
		// Disabled memberships are tombstones, not absent memberships. Omitting
		// them would let an old zero-grant session revive after add/remove.
		if err := encoder.Encode(record); err != nil {
			return Snapshot{}, ErrInvalidSnapshot
		}
	}
	grants := make([]string, 0, len(effective))
	for _, permission := range permissionCatalog {
		if effective[permission] {
			grants = append(grants, permission)
		}
	}
	return Snapshot{seal: hex.EncodeToString(hash.Sum(nil)), permissions: grants}, nil
}

func (s Snapshot) Seal() string { return s.seal }

func (s Snapshot) Permissions() []string { return append([]string{}, s.permissions...) }

func (s Snapshot) Allows(permission string) bool {
	for _, candidate := range s.permissions {
		if permission == candidate {
			return true
		}
	}
	return false
}

func (s Snapshot) MatchesSeal(issuedSeal string) bool {
	return lowerHex(issuedSeal, 64) && lowerHex(s.seal, 64) &&
		subtle.ConstantTimeCompare([]byte(s.seal), []byte(issuedSeal)) == 1
}

func lowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
