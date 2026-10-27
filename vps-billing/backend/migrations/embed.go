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
