CREATE TABLE IF NOT EXISTS catalog_nodes (
 id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 region_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name VARCHAR(120) NOT NULL,
 sort_order INT NOT NULL,
 enabled BOOLEAN NOT NULL,
 CONSTRAINT catalog_nodes_region FOREIGN KEY(region_id) REFERENCES catalog_regions(id),
 CONSTRAINT catalog_nodes_sort CHECK(sort_order >= 0 AND sort_order <= 1000000),
 CONSTRAINT catalog_nodes_enabled CHECK(enabled IN (0,1))
) ENGINE=InnoDB;
ALTER TABLE plans ADD COLUMN node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;
INSERT INTO catalog_nodes(id,region_id,name,sort_order,enabled) SELECT CONCAT('n-',LEFT(SHA2(BINARY id,256),60)),id,'默认节点',0,TRUE FROM catalog_regions ON DUPLICATE KEY UPDATE id=catalog_nodes.id;
UPDATE plans SET node_id=CONCAT('n-',LEFT(SHA2(BINARY region_id,256),60)) WHERE node_id IS NULL;
ALTER TABLE plans MODIFY COLUMN node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL;
ALTER TABLE plans ADD CONSTRAINT plans_catalog_node FOREIGN KEY(node_id) REFERENCES catalog_nodes(id);
