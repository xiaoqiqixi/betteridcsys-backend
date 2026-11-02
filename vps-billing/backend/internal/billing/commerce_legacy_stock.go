package billing

import (
	"context"
	"database/sql"
	"errors"
)

// Deprecated checkouts share the stock of a migrated product. Their historical
// payment and provisioning records retain the 0.5 format.
func reserveLegacyStock06(ctx context.Context, tx *sql.Tx, productID string) (bool, error) {
	var stock sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT stock FROM products WHERE id=? FOR UPDATE", productID).Scan(&stock)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // A plan created through the deprecated catalog only.
	}
	if err != nil || !stock.Valid {
		return false, err
	}
	if stock.Int64 <= 0 {
		return false, ErrUnavailable
	}
	_, err = tx.ExecContext(ctx, "UPDATE products SET stock=stock-1 WHERE id=?", productID)
	return err == nil, err
}

func recordLegacyStock06(ctx context.Context, tx *sql.Tx, order Order, plan Plan) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO order_items(id,order_id,position,product_id,product_name,cycle,quantity,hostname,options_summary,recurring_minor,total_minor,stock_reserved,snapshot)
 VALUES(?,?,0,?,?,'monthly',1,?,'[]',?,?,1,JSON_OBJECT('legacy',JSON_EXTRACT('true','$')))`, ID(), order.ID, plan.ID, plan.Name, order.Hostname, order.TotalMinor, order.TotalMinor)
	return err
}

// Call with the order locked and its unpaid cancellation state verified.
func releaseOrderStock06(ctx context.Context, tx *sql.Tx, orderID string) error {
	rows, err := tx.QueryContext(ctx, "SELECT product_id,SUM(stock_reserved) FROM order_items WHERE order_id=? AND stock_reserved>0 GROUP BY product_id ORDER BY product_id", orderID)
	if err != nil {
		return err
	}
	type reservation struct {
		id       string
		quantity int64
	}
	reservations := []reservation{}
	for rows.Next() {
		var r reservation
		if err = rows.Scan(&r.id, &r.quantity); err != nil {
			rows.Close()
			return err
		}
		reservations = append(reservations, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range reservations {
		if _, err = tx.ExecContext(ctx, "UPDATE products SET stock=stock+? WHERE id=? AND stock IS NOT NULL", r.quantity, r.id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE order_items SET stock_reserved=0 WHERE order_id=?", orderID)
	return err
}
