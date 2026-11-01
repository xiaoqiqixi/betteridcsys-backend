ALTER TABLE payment_attempts MODIFY COLUMN state ENUM('pending','checkout','uncertain','settled','expired','voided') NOT NULL, ADD KEY payment_attempts_invoice_history(invoice_id), DROP INDEX payment_attempts_invoice;
ALTER TABLE payment_settlements ADD COLUMN disposition ENUM('settled','duplicate','cancelled') CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'settled';
ALTER TABLE invoices ADD COLUMN settlement_review BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE invoices ADD CONSTRAINT invoices_settlement_review_boolean CHECK(settlement_review IN (0,1));
