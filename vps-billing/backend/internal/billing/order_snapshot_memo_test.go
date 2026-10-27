package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

const orderMemoCNY = `{"code":"CNY","name":"Archived yuan","prefix":"¥","suffix":" CNY","decimal_places":2}`

// Keep the original decoder as the oracle: reuse must preserve its exact
// historical-data contract, including errors and decoded Unicode text.
func assertOrderMemoDecode(t *testing.T, memo *currencySnapshotMemo, raw []byte, code string) (CurrencyFormat, error) {
	t.Helper()
	want, wantErr := decodeCurrencySnapshot(raw, code)
	// A formatted snapshot copies the byte values before the call; a shallow
	// struct copy would miss an erroneous in-place write to the memo's buffer.
	before := fmt.Sprintf("%#v", *memo)
	got, err := memo.decode(raw, code)
	if got != want || (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
		t.Fatalf("memo differs from strict decoder: got %#v, %v; want %#v, %v", got, err, want, wantErr)
	}
	if err != nil && fmt.Sprintf("%#v", *memo) != before {
		t.Fatal("failed decode published or modified a cached success")
	}
	return got, err
}

func TestOrderSnapshotMemoStrictDecoderEquivalence(t *testing.T) {
	tests := []struct {
		name, raw, code string
		valid           bool
	}{
		{"valid", orderMemoCNY, "CNY", true},
		{"historical_unconfigured_currency", `{"code":"ZZZ","name":"Retired currency","prefix":"old ","suffix":" archive","decimal_places":4}`, "ZZZ", true},
		{"historical_zero_precision", `{"code":"JPY","name":"Historical yen","prefix":"","suffix":" JPY","decimal_places":0}`, "JPY", true},
		{"historical_three_precision", `{"code":"KWD","name":"Historical dinar","prefix":"","suffix":" KWD","decimal_places":3}`, "KWD", true},
		{"maximum_precision", strings.Replace(orderMemoCNY, `:2}`, `:6}`, 1), "CNY", true},
		{"unicode_values_preserved", `{"code":"CNY","name":"历史币种 é","prefix":"￥","suffix":" 元","decimal_places":2}`, "CNY", true},
		{"escaped_exact_keys", `{"c\u006fde":"CNY","name":"Archived yuan","prefix":"¥","suffix":" CNY","\u0064ecimal_places":2}`, "CNY", true},
		{"reordered_whitespace", " {\n\"decimal_places\":2,\"suffix\":\" CNY\",\"prefix\":\"¥\",\"name\":\"Archived yuan\",\"code\":\"CNY\"} \n", "CNY", true},
		// The existing decoder applies encoding/json's last-value behavior for
		// duplicate exact keys. Memoization must not change that contract.
		{"duplicate_exact_key_compatibility", strings.Replace(orderMemoCNY, `"decimal_places":2`, `"decimal_places":9,"decimal_places":2`, 1), "CNY", true},
		{"negative_precision", strings.Replace(orderMemoCNY, `:2}`, `:-1}`, 1), "CNY", false},
		{"precision_seven", strings.Replace(orderMemoCNY, `:2}`, `:7}`, 1), "CNY", false},
		{"precision_nine", strings.Replace(orderMemoCNY, `:2}`, `:9}`, 1), "CNY", false},
		{"fractional_precision", strings.Replace(orderMemoCNY, `:2}`, `:2.5}`, 1), "CNY", false},
		{"string_precision", strings.Replace(orderMemoCNY, `:2}`, `:"2"}`, 1), "CNY", false},
		{"extra_field", strings.Replace(orderMemoCNY, `:2}`, `:2,"future":true}`, 1), "CNY", false},
		{"uppercase_required_key", strings.Replace(orderMemoCNY, `"code"`, `"Code"`, 1), "CNY", false},
		{"extra_case_variant", strings.Replace(orderMemoCNY, `:2}`, `:2,"Code":"CNY"}`, 1), "CNY", false},
		{"unicode_lookalike_key", strings.Replace(orderMemoCNY, `"code"`, `"cоde"`, 1), "CNY", false},
		{"unicode_extra_key", strings.Replace(orderMemoCNY, `:2}`, `:2,"\u0063ode_extra":"CNY"}`, 1), "CNY", false},
		{"empty_name", strings.Replace(orderMemoCNY, `Archived yuan`, "", 1), "CNY", false},
		{"lowercase_snapshot_code", strings.Replace(orderMemoCNY, `CNY`, "cny", 1), "cny", false},
		{"actual_currency_mismatch", orderMemoCNY, "USD", false},
		{"actual_currency_lowercase", orderMemoCNY, "cny", false},
		{"actual_currency_empty", orderMemoCNY, "", false},
		{"empty", "", "CNY", false},
		{"null", "null", "CNY", false},
		{"empty_object", "{}", "CNY", false},
		{"array", "[]", "CNY", false},
		{"truncated", orderMemoCNY[:len(orderMemoCNY)-1], "CNY", false},
		{"trailing_json", orderMemoCNY + "{}", "CNY", false},
	}
	for _, key := range []string{"code", "name", "prefix", "suffix", "decimal_places"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(orderMemoCNY), &fields); err != nil {
			t.Fatal(err)
		}
		fields[key] = json.RawMessage("null")
		nullRaw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		delete(fields, key)
		missingRaw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		tests = append(tests,
			struct {
				name, raw, code string
				valid           bool
			}{"null_" + key, string(nullRaw), "CNY", false},
			struct {
				name, raw, code string
				valid           bool
			}{"missing_" + key, string(missingRaw), "CNY", false},
		)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, seeded := range []bool{false, true} {
				var memo currencySnapshotMemo
				if seeded {
					assertOrderMemoDecode(t, &memo, []byte(orderMemoCNY), "CNY")
				}
				for repeat := 0; repeat < 2; repeat++ {
					_, err := assertOrderMemoDecode(t, &memo, []byte(test.raw), test.code)
					if (err == nil) != test.valid {
						t.Fatalf("unexpected oracle validity (seeded=%t, repeat=%d): %v", seeded, repeat, err)
					}
				}
			}
		})
	}
}

