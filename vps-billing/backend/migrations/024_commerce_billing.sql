ALTER TABLE orders MODIFY COLUMN plan_id VARCHAR(64) NULL;
ALTER TABLE orders MODIFY COLUMN status ENUM('pending_payment','paid','cancelled','active','fraud') NOT NULL;
ALTER TABLE orders ADD COLUMN coupon_code VARCHAR(64) NULL;
ALTER TABLE orders ADD COLUMN paid_at DATETIME(6) NULL;
ALTER TABLE orders ADD COLUMN cancelled_at DATETIME(6) NULL;
ALTER TABLE invoices MODIFY COLUMN order_id CHAR(32) NULL;
ALTER TABLE invoices MODIFY COLUMN status ENUM('unpaid','paid','void','cancelled','refunded','partially_refunded') NOT NULL;
ALTER TABLE invoices ADD COLUMN kind ENUM('order','renewal','upgrade','top_up','manual') NOT NULL DEFAULT 'order';
ALTER TABLE invoices ADD COLUMN commerce_version INT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN subtotal_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN credit_applied_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN amount_paid_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN refunded_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE invoices ADD COLUMN billed_to_snapshot JSON NULL;
ALTER TABLE invoices ADD CONSTRAINT invoice_commerce_money CHECK(subtotal_minor >= 0 AND subtotal_minor <= 9007199254740991 AND credit_applied_minor >= 0 AND amount_paid_minor >= 0 AND refunded_minor >= 0 AND refunded_minor <= credit_applied_minor + amount_paid_minor AND credit_applied_minor + amount_paid_minor <= amount_minor);
ALTER TABLE payment_attempts MODIFY COLUMN order_id CHAR(32) NULL;
CREATE TABLE IF NOT EXISTS order_items (
 id CHAR(32) PRIMARY KEY,
 order_id CHAR(32) NOT NULL,
 position INT NOT NULL,
 product_id VARCHAR(64) NOT NULL,
 product_name VARCHAR(4000) NOT NULL,
 cycle VARCHAR(16) NOT NULL,
 quantity INT NOT NULL,
 hostname VARCHAR(253) NULL,
 options_summary TEXT NOT NULL,
 recurring_minor BIGINT NOT NULL,
 total_minor BIGINT NOT NULL,
 stock_reserved INT NOT NULL,
 snapshot JSON NOT NULL,
 UNIQUE KEY order_item_position(order_id,position),
 FOREIGN KEY(order_id) REFERENCES orders(id),
 FOREIGN KEY(product_id) REFERENCES products(id),
 CONSTRAINT order_item_quantity CHECK(quantity >= 1 AND quantity <= 20 AND stock_reserved >= 0 AND stock_reserved <= quantity),
 CONSTRAINT order_item_money CHECK(recurring_minor >= 0 AND recurring_minor <= 9007199254740991 AND total_minor >= 0 AND total_minor <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoice_lines (
 id CHAR(32) PRIMARY KEY,
 invoice_id CHAR(32) NOT NULL,
 position INT NOT NULL,
 service_id CHAR(32) NULL,
 description TEXT NOT NULL,
 amount_minor BIGINT NOT NULL,
 period_text VARCHAR(120) NULL,
 period_start DATETIME(6) NULL,
 UNIQUE KEY invoice_line_position(invoice_id,position),
 UNIQUE KEY service_invoice_period(service_id,period_start),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 FOREIGN KEY(service_id) REFERENCES services(id),
 CONSTRAINT invoice_line_money CHECK(amount_minor >= -9007199254740991 AND amount_minor <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoice_payments (
 id CHAR(32) PRIMARY KEY,
 invoice_id CHAR(32) NOT NULL,
 attempt_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL UNIQUE,
 method VARCHAR(24) NOT NULL,
 gateway VARCHAR(64) NULL,
 reference VARCHAR(128) NULL,
 amount_minor BIGINT NOT NULL,
 refunded_minor BIGINT NOT NULL,
 status VARCHAR(24) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY invoice_payment_reference(invoice_id,method,reference),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 FOREIGN KEY(attempt_id) REFERENCES payment_attempts(id),
 CONSTRAINT invoice_payment_money CHECK(amount_minor >= 1 AND amount_minor <= 9007199254740991 AND refunded_minor >= 0 AND refunded_minor <= amount_minor)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoice_refunds (
 id CHAR(32) PRIMARY KEY,
 invoice_id CHAR(32) NOT NULL,
 payment_id CHAR(32) NOT NULL,
 amount_minor BIGINT NOT NULL,
 destination VARCHAR(16) NOT NULL,
 status VARCHAR(16) NOT NULL,
 reason VARCHAR(500) NOT NULL,
 provider_reference VARCHAR(128) NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 FOREIGN KEY(payment_id) REFERENCES invoice_payments(id),
 CONSTRAINT invoice_refund_money CHECK(amount_minor >= 1 AND amount_minor <= 9007199254740991)
) ENGINE=InnoDB;
UPDATE invoices SET subtotal_minor=amount_minor,amount_paid_minor=IF(status='paid',amount_minor,0) WHERE commerce_version=0;
UPDATE orders o JOIN invoices i ON i.order_id=o.id SET o.paid_at=i.paid_at WHERE o.status='paid';
INSERT IGNORE INTO order_items(id,order_id,position,product_id,product_name,cycle,quantity,hostname,options_summary,recurring_minor,total_minor,stock_reserved,snapshot) SELECT MD5(CONCAT('legacy-item:',o.id)),o.id,0,o.plan_id,o.plan_name,'monthly',1,o.hostname,'',o.total_minor,o.total_minor,0,JSON_OBJECT('legacy',JSON_EXTRACT('true','$')) FROM orders o JOIN products p ON p.id=o.plan_id;
INSERT IGNORE INTO invoice_lines(id,invoice_id,position,description,amount_minor) SELECT MD5(CONCAT('legacy-line:',i.id)),i.id,0,o.plan_name,i.amount_minor FROM invoices i JOIN orders o ON o.id=i.order_id;
