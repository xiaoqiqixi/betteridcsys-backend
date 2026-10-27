CREATE TABLE IF NOT EXISTS currency_settings (
 id TINYINT NOT NULL PRIMARY KEY,
 document JSON NOT NULL,
 prices_revision BIGINT NOT NULL DEFAULT 1,
 CONSTRAINT currency_settings_singleton CHECK(id = 1),
 CONSTRAINT currency_settings_prices_revision CHECK(prices_revision >= 1 AND prices_revision <= 9007199254740991)
) ENGINE=InnoDB;
ALTER TABLE orders ADD COLUMN currency_snapshot JSON NULL;
ALTER TABLE orders ADD COLUMN plan_snapshot JSON NULL;
ALTER TABLE invoices ADD COLUMN currency_snapshot JSON NULL;
ALTER TABLE services ADD COLUMN currency_snapshot JSON NULL;
ALTER TABLE plans ADD CONSTRAINT plans_currency_code CHECK(OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90);
ALTER TABLE plan_prices ADD CONSTRAINT plan_prices_currency_code CHECK(OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90);
ALTER TABLE orders ADD CONSTRAINT orders_currency_code CHECK(OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90);
ALTER TABLE invoices ADD CONSTRAINT invoices_currency_code CHECK(OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90);
ALTER TABLE services ADD CONSTRAINT services_currency_code CHECK(OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90);
ALTER TABLE plans DROP CHECK plans_chk_2;
ALTER TABLE plan_prices DROP CHECK plan_prices_chk_2;
ALTER TABLE orders DROP CHECK orders_currency;
ALTER TABLE orders DROP CHECK orders_currency_exact;
ALTER TABLE invoices DROP CHECK invoices_currency;
ALTER TABLE invoices DROP CHECK invoices_currency_exact;
ALTER TABLE services DROP CHECK services_currency;
ALTER TABLE services DROP CHECK services_currency_exact;
