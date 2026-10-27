CREATE TABLE IF NOT EXISTS users (
 id CHAR(32) PRIMARY KEY, email VARCHAR(254) CHARACTER SET ascii COLLATE ascii_general_ci NOT NULL UNIQUE,
 name VARCHAR(120) NOT NULL, role ENUM('customer','admin') NOT NULL,
 password_hash VARCHAR(100) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS sessions (
 token_hash CHAR(64) PRIMARY KEY, user_id CHAR(32) NOT NULL, expires_at DATETIME(6) NOT NULL,
 KEY ix_session_expiry (expires_at), FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS plans (
 id VARCHAR(64) PRIMARY KEY, name VARCHAR(120) NOT NULL, description VARCHAR(500) NOT NULL,
 cpu INT NOT NULL, memory_gb INT NOT NULL, disk_gb INT NOT NULL, transfer_tb INT NOT NULL,
 region VARCHAR(120) NOT NULL, price_minor BIGINT NOT NULL, currency CHAR(3) NOT NULL,
 available BOOLEAN NOT NULL DEFAULT FALSE, CHECK(price_minor >= 0), CHECK(currency = 'USD')
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS orders (
 id CHAR(32) PRIMARY KEY, number VARCHAR(64) NOT NULL UNIQUE, user_id CHAR(32) NOT NULL,
 plan_id VARCHAR(64) NOT NULL, plan_name VARCHAR(120) NOT NULL, hostname VARCHAR(253) NOT NULL,
 status ENUM('pending_payment','paid','cancelled') NOT NULL,
 total_minor BIGINT NOT NULL, currency CHAR(3) NOT NULL, created_at DATETIME(6) NOT NULL,
 KEY ix_orders_user_date(user_id,created_at), FOREIGN KEY(user_id) REFERENCES users(id),
 FOREIGN KEY(plan_id) REFERENCES plans(id), CHECK(total_minor >= 0), CHECK(currency = 'USD')
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS invoices (
 id CHAR(32) PRIMARY KEY, number VARCHAR(64) NOT NULL UNIQUE, user_id CHAR(32) NOT NULL,
 order_id CHAR(32) NOT NULL UNIQUE, status ENUM('unpaid','paid','void') NOT NULL,
 amount_minor BIGINT NOT NULL, currency CHAR(3) NOT NULL, issued_at DATETIME(6) NOT NULL,
 due_at DATETIME(6) NOT NULL, paid_at DATETIME(6) NULL,
 KEY ix_invoices_user_date(user_id,issued_at), FOREIGN KEY(user_id) REFERENCES users(id),
 FOREIGN KEY(order_id) REFERENCES orders(id), CHECK(amount_minor >= 0), CHECK(currency = 'USD')
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS order_requests (
 user_id CHAR(32) NOT NULL, request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 fingerprint CHAR(64) NOT NULL, order_id CHAR(32) NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
 PRIMARY KEY(user_id,request_key), FOREIGN KEY(user_id) REFERENCES users(id), FOREIGN KEY(order_id) REFERENCES orders(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS services (
 id CHAR(32) PRIMARY KEY, user_id CHAR(32) NOT NULL, name VARCHAR(253) NOT NULL,
 plan_name VARCHAR(120) NOT NULL, region VARCHAR(120) NOT NULL, ipv4 VARCHAR(45) NOT NULL DEFAULT '',
 status ENUM('pending','active','suspended','terminated') NOT NULL, next_due_at DATETIME(6) NOT NULL,
 price_minor BIGINT NOT NULL, currency CHAR(3) NOT NULL, cpu INT NOT NULL, memory_gb INT NOT NULL, disk_gb INT NOT NULL,
 KEY ix_services_user(user_id), FOREIGN KEY(user_id) REFERENCES users(id), CHECK(price_minor >= 0)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS audit_events (
 id CHAR(32) PRIMARY KEY, user_id CHAR(32) NOT NULL, action VARCHAR(64) NOT NULL,
 target VARCHAR(128) NOT NULL, created_at DATETIME(6) NOT NULL, actor_email VARCHAR(254) NOT NULL,
 detail VARCHAR(1000) NOT NULL, KEY ix_audit_user_date(user_id,created_at), FOREIGN KEY(user_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS outbox_events (
 id CHAR(32) PRIMARY KEY, aggregate_id CHAR(32) NOT NULL, event_type VARCHAR(64) NOT NULL,
 payload JSON NOT NULL, created_at DATETIME(6) NOT NULL, processed_at DATETIME(6) NULL,
 attempts INT NOT NULL DEFAULT 0, KEY ix_outbox_pending(processed_at,created_at),
 UNIQUE KEY ix_order_event(aggregate_id,event_type), FOREIGN KEY(aggregate_id) REFERENCES orders(id)
) ENGINE=InnoDB;
