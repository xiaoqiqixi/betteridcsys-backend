CREATE TABLE IF NOT EXISTS affiliate_commissions (
 id CHAR(32) PRIMARY KEY,
 affiliate_id CHAR(32) NOT NULL,
 client_id CHAR(32) NOT NULL,
 invoice_id CHAR(32) NOT NULL UNIQUE,
 currency CHAR(3) NOT NULL,
 decimal_places INT NOT NULL,
 basis_points INT NOT NULL,
 amount_minor BIGINT NOT NULL,
 withdrawn_minor BIGINT NOT NULL,
 status ENUM('pending','available','withdrawn','cancelled') NOT NULL,
 created_at DATETIME(6) NOT NULL,
 released_at DATETIME(6) NULL,
 INDEX affiliate_commission_balance(affiliate_id,status,created_at,id),
 INDEX affiliate_commission_release(status,created_at),
 FOREIGN KEY(affiliate_id) REFERENCES affiliates(user_id),
 FOREIGN KEY(client_id) REFERENCES users(id),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 CONSTRAINT affiliate_commission_money CHECK(amount_minor >= 0 AND amount_minor <= 9007199254740991 AND withdrawn_minor >= 0 AND withdrawn_minor <= amount_minor),
 CONSTRAINT affiliate_commission_rate CHECK(basis_points >= 0 AND basis_points <= 10000),
 CONSTRAINT affiliate_commission_precision CHECK(decimal_places >= 0 AND decimal_places <= 6)
) ENGINE=InnoDB;
INSERT IGNORE INTO account_settings(name,document) VALUES('affiliate_policy',JSON_OBJECT('commission_basis_points',0,'recurring',JSON_EXTRACT('false','$')));
