package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var commerceCouponColumns = map[string]string{
	"coupons":            "id:char(32)! code:varchar(32)! document:json! revision:bigint!",
	"coupon_redemptions": "id:char(32)! coupon_id:char(32)! user_id:char(32)! order_id:char(32)! created_at:datetime(6)!",
}

func prepareCommerceCouponMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	for table := range commerceCouponColumns {
		if !strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			continue
		}
		var kind, engine, collation string
		err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind, &engine, &collation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if kind != "BASE TABLE" || engine != "InnoDB" {
			return "", false, errors.New("unexpected coupon schema object")
		}
		if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	return "", false, errors.New("unsupported coupon migration")
}
