package billing

import "testing"

func TestAffiliateMigrationCheckNormalization(t *testing.T) {
	for table, checks := range affiliateSettingsChecks {
		for name, expression := range checks {
			t.Run(table+"/"+name, func(t *testing.T) {
				if !commerceCheckMatches(currencyMigrationCheck{"CHECK", "YES", expression}, expression) {
					t.Fatal("unrecognized affiliate constraint", expression)
				}
			})
		}
	}
	for _, tc := range []struct {
		name, actual, expected string
		valid                  bool
	}{
		{"nullable override", "((`rate_bp` is null) or ((`rate_bp` >= 0) and (`rate_bp` <= 10000)))", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", true},
		{"charset literals", "(`status` in (_utf8mb4'active',_utf8mb4'suspended'))", "status IN ('active','suspended')", true},
		{"negative adjustment", "((`amount_minor` <= 0) and (`amount_minor` >= -(9007199254740991)))", "amount_minor <= 0 AND amount_minor >= -9007199254740991", true},
		{"scope months", "((`scope` in (_utf8mb4'first_payment',_utf8mb4'all_payments',_utf8mb4'first_months')) and (`scope_months` >= 1) and (`scope_months` <= 120))", "scope IN ('first_payment','all_payments','first_months') AND scope_months >= 1 AND scope_months <= 120", true},
		{"changed rate bound", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10001)", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", false},
		{"changed boolean", "rate_bp IS NULL AND (rate_bp >= 0 AND rate_bp <= 10000)", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", false},
		{"changed grouping", "(rate_bp IS NULL OR rate_bp >= 0) AND rate_bp <= 10000", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", false},
		{"changed literal case", "status IN ('ACTIVE','suspended')", "status IN ('active','suspended')", false},
		{"changed policy", "clawback IN ('reverse_all')", "clawback IN ('reverse_pending','reverse_all')", false},
		{"disabled constraint", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000) OR 1=1", "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commerceCheckMatches(currencyMigrationCheck{"CHECK", "YES", tc.actual}, tc.expected); got != tc.valid {
				t.Fatal("constraint comparison", got, tc)
			}
		})
	}
}

func TestAffiliatePreflightChecksHaveIndependentOwnership(t *testing.T) {
	first := preflightChecks(len(schemaVersions))
	delete(first["affiliate_profiles"], "affiliate_profile_rate")
	second := preflightChecks(len(schemaVersions))
	if second["affiliate_profiles"]["affiliate_profile_rate"] == "" {
		t.Fatal("a schema inspection consumed the shared expected constraints")
	}
}
