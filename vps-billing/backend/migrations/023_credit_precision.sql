ALTER TABLE credit_ledger DROP CHECK credit_precision, ADD CONSTRAINT credit_precision CHECK(decimal_places >= 0 AND decimal_places <= 6);
