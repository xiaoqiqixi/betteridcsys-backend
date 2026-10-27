CREATE TABLE IF NOT EXISTS plan_prices (
 plan_id VARCHAR(64) NOT NULL, currency CHAR(3) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 amount_minor BIGINT NOT NULL, available BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(plan_id,currency), FOREIGN KEY(plan_id) REFERENCES plans(id),
 CHECK(amount_minor > 0), CHECK(currency IN ('CNY','USD'))
) ENGINE=InnoDB;
INSERT IGNORE INTO plan_prices(plan_id,currency,amount_minor,available)
 SELECT id,'USD',price_minor,available FROM plans WHERE price_minor>0;
INSERT IGNORE INTO plan_prices(plan_id,currency,amount_minor,available) VALUES
 ('starter','CNY',3900,FALSE),('standard','CNY',7900,FALSE),('performance','CNY',15900,FALSE);
ALTER TABLE orders ADD CONSTRAINT orders_currency CHECK(currency IN ('CNY','USD'));
ALTER TABLE invoices ADD CONSTRAINT invoices_currency CHECK(currency IN ('CNY','USD'));
