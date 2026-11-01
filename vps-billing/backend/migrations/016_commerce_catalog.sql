CREATE TABLE IF NOT EXISTS products (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 slug VARCHAR(64) NOT NULL UNIQUE,
 kind ENUM('cloud_server','dedicated_server','generic') NOT NULL,
 node_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 name_i18n JSON NOT NULL,
 description_i18n JSON NOT NULL,
 specs JSON NOT NULL,
 badges JSON NOT NULL,
 stock BIGINT NULL,
 visibility ENUM('public','hidden','retired') NOT NULL,
 controller_id VARCHAR(64) NULL,
 remote_product_id VARCHAR(128) NULL,
 sort_order BIGINT NOT NULL,
 revision BIGINT NOT NULL,
 CONSTRAINT product_node FOREIGN KEY(node_id) REFERENCES catalog_nodes(id),
 CONSTRAINT product_stock_min CHECK(stock >= 0),
 CONSTRAINT product_stock_max CHECK(stock <= 9007199254740991),
 CONSTRAINT product_revision_min CHECK(revision >= 1),
 CONSTRAINT product_revision_max CHECK(revision <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS product_prices (
 product_id VARCHAR(64) NOT NULL,
 currency CHAR(3) NOT NULL,
 cycle VARCHAR(16) NOT NULL,
 recurring_minor BIGINT NOT NULL,
 setup_minor BIGINT NOT NULL,
 decimal_places INT NOT NULL,
 PRIMARY KEY(product_id,currency,cycle),
 CONSTRAINT product_price_product FOREIGN KEY(product_id) REFERENCES products(id),
 CONSTRAINT product_price_recurring_min CHECK(recurring_minor >= 0),
 CONSTRAINT product_price_recurring_max CHECK(recurring_minor <= 9007199254740991),
 CONSTRAINT product_price_setup_min CHECK(setup_minor >= 0),
 CONSTRAINT product_price_setup_max CHECK(setup_minor <= 9007199254740991),
 CONSTRAINT product_price_precision_min CHECK(decimal_places >= 0),
 CONSTRAINT product_price_precision_max CHECK(decimal_places <= 6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS product_options (
 product_id VARCHAR(64) NOT NULL,
 id VARCHAR(64) NOT NULL,
 option_key VARCHAR(48) NOT NULL,
 kind ENUM('select','quantity','os_image') NOT NULL,
 label_i18n JSON NOT NULL,
 required BOOLEAN NOT NULL,
 min_quantity BIGINT NULL,
 max_quantity BIGINT NULL,
 sort_order INT NOT NULL,
 PRIMARY KEY(product_id,id),
 UNIQUE KEY product_option_key(product_id,option_key),
 CONSTRAINT product_option_product FOREIGN KEY(product_id) REFERENCES products(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS product_option_choices (
 product_id VARCHAR(64) NOT NULL,
 option_id VARCHAR(64) NOT NULL,
 id VARCHAR(64) NOT NULL,
 label_i18n JSON NOT NULL,
 sort_order INT NOT NULL,
 PRIMARY KEY(product_id,option_id,id),
 CONSTRAINT product_choice_product FOREIGN KEY(product_id) REFERENCES products(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS option_prices (
 product_id VARCHAR(64) NOT NULL,
 option_id VARCHAR(64) NOT NULL,
 choice_id VARCHAR(64) NOT NULL,
 currency CHAR(3) NOT NULL,
 cycle VARCHAR(16) NOT NULL,
 recurring_minor BIGINT NOT NULL,
 setup_minor BIGINT NOT NULL,
 decimal_places INT NOT NULL,
 PRIMARY KEY(product_id,option_id,choice_id,currency,cycle),
 CONSTRAINT option_price_product FOREIGN KEY(product_id) REFERENCES products(id),
 CONSTRAINT option_price_recurring_min CHECK(recurring_minor >= 0),
 CONSTRAINT option_price_recurring_max CHECK(recurring_minor <= 9007199254740991),
 CONSTRAINT option_price_setup_min CHECK(setup_minor >= 0),
 CONSTRAINT option_price_setup_max CHECK(setup_minor <= 9007199254740991),
 CONSTRAINT option_price_precision_min CHECK(decimal_places >= 0),
 CONSTRAINT option_price_precision_max CHECK(decimal_places <= 6)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS os_images (
 id VARCHAR(64) NOT NULL PRIMARY KEY,
 family VARCHAR(16) NOT NULL,
 name VARCHAR(80) NOT NULL,
 version VARCHAR(40) NOT NULL,
 min_disk_gb BIGINT NOT NULL,
 enabled BOOLEAN NOT NULL,
 controller_id VARCHAR(64) NOT NULL,
 remote_image_id VARCHAR(128) NOT NULL,
 sha256 VARCHAR(64) NOT NULL,
 CONSTRAINT os_image_min_disk CHECK(min_disk_gb >= 1)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS product_os_images (
 product_id VARCHAR(64) NOT NULL,
 image_id VARCHAR(64) NOT NULL,
 PRIMARY KEY(product_id,image_id),
 CONSTRAINT product_os_product FOREIGN KEY(product_id) REFERENCES products(id),
 CONSTRAINT product_os_image FOREIGN KEY(image_id) REFERENCES os_images(id)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS commerce_requests (
 user_id CHAR(32) NOT NULL,
 operation VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 request_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 fingerprint CHAR(64) NOT NULL,
 response JSON NOT NULL,
 created_at DATETIME(6) NOT NULL,
 PRIMARY KEY(user_id,operation,request_key),
 CONSTRAINT commerce_request_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS commerce_taxonomy (
 id TINYINT NOT NULL PRIMARY KEY,
 document JSON NOT NULL,
 CONSTRAINT commerce_taxonomy_singleton CHECK(id = 1)
) ENGINE=InnoDB;
INSERT INTO products(id,slug,kind,node_id,name_i18n,description_i18n,specs,badges,stock,visibility,controller_id,remote_product_id,sort_order,revision)
 SELECT p.id,IF(REGEXP_LIKE(p.id,'^[a-z0-9-]{1,64}$','c'),p.id,CONCAT('legacy-',LEFT(SHA2(p.id,256),48))),'generic',p.node_id,JSON_OBJECT('zh-CN',p.name,'en',p.name),JSON_OBJECT('zh-CN',p.description,'en',p.description),JSON_OBJECT('cpu_cores',p.cpu,'memory_mb',p.memory_gb*1024,'disk_gb',p.disk_gb,'bandwidth_mbps',0,'traffic_gb',p.transfer_tb*1024,'ipv4',1,'ipv6',JSON_EXTRACT('false','$')),JSON_ARRAY(),NULL,IF(p.available,'public','hidden'),NULL,NULL,0,1 FROM plans p WHERE NOT EXISTS (SELECT 1 FROM products n WHERE n.id=p.id);
INSERT INTO product_prices(product_id,currency,cycle,recurring_minor,setup_minor,decimal_places)
 SELECT p.plan_id,p.currency,'monthly',p.amount_minor,0,c.decimal_places
 FROM plan_prices p JOIN products n ON n.id=p.plan_id JOIN currency_settings s ON s.id=1
 JOIN JSON_TABLE(s.document,'$.currencies[*]' COLUMNS(code VARCHAR(3) PATH '$.code',decimal_places INT PATH '$.decimal_places')) c ON BINARY c.code=BINARY p.currency
 WHERE p.available=TRUE AND NOT EXISTS(SELECT 1 FROM product_prices x WHERE x.product_id=p.plan_id AND BINARY x.currency=BINARY p.currency AND x.cycle='monthly');
