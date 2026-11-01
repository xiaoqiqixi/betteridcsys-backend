CREATE TABLE IF NOT EXISTS coupons (
 id CHAR(32) NOT NULL PRIMARY KEY,
 code VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
 document JSON NOT NULL,
 revision BIGINT NOT NULL,
 CONSTRAINT coupon_revision_min CHECK(revision >= 1),
 CONSTRAINT coupon_revision_max CHECK(revision <= 9007199254740991)
) ENGINE=InnoDB;
CREATE TABLE IF NOT EXISTS coupon_redemptions (
 id CHAR(32) NOT NULL PRIMARY KEY,
 coupon_id CHAR(32) NOT NULL,
 user_id CHAR(32) NOT NULL,
 order_id CHAR(32) NOT NULL UNIQUE,
 created_at DATETIME(6) NOT NULL,
 KEY coupon_client(coupon_id,user_id),
 CONSTRAINT coupon_redemption_coupon FOREIGN KEY(coupon_id) REFERENCES coupons(id),
 CONSTRAINT coupon_redemption_user FOREIGN KEY(user_id) REFERENCES users(id),
 CONSTRAINT coupon_redemption_order FOREIGN KEY(order_id) REFERENCES orders(id)
) ENGINE=InnoDB;
