package billing

import "testing"

func TestTaxonomyMigrationExactConjunctionChecks(t *testing.T) {
	for _, test := range []struct {
		name, expected, actual string
		match                  bool
	}{
		{"sort_mysql_parentheses", "sort_order >= 0 AND sort_order <= 1000000", "((`sort_order` >= 0) and (`sort_order` <= 1000000))", true},
		{"revision_mysql_parentheses", "revision >= 1 AND revision <= 9007199254740991", "((`revision` >= 1) and (`revision` <= 9007199254740991))", true},
		{"sort_or", "sort_order >= 0 AND sort_order <= 1000000", "sort_order >= 0 OR sort_order <= 1000000", false},
		{"sort_duplicate", "sort_order >= 0 AND sort_order <= 1000000", "sort_order >= 0 AND sort_order >= 0", false},
		{"sort_wrong_cap", "sort_order >= 0 AND sort_order <= 1000000", "sort_order >= 0 AND sort_order <= 1000001", false},
		{"sort_missing_cap", "sort_order >= 0 AND sort_order <= 1000000", "sort_order >= 0", false},
		{"sort_extra_disjunct", "sort_order >= 0 AND sort_order <= 1000000", "(sort_order >= 0 AND sort_order <= 1000000) OR 1=1", false},
		{"revision_or", "revision >= 1 AND revision <= 9007199254740991", "revision >= 1 OR revision <= 9007199254740991", false},
		{"revision_duplicate", "revision >= 1 AND revision <= 9007199254740991", "revision >= 1 AND revision >= 1", false},
		{"revision_wrong_cap", "revision >= 1 AND revision <= 9007199254740991", "revision >= 1 AND revision <= 9007199254740992", false},
		{"revision_missing_cap", "revision >= 1 AND revision <= 9007199254740991", "revision >= 1", false},
		{"wrong_operand", "revision >= 1 AND revision <= 9007199254740991", "prices_revision >= 1 AND prices_revision <= 9007199254740991", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			check := currencyMigrationCheck{kind: "CHECK", enforced: "YES", clause: test.actual}
			if got := currencyMigrationCheckMatches(check, test.expected); got != test.match {
				t.Fatalf("match=%v want=%v", got, test.match)
			}
			check.enforced = "NO"
			if currencyMigrationCheckMatches(check, test.expected) {
				t.Fatal("unenforced constraint accepted")
			}
		})
	}
}
