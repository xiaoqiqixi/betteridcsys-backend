CREATE TABLE IF NOT EXISTS affiliate_settings (
 id INT PRIMARY KEY,
 revision BIGINT NOT NULL,
 document JSON NOT NULL,
 CONSTRAINT affiliate_settings_singleton CHECK(id = 1),
 CONSTRAINT affiliate_settings_revision CHECK(revision >= 0 AND revision <= 9007199254740991)
) ENGINE=InnoDB;
INSERT IGNORE INTO affiliate_settings(id,revision,document) VALUES(1,0,JSON_OBJECT('enabled',JSON_EXTRACT('false','$'),'default_rate_bp',0,'scope','first_payment','scope_months',12,'hold_days',30,'attribution_days',30,'min_withdraw',JSON_ARRAY(),'max_per_referral',JSON_ARRAY(),'allow_self_referral',JSON_EXTRACT('false','$'),'clawback','reverse_all','product_overrides',JSON_ARRAY(),'revision',0));
CREATE TABLE IF NOT EXISTS affiliate_profiles (
 user_id CHAR(32) PRIMARY KEY,
 rate_bp INT NULL,
 status VARCHAR(16) NOT NULL,
 note TEXT NOT NULL,
 FOREIGN KEY(user_id) REFERENCES affiliates(user_id),
 CONSTRAINT affiliate_profile_rate CHECK(rate_bp IS NULL OR (rate_bp >= 0 AND rate_bp <= 10000)),
 CONSTRAINT affiliate_profile_status CHECK(status IN ('active','suspended'))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_commission_rules (
 commission_id CHAR(32) PRIMARY KEY,
 cash_minor BIGINT NOT NULL,
 hold_until DATETIME(6) NOT NULL,
 clawback VARCHAR(16) NOT NULL,
 settings_snapshot JSON NOT NULL,
 FOREIGN KEY(commission_id) REFERENCES affiliate_commissions(id),
 INDEX affiliate_hold_until(hold_until,commission_id),
 CONSTRAINT affiliate_rule_cash CHECK(cash_minor >= 0 AND cash_minor <= 9007199254740991),
 CONSTRAINT affiliate_rule_clawback CHECK(clawback IN ('reverse_pending','reverse_all'))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_commission_lines (
 id CHAR(32) PRIMARY KEY,
 commission_id CHAR(32) NOT NULL,
 invoice_line_id CHAR(32) NULL,
 product_id VARCHAR(64) NULL,
 cash_minor BIGINT NOT NULL,
 rate_bp INT NOT NULL,
 amount_minor BIGINT NOT NULL,
 scope VARCHAR(16) NOT NULL,
 scope_months INT NOT NULL,
 settings_revision BIGINT NOT NULL,
 UNIQUE KEY affiliate_source_line(commission_id,invoice_line_id),
 FOREIGN KEY(commission_id) REFERENCES affiliate_commissions(id),
 FOREIGN KEY(invoice_line_id) REFERENCES invoice_lines(id),
 CONSTRAINT affiliate_line_money CHECK(cash_minor >= 0 AND cash_minor <= 9007199254740991 AND amount_minor >= 0 AND amount_minor <= 9007199254740991),
 CONSTRAINT affiliate_line_rate CHECK(rate_bp >= 0 AND rate_bp <= 10000),
 CONSTRAINT affiliate_line_scope CHECK(scope IN ('first_payment','all_payments','first_months') AND scope_months >= 1 AND scope_months <= 120)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_adjustments (
 id CHAR(32) PRIMARY KEY,
 commission_id CHAR(32) NOT NULL,
 refund_id CHAR(32) NOT NULL UNIQUE,
 amount_minor BIGINT NOT NULL,
 created_at DATETIME(6) NOT NULL,
 FOREIGN KEY(commission_id) REFERENCES affiliate_commissions(id),
 FOREIGN KEY(refund_id) REFERENCES invoice_refunds(id),
 CONSTRAINT affiliate_adjustment_amount CHECK(amount_minor <= 0 AND amount_minor >= -9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_payouts (
 id CHAR(32) PRIMARY KEY,
 affiliate_id CHAR(32) NOT NULL,
 currency CHAR(3) NOT NULL,
 decimal_places INT NOT NULL,
 amount_minor BIGINT NOT NULL,
 reference VARCHAR(120) NOT NULL,
 destination VARCHAR(16) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 FOREIGN KEY(affiliate_id) REFERENCES affiliates(user_id),
 UNIQUE KEY affiliate_payout_reference(affiliate_id,currency,destination,reference),
 INDEX affiliate_payout_balance(affiliate_id,currency),
 CONSTRAINT affiliate_payout_amount CHECK(amount_minor >= 1 AND amount_minor <= 9007199254740991),
 CONSTRAINT affiliate_payout_precision CHECK(decimal_places >= 0 AND decimal_places <= 6),
 CONSTRAINT affiliate_payout_destination CHECK(destination IN ('credit','manual'))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_registration_ips (
 user_id CHAR(32) PRIMARY KEY,
 ip_prefix VARCHAR(64) NOT NULL,
 FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
INSERT IGNORE INTO affiliate_commission_rules(commission_id,cash_minor,hold_until,clawback,settings_snapshot) SELECT c.id,i.amount_minor,TIMESTAMPADD(DAY,COALESCE((SELECT JSON_EXTRACT(document,'$.affiliate_hold_days') FROM automation_settings WHERE id=1),30),c.created_at),'reverse_pending',JSON_OBJECT('legacy',JSON_EXTRACT('true','$')) FROM affiliate_commissions c JOIN invoices i ON i.id=c.invoice_id;
INSERT IGNORE INTO affiliate_commission_lines(id,commission_id,invoice_line_id,product_id,cash_minor,rate_bp,amount_minor,scope,scope_months,settings_revision) SELECT c.id,c.id,NULL,NULL,i.amount_minor,c.basis_points,c.amount_minor,'all_payments',12,0 FROM affiliate_commissions c JOIN invoices i ON i.id=c.invoice_id;
CREATE TABLE IF NOT EXISTS affiliate_invoice_lines (
 invoice_line_id CHAR(32) PRIMARY KEY,
 product_id VARCHAR(64) NOT NULL,
 order_item_id CHAR(32) NOT NULL,
 FOREIGN KEY(invoice_line_id) REFERENCES invoice_lines(id),
 FOREIGN KEY(order_item_id) REFERENCES order_items(id)
) ENGINE=InnoDB;
