CREATE TABLE IF NOT EXISTS staff_roles (
 id VARCHAR(64) PRIMARY KEY,
 name VARCHAR(60) NOT NULL,
 description VARCHAR(240) NOT NULL,
 permissions JSON NOT NULL,
 builtin BOOLEAN NOT NULL,
 revision BIGINT NOT NULL,
 CONSTRAINT staff_role_revision CHECK(revision >= 1 AND revision <= 9007199254740991),
 CONSTRAINT staff_role_builtin CHECK(builtin IN (0,1))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS staff_members (
 user_id CHAR(32) PRIMARY KEY,
 role_id VARCHAR(64) NOT NULL,
 last_login_at DATETIME(6) NULL,
 FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
 FOREIGN KEY (role_id) REFERENCES staff_roles(id)
) ENGINE=InnoDB;
INSERT IGNORE INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES('owner','Owner','All permissions',JSON_ARRAY('overview.read','clients.read','clients.write','clients.credit','orders.read','orders.write','invoices.read','invoices.write','invoices.refund','services.read','services.write','services.power','services.terminate','products.read','products.write','promotions.write','tickets.read','tickets.write','content.write','automation.write','integrations.write','staff.write','settings.write','audit.read','updates.write'),TRUE,1);
INSERT IGNORE INTO staff_members(user_id,role_id) SELECT id,'owner' FROM users WHERE role='admin';