func TestOrderSnapshotMemoZeroValueRejectsNilAndEmpty(t *testing.T) {
	for _, raw := range [][]byte{nil, {}, []byte("null")} {
		for _, code := range []string{"", "CNY"} {
			var memo currencySnapshotMemo
			if _, err := assertOrderMemoDecode(t, &memo, raw, code); err == nil {
				t.Fatal("zero-value memo treated absent bytes as a cached success")
			}
		}
	}
}

func TestOrderSnapshotMemoOwnsDriverBufferAndChecksActualCurrency(t *testing.T) {
	var memo currencySnapshotMemo
	raw := []byte(orderMemoCNY)
	want, err := assertOrderMemoDecode(t, &memo, raw, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	precision := bytes.Index(raw, []byte(`"decimal_places":2`)) + len(`"decimal_places":`)
	if precision < len(`"decimal_places":`) || raw[precision] != '2' {
		t.Fatal("test precision location")
	}
	// Drivers can reuse the same input backing array for the following row.
	// A borrowed key would now compare equal and incorrectly return precision 2.
	raw[precision] = '9'
	if _, err := assertOrderMemoDecode(t, &memo, raw, "CNY"); err == nil {
		t.Fatal("mutated driver buffer bypassed strict precision validation")
	}
	raw[precision] = '2'
	if got, err := assertOrderMemoDecode(t, &memo, raw, "CNY"); err != nil || got != want {
		t.Fatal("invalid following row damaged last valid snapshot", got, err)
	}
	if _, err := assertOrderMemoDecode(t, &memo, raw, "USD"); err == nil {
		t.Fatal("identical raw snapshot hid actual row currency mismatch")
	}
	// Replacing the one successful entry may reuse its owned storage, but must
	// retain strict decoding when the old snapshot appears again.
	usd := bytes.ReplaceAll(raw, []byte("CNY"), []byte("USD"))
	assertOrderMemoDecode(t, &memo, usd, "USD")
	assertOrderMemoDecode(t, &memo, raw, "CNY")
}

func TestOrderSnapshotMemoValuesAndInstancesAreIndependent(t *testing.T) {
	var first, second currencySnapshotMemo
	raw := []byte(orderMemoCNY)
	original, err := assertOrderMemoDecode(t, &first, raw, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.Code, changed.Name, changed.Prefix, changed.Suffix, changed.DecimalPlaces = "USD", "caller mutation", "$", " changed", 6
	if got, err := assertOrderMemoDecode(t, &first, raw, "CNY"); err != nil || got != original || got == changed {
		t.Fatal("returned currency value mutated cached state", got, err)
	}
	assertOrderMemoDecode(t, &second, raw, "CNY")
	changedRaw := bytes.Replace(raw, []byte("Archived yuan"), []byte("Updated archive"), 1)
	updated, err := assertOrderMemoDecode(t, &first, changedRaw, "CNY")
	if err != nil || updated.Name != "Updated archive" {
		t.Fatal("independent call ignored different stored bytes", updated, err)
	}
	if got, err := assertOrderMemoDecode(t, &second, raw, "CNY"); err != nil || got != original {
		t.Fatal("another memo instance modified existing result", got, err)
	}
}

type orderSnapshotMemoRow struct {
	order                           Order
	issuerRaw, currencyRaw, planRaw []byte
	failure                         error
}

func (r orderSnapshotMemoRow) Scan(dest ...any) error {
	if r.failure != nil {
		return r.failure
	}
	if len(dest) != 12 {
		return errors.New("unexpected test scan arity")
	}
	values := []any{r.order.ID, r.order.Number, r.order.UserID, r.order.PlanName, r.order.Status,
		r.order.TotalMinor, r.order.Currency, r.order.CreatedAt, r.order.Hostname,
		r.issuerRaw, r.currencyRaw, r.planRaw}
	for n, value := range values {
		reflect.ValueOf(dest[n]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func orderSnapshotMemoTestRow(t *testing.T, number int) orderSnapshotMemoRow {
	t.Helper()
	r := orderSnapshotMemoRow{order: recentOrderTestRecord(number, "recorded-owner", time.Unix(1700000000+int64(number), 0).UTC(), "CNY")}
	var err error
	for _, pair := range []struct {
		value any
		raw   *[]byte
	}{{r.order.Issuer, &r.issuerRaw}, {r.order.CurrencyFormat, &r.currencyRaw}, {r.order.PlanSnapshot, &r.planRaw}} {
		*pair.raw, err = json.Marshal(pair.value)
		if err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestOrderSnapshotMemoScanPreservesEveryRowsStrictValidation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*orderSnapshotMemoRow)
		valid  bool
	}{
		{"same_currency_new_order", func(r *orderSnapshotMemoRow) {}, true},
		{"legacy_absent_optional_snapshots", func(r *orderSnapshotMemoRow) { r.issuerRaw, r.planRaw = nil, nil }, true},
		{"legacy_null_optional_snapshots", func(r *orderSnapshotMemoRow) { r.issuerRaw, r.planRaw = []byte("null"), []byte("null") }, true},
		{"new_issuer", func(r *orderSnapshotMemoRow) { r.issuerRaw = []byte(`{"name":"Changed issuer"}`) }, true},
		{"new_plan", func(r *orderSnapshotMemoRow) { r.planRaw = []byte(`{"name":"Changed plan","cpu":8}`) }, true},
		{"invalid_issuer", func(r *orderSnapshotMemoRow) { r.issuerRaw = []byte("[]") }, false},
		{"invalid_plan", func(r *orderSnapshotMemoRow) { r.planRaw = []byte("[]") }, false},
		{"missing_currency", func(r *orderSnapshotMemoRow) { r.currencyRaw = nil }, false},
		{"actual_currency_mismatch", func(r *orderSnapshotMemoRow) { r.order.Currency = "USD" }, false},
		{"invalid_currency_precision", func(r *orderSnapshotMemoRow) {
			r.currencyRaw = bytes.Replace(r.currencyRaw, []byte(`"decimal_places":2`), []byte(`"decimal_places":9`), 1)
		}, false},
		{"driver_error", func(r *orderSnapshotMemoRow) { r.failure = errors.New("synthetic scan failure") }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var memo currencySnapshotMemo
			if _, err := scanOrderWithCurrencyMemo(orderSnapshotMemoTestRow(t, 1), &memo); err != nil {
				t.Fatal(err)
			}
			row := orderSnapshotMemoTestRow(t, 2)
			test.mutate(&row)
			want, wantErr := scanOrder(row)
			got, err := scanOrderWithCurrencyMemo(row, &memo)
			if !reflect.DeepEqual(got, want) || (err == nil) != (wantErr == nil) || (err != nil && err.Error() != wantErr.Error()) {
				t.Fatalf("memoized row differs from strict scan: got %#v, %v; want %#v, %v", got, err, want, wantErr)
			}
			if (err == nil) != test.valid {
				t.Fatal("unexpected strict row validity", err)
			}
		})
	}
}

func TestOrderSnapshotMemoScanKeepsRowsAndCallsIndependent(t *testing.T) {
	var memo currencySnapshotMemo
	first, err := scanOrderWithCurrencyMemo(orderSnapshotMemoTestRow(t, 1), &memo)
	if err != nil {
		t.Fatal(err)
	}
	row := orderSnapshotMemoTestRow(t, 2)
	second, err := scanOrderWithCurrencyMemo(row, &memo)
	if err != nil || first.Issuer == second.Issuer || first.PlanSnapshot == second.PlanSnapshot {
		t.Fatal("adjacent records share mutable snapshot pointers", err)
	}
	want := cloneOrder(second)
	first.Issuer.Name, first.PlanSnapshot.Name, first.CurrencyFormat.Name = "mutated issuer", "mutated plan", "mutated currency"
	if !reflect.DeepEqual(second, want) {
		t.Fatal("mutating one row changed another row's historical snapshots")
	}
	second.Issuer.Name, second.PlanSnapshot.Name, second.CurrencyFormat.Name = "another issuer", "another plan", "another currency"
	var nextCall currencySnapshotMemo
	again, err := scanOrderWithCurrencyMemo(row, &nextCall)
	if err != nil || !reflect.DeepEqual(again, want) || again.Issuer == second.Issuer || again.PlanSnapshot == second.PlanSnapshot {
		t.Fatal("previous call's returned mutation reached a later decode", err)
	}
}
