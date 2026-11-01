ALTER TABLE users ADD COLUMN currency CHAR(3) NOT NULL DEFAULT 'CNY';
ALTER TABLE users ADD COLUMN currency_locked BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD CONSTRAINT users_currency_locked CHECK(currency_locked IN (0,1));
CREATE TABLE IF NOT EXISTS account_profiles (
 user_id CHAR(32) PRIMARY KEY,
 document JSON NOT NULL,
 locale VARCHAR(5) NOT NULL,
 email_verified BOOLEAN NOT NULL,
 notes TEXT NOT NULL,
 closed BOOLEAN NOT NULL,
 referrer_id CHAR(32) NULL,
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
 FOREIGN KEY (referrer_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS account_sessions (
 id CHAR(32) PRIMARY KEY,
 token_hash CHAR(64) NOT NULL UNIQUE,
 ip VARCHAR(45) NOT NULL,
 user_agent VARCHAR(512) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 last_seen_at DATETIME(6) NOT NULL,
 FOREIGN KEY (token_hash) REFERENCES sessions(token_hash) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS email_tokens (
 token_hash CHAR(64) PRIMARY KEY,
 user_id CHAR(32) NOT NULL,
 purpose VARCHAR(32) NOT NULL,
 expires_at DATETIME(6) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX email_tokens_user(user_id,purpose),
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS login_events (
 id CHAR(32) PRIMARY KEY,
 user_id CHAR(32) NULL,
 email_hash CHAR(64) NOT NULL,
 ip VARCHAR(45) NOT NULL,
 user_agent VARCHAR(512) NOT NULL,
 result VARCHAR(7) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX login_events_user(user_id,created_at,id),
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS notification_prefs (
 user_id CHAR(32) PRIMARY KEY,
 document JSON NOT NULL,
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS account_settings (
 name VARCHAR(32) PRIMARY KEY,
 document JSON NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS account_requests (
 principal CHAR(64) NOT NULL,
 operation VARCHAR(128) NOT NULL,
 request_key VARCHAR(128) NOT NULL,
 fingerprint CHAR(64) NOT NULL,
 response MEDIUMTEXT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(principal,operation,request_key)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS email_templates (
 template_key VARCHAR(32) PRIMARY KEY,
 subject JSON NOT NULL,
 body JSON NOT NULL,
 enabled BOOLEAN NOT NULL,
 revision BIGINT NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS email_outbox (
 id CHAR(32) PRIMARY KEY,
 user_id CHAR(32) NOT NULL,
 template_key VARCHAR(32) NOT NULL,
 variables JSON NOT NULL,
 protected_variables MEDIUMTEXT NULL,
 attempts INT NOT NULL,
 next_attempt_at DATETIME(6) NOT NULL,
 sent_at DATETIME(6) NULL,
 failed_at DATETIME(6) NULL,
 last_error VARCHAR(64) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX email_outbox_due(sent_at,failed_at,next_attempt_at),
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliates (
 user_id CHAR(32) PRIMARY KEY,
 code VARCHAR(64) NOT NULL UNIQUE,
 created_at DATETIME(6) NOT NULL,
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS affiliate_referrals (
 client_id CHAR(32) PRIMARY KEY,
 affiliate_id CHAR(32) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 converted BOOLEAN NOT NULL,
 FOREIGN KEY (client_id) REFERENCES users(id) ON DELETE CASCADE,
 FOREIGN KEY (affiliate_id) REFERENCES affiliates(user_id)
) ENGINE=InnoDB;
UPDATE users SET currency=COALESCE((SELECT i.currency FROM invoices i WHERE i.user_id=users.id ORDER BY i.issued_at,i.id LIMIT 1),(SELECT JSON_UNQUOTE(JSON_EXTRACT(document,'$.default_currency')) FROM currency_settings WHERE id=1),'CNY'), currency_locked=EXISTS(SELECT 1 FROM invoices i WHERE i.user_id=users.id);
INSERT IGNORE INTO account_sessions(id,token_hash,ip,user_agent,created_at,last_seen_at) SELECT LEFT(SHA2(CONCAT('session:',token_hash),256),32),token_hash,'','',LEAST(expires_at,UTC_TIMESTAMP(6)),LEAST(expires_at,UTC_TIMESTAMP(6)) FROM sessions;
INSERT IGNORE INTO account_settings(name,document) VALUES('security',JSON_OBJECT('client_session_hours',8,'staff_session_hours',8,'password_min_length',10,'revision',1));
