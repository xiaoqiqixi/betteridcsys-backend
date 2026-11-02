package billing

var affiliateSettingsColumns = map[string]string{
	"affiliate_invoice_lines":    "invoice_line_id:char(32)! product_id:varchar(64)! order_item_id:char(32)!",
	"affiliate_settings":         "id:int! revision:bigint! document:json!",
	"affiliate_profiles":         "user_id:char(32)! rate_bp:int? status:varchar(16)! note:text!",
	"affiliate_commission_rules": "commission_id:char(32)! cash_minor:bigint! hold_until:datetime(6)! clawback:varchar(16)! settings_snapshot:json!",
	"affiliate_commission_lines": "id:char(32)! commission_id:char(32)! invoice_line_id:char(32)? product_id:varchar(64)? cash_minor:bigint! rate_bp:int! amount_minor:bigint! scope:varchar(16)! scope_months:int! settings_revision:bigint!",
	"affiliate_adjustments":      "id:char(32)! commission_id:char(32)! refund_id:char(32)! amount_minor:bigint! created_at:datetime(6)!",
	"affiliate_payouts":          "id:char(32)! affiliate_id:char(32)! currency:char(3)! decimal_places:int! amount_minor:bigint! reference:varchar(120)! destination:varchar(16)! created_at:datetime(6)!",
	"affiliate_registration_ips": "user_id:char(32)! ip_prefix:varchar(64)!",
}
var affiliateSettingsChecks = map[string]map[string]string{
	"affiliate_settings":         {"affiliate_settings_singleton": "id = 1", "affiliate_settings_revision": "revision >= 0 AND revision <= 9007199254740991"},
	"affiliate_profiles":         {"affiliate_profile_rate": "rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)", "affiliate_profile_status": "status IN ('active','suspended')"},
	"affiliate_commission_rules": {"affiliate_rule_cash": "cash_minor >= 0 AND cash_minor <= 9007199254740991", "affiliate_rule_clawback": "clawback IN ('reverse_pending','reverse_all')"},
	"affiliate_commission_lines": {"affiliate_line_money": "cash_minor >= 0 AND cash_minor <= 9007199254740991 AND amount_minor >= 0 AND amount_minor <= 9007199254740991", "affiliate_line_rate": "rate_bp >= 0 AND rate_bp <= 10000", "affiliate_line_scope": "scope IN ('first_payment','all_payments','first_months') AND scope_months >= 1 AND scope_months <= 120"},
	"affiliate_adjustments":      {"affiliate_adjustment_amount": "amount_minor <= 0 AND amount_minor >= -9007199254740991"},
	"affiliate_payouts":          {"affiliate_payout_amount": "amount_minor >= 1 AND amount_minor <= 9007199254740991", "affiliate_payout_precision": "decimal_places >= 0 AND decimal_places <= 6", "affiliate_payout_destination": "destination IN ('credit','manual')"},
	"affiliate_registration_ips": {},
}
var affiliateSettingsKeys = []string{"affiliate_invoice_lines:invoice_line_id", "affiliate_adjustments:refund_id", "affiliate_settings:id", "affiliate_profiles:user_id", "affiliate_commission_rules:commission_id", "affiliate_commission_lines:id", "affiliate_commission_lines:commission_id,invoice_line_id", "affiliate_adjustments:id", "affiliate_payouts:id", "affiliate_payouts:affiliate_id,currency,destination,reference", "affiliate_registration_ips:user_id"}
var affiliateSettingsFKs = []string{"affiliate_invoice_lines:invoice_line_id:invoice_lines:id:RESTRICT", "affiliate_invoice_lines:order_item_id:order_items:id:RESTRICT", "affiliate_profiles:user_id:affiliates:user_id:RESTRICT", "affiliate_commission_rules:commission_id:affiliate_commissions:id:RESTRICT", "affiliate_commission_lines:commission_id:affiliate_commissions:id:RESTRICT", "affiliate_commission_lines:invoice_line_id:invoice_lines:id:RESTRICT", "affiliate_adjustments:commission_id:affiliate_commissions:id:RESTRICT", "affiliate_adjustments:refund_id:invoice_refunds:id:RESTRICT", "affiliate_payouts:affiliate_id:affiliates:user_id:RESTRICT", "affiliate_registration_ips:user_id:users:id:CASCADE"}
