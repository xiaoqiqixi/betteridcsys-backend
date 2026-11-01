CREATE TABLE IF NOT EXISTS credit_ledger (
 user_id CHAR(32) NOT NULL,
 sequence BIGINT NOT NULL,
 id CHAR(32) NOT NULL UNIQUE,
 kind VARCHAR(24) NOT NULL,
 amount_minor BIGINT NOT NULL,
 balance_after_minor BIGINT NOT NULL,
 currency CHAR(3) NOT NULL,
 decimal_places INT NOT NULL,
 invoice_id CHAR(32) NULL,
 description VARCHAR(500) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(user_id,sequence),
 FOREIGN KEY(user_id) REFERENCES users(id),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 CONSTRAINT credit_sequence CHECK(sequence >= 1 AND sequence <= 9007199254740991),
 CONSTRAINT credit_amount CHECK(amount_minor >= -9007199254740991 AND amount_minor <= 9007199254740991),
 CONSTRAINT credit_balance CHECK(balance_after_minor >= 0 AND balance_after_minor <= 9007199254740991),
 CONSTRAINT credit_precision CHECK(decimal_places >= 0 AND decimal_places <= 3),
 CONSTRAINT credit_kind CHECK(kind IN ('top_up','invoice_payment','refund','adjustment','affiliate_payout','overpayment'))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS credit_limits (
 currency CHAR(3) PRIMARY KEY,
 min_minor BIGINT NOT NULL,
 max_minor BIGINT NOT NULL,
 presets JSON NOT NULL,
 CONSTRAINT credit_limits_amount CHECK(min_minor >= 1 AND max_minor >= min_minor AND max_minor <= 9007199254740991)
) ENGINE=InnoDB;
