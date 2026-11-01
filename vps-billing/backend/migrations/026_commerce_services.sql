ALTER TABLE services MODIFY COLUMN status ENUM('pending','active','suspended','terminated','provisioning','cancelled','failed') NOT NULL;
ALTER TABLE services MODIFY COLUMN plan_name VARCHAR(4000) NOT NULL;
ALTER TABLE services ADD COLUMN product_id VARCHAR(64) NULL;
ALTER TABLE services ADD COLUMN cycle VARCHAR(16) NOT NULL DEFAULT 'monthly';
ALTER TABLE services ADD COLUMN recurring_minor BIGINT NOT NULL DEFAULT 0;
ALTER TABLE services ADD COLUMN price_override_minor BIGINT NULL;
ALTER TABLE services ADD COLUMN next_invoice_at DATETIME(6) NULL;
ALTER TABLE services ADD COLUMN auto_renew BOOLEAN NOT NULL DEFAULT TRUE;
ALTER TABLE services ADD COLUMN label VARCHAR(80) NOT NULL DEFAULT '';
ALTER TABLE services ADD COLUMN controller_id VARCHAR(64) NULL;
ALTER TABLE services ADD COLUMN resource_id VARCHAR(128) NULL;
ALTER TABLE services ADD COLUMN suspension_reason VARCHAR(500) NULL;
ALTER TABLE services ADD COLUMN cancellation_type VARCHAR(16) NULL;
ALTER TABLE services ADD COLUMN cancellation_reason VARCHAR(500) NULL;
ALTER TABLE services ADD COLUMN cancellation_requested_at DATETIME(6) NULL;
ALTER TABLE services ADD COLUMN terminates_at DATETIME(6) NULL;
ALTER TABLE services ADD COLUMN specs_snapshot JSON NULL;
ALTER TABLE services ADD COLUMN os_snapshot JSON NULL;
ALTER TABLE services ADD COLUMN configuration_snapshot JSON NULL;
ALTER TABLE services ADD COLUMN created_at DATETIME(6) NULL;
ALTER TABLE services ADD COLUMN revision BIGINT NOT NULL DEFAULT 1;
ALTER TABLE services ADD COLUMN anchor_day INT NOT NULL DEFAULT 1;
ALTER TABLE services ADD COLUMN last_change_at DATETIME(6) NULL;
ALTER TABLE services ADD CONSTRAINT service_recurring_money CHECK(recurring_minor >= 0 AND recurring_minor <= 9007199254740991);
ALTER TABLE services ADD CONSTRAINT service_override_money CHECK(price_override_minor >= 0 AND price_override_minor <= 9007199254740991);
ALTER TABLE services ADD CONSTRAINT service_revision CHECK(revision >= 1 AND revision <= 9007199254740991);
ALTER TABLE services ADD CONSTRAINT service_anchor_day CHECK(anchor_day >= 1 AND anchor_day <= 31);
ALTER TABLE services ADD CONSTRAINT service_auto_renew CHECK(auto_renew IN (0,1));
CREATE TABLE IF NOT EXISTS service_units (
 order_item_id CHAR(32) NOT NULL,
 unit_index INT NOT NULL,
 service_id CHAR(32) NOT NULL UNIQUE,
 PRIMARY KEY(order_item_id,unit_index),
 FOREIGN KEY(order_item_id) REFERENCES order_items(id),
 FOREIGN KEY(service_id) REFERENCES services(id),
 CONSTRAINT service_unit_index CHECK(unit_index >= 0 AND unit_index < 20)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS service_tasks (
 id CHAR(32) PRIMARY KEY,
 service_id CHAR(32) NOT NULL,
 action VARCHAR(32) NOT NULL,
 status ENUM('queued','running','succeeded','failed','uncertain') NOT NULL,
 operation_id VARCHAR(128) NULL UNIQUE,
 message VARCHAR(500) NULL,
 payload JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NULL,
 INDEX service_task_status(status,created_at,id),
 INDEX service_task_history(service_id,created_at,id),
 FOREIGN KEY(service_id) REFERENCES services(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoice_service_changes (
 invoice_id CHAR(32) PRIMARY KEY,
 service_id CHAR(32) NOT NULL,
 kind ENUM('renewal','upgrade') NOT NULL,
 period_start DATETIME(6) NULL,
 period_end DATETIME(6) NULL,
 payload JSON NOT NULL,
 UNIQUE KEY service_billing_period(service_id,kind,period_start),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id),
 FOREIGN KEY(service_id) REFERENCES services(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoice_notifications (
 invoice_id CHAR(32) NOT NULL,
 stage VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(invoice_id,stage),
 FOREIGN KEY(invoice_id) REFERENCES invoices(id)
) ENGINE=InnoDB;
UPDATE services SET recurring_minor=price_minor,anchor_day=DAY(next_due_at),created_at=UTC_TIMESTAMP(6),label=LEFT(name,80),next_invoice_at=next_due_at WHERE created_at IS NULL;
