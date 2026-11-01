CREATE TABLE IF NOT EXISTS automation_settings (
 id INT PRIMARY KEY,
 document JSON NOT NULL,
 CONSTRAINT automation_singleton CHECK(id = 1)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS automation_runs (
 id CHAR(32) PRIMARY KEY,
 job VARCHAR(32) NOT NULL,
 scheduled_at DATETIME(6) NOT NULL,
 status VARCHAR(16) NOT NULL,
 started_at DATETIME(6) NOT NULL,
 finished_at DATETIME(6) NULL,
 processed BIGINT NOT NULL,
 failed BIGINT NOT NULL,
 message VARCHAR(500) NULL,
 UNIQUE KEY automation_tick(job,scheduled_at),
 KEY automation_history(started_at,id),
 CONSTRAINT automation_counts CHECK(processed >= 0 AND failed >= 0),
 CONSTRAINT automation_status CHECK(status IN ('running','succeeded','failed','uncertain'))
) ENGINE=InnoDB;
INSERT IGNORE INTO automation_settings(id,document) VALUES(1,JSON_OBJECT('invoice_days_before_due',7,'reminder_days_before_due',JSON_ARRAY(1,3,7),'overdue_reminder_days',JSON_ARRAY(1,3,7),'suspend_after_days',3,'terminate_after_days',30,'cancel_unpaid_orders_after_days',3,'auto_provision',JSON_EXTRACT('false','$'),'auto_apply_credit',JSON_EXTRACT('false','$'),'affiliate_hold_days',30,'ticket_autoclose_hours',72,'revision',1));
