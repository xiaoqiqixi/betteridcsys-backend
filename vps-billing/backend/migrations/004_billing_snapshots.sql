ALTER TABLE orders ADD COLUMN issuer_snapshot JSON NULL;
ALTER TABLE services ADD CONSTRAINT services_currency CHECK(currency IN ('CNY','USD'));
ALTER TABLE plan_prices ADD CONSTRAINT plan_prices_exact_minor CHECK(amount_minor <= 9007199254740991);
ALTER TABLE orders ADD CONSTRAINT orders_exact_minor CHECK(total_minor <= 9007199254740991);
ALTER TABLE invoices ADD CONSTRAINT invoices_exact_minor CHECK(amount_minor <= 9007199254740991);
ALTER TABLE services ADD CONSTRAINT services_exact_minor CHECK(price_minor <= 9007199254740991);
