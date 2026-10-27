CREATE TABLE IF NOT EXISTS catalog_types (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 name VARCHAR(120) NOT NULL,
 sort_order INT NOT NULL,
 enabled BOOLEAN NOT NULL,
 CONSTRAINT catalog_types_sort CHECK(sort_order >= 0 AND sort_order <= 1000000),
 CONSTRAINT catalog_types_enabled CHECK(enabled IN (0,1))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS catalog_regions (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 type_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(120) NOT NULL,
 sort_order INT NOT NULL,
 enabled BOOLEAN NOT NULL,
 CONSTRAINT catalog_regions_type FOREIGN KEY(type_id) REFERENCES catalog_types(id),
 CONSTRAINT catalog_regions_sort CHECK(sort_order >= 0 AND sort_order <= 1000000),
 CONSTRAINT catalog_regions_enabled CHECK(enabled IN (0,1))
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS catalog_taxonomy_settings (
 id TINYINT NOT NULL PRIMARY KEY,
 revision BIGINT NOT NULL,
 CONSTRAINT catalog_taxonomy_singleton CHECK(id = 1),
 CONSTRAINT catalog_taxonomy_revision CHECK(revision >= 1 AND revision <= 9007199254740991)
) ENGINE=InnoDB;
ALTER TABLE plans ADD COLUMN type_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;
ALTER TABLE plans ADD COLUMN region_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;
INSERT INTO catalog_types(id,name,sort_order,enabled) VALUES('generic','通用服务器',0,TRUE) ON DUPLICATE KEY UPDATE id=id;
INSERT INTO catalog_regions(id,type_id,name,sort_order,enabled) SELECT CONCAT('r-',LEFT(SHA2(MIN(BINARY region),256),60)),'generic',MIN(region),0,TRUE FROM plans GROUP BY BINARY region ON DUPLICATE KEY UPDATE id=catalog_regions.id;
INSERT INTO catalog_taxonomy_settings(id,revision) VALUES(1,1) ON DUPLICATE KEY UPDATE id=id;
UPDATE plans SET type_id='generic',region_id=CONCAT('r-',LEFT(SHA2(BINARY region,256),60)) WHERE type_id IS NULL OR region_id IS NULL;
ALTER TABLE plans MODIFY COLUMN type_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE plans MODIFY COLUMN region_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE plans ADD CONSTRAINT plans_catalog_type FOREIGN KEY(type_id) REFERENCES catalog_types(id);
ALTER TABLE plans ADD CONSTRAINT plans_catalog_region FOREIGN KEY(region_id) REFERENCES catalog_regions(id);
