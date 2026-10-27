package billing

import (
	"errors"
	"testing"
	"time"
)

func TestAuditQueryRejectsUTCYearOutsideMySQLRange(t *testing.T) {
	for _, input := range []string{"1000-01-01T00:00:00+14:00", "9999-12-31T23:59:59-14:00"} {
		t.Run(input, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = normalizeAuditQuery(AuditQuery{From: at}, ""); !errors.Is(err, ErrAuditQuery) {
				t.Fatalf("UTC timestamp outside MySQL range accepted: %v", err)
			}
		})
	}
}
