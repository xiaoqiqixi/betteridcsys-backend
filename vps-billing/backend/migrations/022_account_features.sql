CREATE TABLE IF NOT EXISTS ssh_keys (
 id CHAR(32) PRIMARY KEY,
 user_id CHAR(32) NOT NULL,
 name VARCHAR(80) NOT NULL,
 public_key TEXT NOT NULL,
 algorithm VARCHAR(40) NOT NULL,
 fingerprint VARCHAR(100) NOT NULL,
 created_at DATETIME(6) NOT NULL,
 UNIQUE KEY ssh_keys_owner_fingerprint(user_id,fingerprint),
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS api_tokens (
 id CHAR(32) PRIMARY KEY,
 user_id CHAR(32) NOT NULL,
 token_hash CHAR(64) NOT NULL UNIQUE,
 name VARCHAR(60) NOT NULL,
 prefix VARCHAR(16) NOT NULL,
 scopes JSON NOT NULL,
 ip_allowlist JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 last_used_at DATETIME(6) NULL,
 expires_at DATETIME(6) NULL,
 INDEX api_tokens_owner(user_id),
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS support_departments (
 id VARCHAR(64) PRIMARY KEY,
 document JSON NOT NULL,
 visible BOOLEAN NOT NULL,
 sort_order BIGINT NOT NULL
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS tickets (
 id CHAR(32) PRIMARY KEY,
 number VARCHAR(64) NOT NULL UNIQUE,
 user_id CHAR(32) NOT NULL,
 department_id VARCHAR(64) NOT NULL,
 subject VARCHAR(200) NOT NULL,
 status VARCHAR(20) NOT NULL,
 priority VARCHAR(6) NOT NULL,
 service_id CHAR(32) NULL,
 assignee_id CHAR(32) NULL,
 created_at DATETIME(6) NOT NULL,
 last_reply_at DATETIME(6) NOT NULL,
 INDEX tickets_owner(user_id,last_reply_at,id),
 INDEX tickets_queue(status,department_id,last_reply_at,id),
 FOREIGN KEY (user_id) REFERENCES users(id),
 FOREIGN KEY (department_id) REFERENCES support_departments(id),
 FOREIGN KEY (service_id) REFERENCES services(id),
 FOREIGN KEY (assignee_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS ticket_messages (
 id CHAR(32) PRIMARY KEY,
 ticket_id CHAR(32) NOT NULL,
 author_id CHAR(32) NULL,
 author_type VARCHAR(6) NOT NULL,
 author_name VARCHAR(120) NOT NULL,
 body MEDIUMTEXT NOT NULL,
 internal BOOLEAN NOT NULL,
 created_at DATETIME(6) NOT NULL,
 INDEX ticket_messages_thread(ticket_id,created_at,id),
 FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE,
 FOREIGN KEY (author_id) REFERENCES users(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS announcements (
 id CHAR(32) PRIMARY KEY,
 title JSON NOT NULL,
 body JSON NOT NULL,
 pinned BOOLEAN NOT NULL,
 published BOOLEAN NOT NULL,
 published_at DATETIME(6) NOT NULL,
 INDEX announcements_public(published,pinned,published_at,id)
) ENGINE=InnoDB;
INSERT IGNORE INTO support_departments(id,document,visible,sort_order) VALUES
 ('billing',JSON_OBJECT('id','billing','name',JSON_OBJECT('en','Billing','zh-CN','账务'),'description',JSON_OBJECT('en','Invoices and payments','zh-CN','账单与付款'),'visible',JSON_EXTRACT('true','$'),'sort_order',10),TRUE,10),
 ('technical',JSON_OBJECT('id','technical','name',JSON_OBJECT('en','Technical support','zh-CN','技术支持'),'description',JSON_OBJECT('en','Help with your services','zh-CN','服务技术支持'),'visible',JSON_EXTRACT('true','$'),'sort_order',20),TRUE,20);
