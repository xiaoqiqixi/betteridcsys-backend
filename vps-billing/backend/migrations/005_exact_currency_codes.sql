ALTER TABLE orders ADD CONSTRAINT orders_currency_exact CHECK(CAST(currency AS BINARY) IN ('CNY','USD'));
ALTER TABLE invoices ADD CONSTRAINT invoices_currency_exact CHECK(CAST(currency AS BINARY) IN ('CNY','USD'));
ALTER TABLE services ADD CONSTRAINT services_currency_exact CHECK(CAST(currency AS BINARY) IN ('CNY','USD'));
