package migrations

import _ "embed"

//go:embed 001_initial.sql
var Initial string

//go:embed 002_site_settings.sql
var SiteSettings string

//go:embed 003_currencies.sql
var Currencies string

//go:embed 004_billing_snapshots.sql
var BillingSnapshots string

//go:embed 005_exact_currency_codes.sql
var ExactCurrencyCodes string

//go:embed 006_email_settings.sql
var EmailSettings string

//go:embed 007_currency_registry.sql
var CurrencyRegistry string

//go:embed 008_payment_runtime.sql
var PaymentRuntime string

//go:embed 009_audit_query_indexes.sql
var AuditQueryIndexes string

//go:embed 010_customer_accounts.sql
var CustomerAccounts string

//go:embed 011_catalog_taxonomy.sql
var CatalogTaxonomy string

//go:embed 012_catalog_nodes.sql
var CatalogNodes string

//go:embed 013_product_configurations.sql
var ProductConfigurations string

//go:embed 014_payment_resolution.sql
var PaymentResolution string

//go:embed 015_staff_roles.sql
var StaffRoles string

//go:embed 016_commerce_catalog.sql
var CommerceCatalog string

//go:embed 017_account_security.sql
var AccountSecurity string

//go:embed 018_commerce_coupons.sql
var CommerceCoupons string

//go:embed 019_payment_gateways.sql
var PaymentGateways string

//go:embed 020_credit_ledger.sql
var CommerceLedger string

//go:embed 021_automation.sql
var Automation string

//go:embed 022_account_features.sql
var AccountFeatures string

//go:embed 023_credit_precision.sql
var CreditPrecision string
