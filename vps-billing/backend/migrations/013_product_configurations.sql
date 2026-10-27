CREATE TABLE IF NOT EXISTS product_configurations (
 plan_id VARCHAR(64) NOT NULL PRIMARY KEY,
 revision BIGINT NOT NULL,
 spec JSON NOT NULL,
 configuration_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 CONSTRAINT product_configuration_plan FOREIGN KEY(plan_id) REFERENCES plans(id),
 CONSTRAINT product_configuration_revision CHECK(revision >= 1 AND revision <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS order_product_configurations (
 order_id CHAR(32) NOT NULL PRIMARY KEY,
 plan_id VARCHAR(64) NOT NULL,
 configuration_revision BIGINT NOT NULL,
 spec JSON NOT NULL,
 configuration_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 created_at DATETIME(6) NOT NULL,
 CONSTRAINT order_product_configuration_order FOREIGN KEY(order_id) REFERENCES orders(id),
 CONSTRAINT order_product_configuration_plan FOREIGN KEY(plan_id) REFERENCES plans(id),
 CONSTRAINT order_product_configuration_revision CHECK(configuration_revision >= 1 AND configuration_revision <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS provisioning_jobs (
 id CHAR(32) NOT NULL PRIMARY KEY,
 order_id CHAR(32) NOT NULL,
 state ENUM('queued','sending','accepted','succeeded','rejected','uncertain') NOT NULL,
 request JSON NOT NULL,
 request_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 claim_token CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
 lease_expires_at DATETIME(6) NULL,
 result JSON NULL,
 attempts INT NOT NULL DEFAULT 0,
 created_at DATETIME(6) NOT NULL,
 updated_at DATETIME(6) NOT NULL,
 UNIQUE KEY provisioning_job_order(order_id),
 KEY ix_provisioning_state(state,created_at),
 CONSTRAINT provisioning_job_snapshot FOREIGN KEY(order_id) REFERENCES order_product_configurations(order_id),
 CONSTRAINT provisioning_job_attempts CHECK(attempts >= 0 AND attempts <= 1)
) ENGINE=InnoDB;
