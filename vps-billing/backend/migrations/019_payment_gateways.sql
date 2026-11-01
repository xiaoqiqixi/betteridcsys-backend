CREATE TABLE IF NOT EXISTS payment_gateway_settings (
 id VARCHAR(64) PRIMARY KEY,
 document JSON NOT NULL
) ENGINE=InnoDB;
