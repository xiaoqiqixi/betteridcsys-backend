package integrations

// SettlementExpectation must come from persisted core-owned invoice/order and
// merchant configuration, never from callback-supplied claims. Passing validation
// does NOT establish deduplication, record payment or authorize provisioning.
type SettlementExpectation struct {
	CurrencyDecimalPlaces                                        int
	ProviderID, MerchantAccountID, AccountID, OrderID, InvoiceID string
	AmountMinor                                                  int64
	Currency                                                     string
}

func ValidateSettlement(v VerifiedPayment, want SettlementExpectation) error {
	if want.ProviderID == "" || want.MerchantAccountID == "" || want.AccountID == "" || want.OrderID == "" || want.InvoiceID == "" || want.AmountMinor <= 0 || want.AmountMinor > maxMinor || !supportedCurrency(want.Currency) || !validCurrencyPrecision(want.CurrencyDecimalPlaces) || v.ProviderID != want.ProviderID || v.MerchantAccountID != want.MerchantAccountID || v.AccountID != want.AccountID || v.OrderID != want.OrderID || v.InvoiceID != want.InvoiceID || v.AmountMinor != want.AmountMinor || v.Currency != want.Currency || v.CurrencyDecimalPlaces != want.CurrencyDecimalPlaces || v.EventID == "" || v.TransactionID == "" || v.Status != "settled" || v.SettledAt.IsZero() {
		return ErrInvalidResult
	}
	return nil
}
