CREATE TABLE IF NOT EXISTS server_connections (
 id VARCHAR(64) PRIMARY KEY,
 module_id VARCHAR(64) NOT NULL,
 name VARCHAR(80) NOT NULL,
 enabled BOOLEAN NOT NULL DEFAULT TRUE,
 status VARCHAR(16) NOT NULL DEFAULT 'unconfigured',
 description JSON NULL,
 last_checked_at DATETIME(6) NULL,
 revision BIGINT NOT NULL DEFAULT 1
) ENGINE=InnoDB;
ALTER TABLE products ADD COLUMN module JSON NULL;
ALTER TABLE products ADD COLUMN order_schema JSON NULL;
ALTER TABLE services ADD COLUMN module_id VARCHAR(64) NULL;
ALTER TABLE services ADD COLUMN connection_id VARCHAR(64) NULL;
ALTER TABLE os_images ADD COLUMN connection_id VARCHAR(64) NOT NULL DEFAULT '';
UPDATE os_images SET connection_id=controller_id WHERE connection_id='';
