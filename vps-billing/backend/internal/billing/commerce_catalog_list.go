package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Fetch children in bounded batches inside the caller's snapshot transaction.
// Join through their parents to return canonical IDs: MySQL's collation can
// match child foreign keys whose case differs from the parent's stored spelling.
func readProductListChildren06(ctx context.Context, q currencyQuerier, products []api.AdminProduct, currency string, config CurrencyConfiguration, admin bool) ([]api.AdminProduct, error) {
	for start := 0; start < len(products); start += 100 {
		batch := products[start:min(start+100, len(products))]
		byID := make(map[string]*api.AdminProduct, len(batch))
		args := make([]any, len(batch))
		marks := make([]string, len(batch))
		for i := range batch {
			byID[batch[i].ID], args[i], marks[i] = &batch[i], batch[i].ID, "?"
		}
		where := " WHERE p.id IN (" + strings.Join(marks, ",") + ")"
		read := func(query string, values []any, scan func(*sql.Rows) error) error {
			rows, err := q.QueryContext(ctx, query, values...)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				if err := scan(rows); err != nil {
					return err
				}
			}
			return rows.Err()
		}
		priceWhere, priceArgs := where, args
		if currency != "" {
			priceWhere += " AND BINARY x.currency=BINARY ?"
			priceArgs = append(append([]any{}, args...), currency)
		}
		visiblePrice := func(price api.PricePoint) bool {
			c, ok := config.find(price.Currency)
			return admin || ok && c.Enabled
		}
		if err := read(`SELECT p.id,x.cycle,x.currency,x.recurring_minor,x.setup_minor,x.decimal_places
 FROM products p JOIN product_prices x ON x.product_id=p.id`+priceWhere+" ORDER BY p.id,x.currency,x.cycle", priceArgs, func(rows *sql.Rows) error {
			var id string
			var price api.PricePoint
			if err := rows.Scan(&id, &price.Cycle, &price.Currency, &price.RecurringMinor, &price.SetupMinor, &price.DecimalPlaces); err != nil {
				return err
			}
			if visiblePrice(price) {
				byID[id].Prices = append(byID[id].Prices, price)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if err := read(`SELECT p.id,o.id,o.option_key,o.kind,o.label_i18n,o.required,o.min_quantity,o.max_quantity
 FROM products p JOIN product_options o ON o.product_id=p.id`+where+" ORDER BY p.id,o.sort_order,o.id", args, func(rows *sql.Rows) error {
			var id string
			var option api.ProductOption
			var raw []byte
			var minimum, maximum sql.NullInt64
			if err := rows.Scan(&id, &option.ID, &option.Key, &option.Kind, &raw, &option.Required, &minimum, &maximum); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &option.Label); err != nil {
				return err
			}
			option.Min, option.Max = minimum.Int64, maximum.Int64
			option.Choices = []api.OptionChoice{}
			if option.Kind == "quantity" {
				option.UnitPrices = []api.PricePoint{}
			}
			byID[id].Options = append(byID[id].Options, option)
			return nil
		}); err != nil {
			return nil, err
		}
		type optionKey struct{ product, option string }
		type choiceKey struct{ product, option, choice string }
		options := map[optionKey]*api.ProductOption{}
		for i := range batch {
			for j := range batch[i].Options {
				o := &batch[i].Options[j]
				options[optionKey{batch[i].ID, o.ID}] = o
			}
		}
		if len(options) > 0 {
			if err := read(`SELECT p.id,o.id,c.id,c.label_i18n
 FROM products p JOIN product_options o ON o.product_id=p.id
 JOIN product_option_choices c ON c.product_id=p.id AND c.option_id=o.id`+where+" ORDER BY p.id,o.id,c.sort_order,c.id", args, func(rows *sql.Rows) error {
				var pid, oid string
				var choice api.OptionChoice
				var raw []byte
				if err := rows.Scan(&pid, &oid, &choice.ID, &raw); err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &choice.Label); err != nil {
					return err
				}
				choice.Prices = []api.PricePoint{}
				o := options[optionKey{pid, oid}]
				o.Choices = append(o.Choices, choice)
				return nil
			}); err != nil {
				return nil, err
			}
			choices := map[choiceKey]*api.OptionChoice{}
			for key, option := range options {
				for i := range option.Choices {
					choice := &option.Choices[i]
					choices[choiceKey{key.product, key.option, choice.ID}] = choice
				}
			}
			if err := read(`SELECT p.id,o.id,COALESCE(c.id,''),x.cycle,x.currency,x.recurring_minor,x.setup_minor,x.decimal_places
 FROM products p JOIN option_prices x ON x.product_id=p.id
 JOIN product_options o ON o.product_id=p.id AND o.id=x.option_id
 LEFT JOIN product_option_choices c ON c.product_id=p.id AND c.option_id=o.id AND c.id=x.choice_id`+priceWhere+" AND (x.choice_id='' OR c.id IS NOT NULL) ORDER BY p.id,o.id,c.id,x.currency,x.cycle", priceArgs, func(rows *sql.Rows) error {
				var pid, oid, cid string
				var price api.PricePoint
				if err := rows.Scan(&pid, &oid, &cid, &price.Cycle, &price.Currency, &price.RecurringMinor, &price.SetupMinor, &price.DecimalPlaces); err != nil {
					return err
				}
				if visiblePrice(price) {
					if cid == "" {
						o := options[optionKey{pid, oid}]
						if o.Kind == "quantity" {
							o.UnitPrices = append(o.UnitPrices, price)
						}
					} else if choice := choices[choiceKey{pid, oid, cid}]; choice != nil {
						choice.Prices = append(choice.Prices, price)
					}
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
		if err := read(`SELECT p.id,i.id,i.family,i.name,i.version,i.min_disk_gb
 FROM products p JOIN product_os_images pi ON pi.product_id=p.id JOIN os_images i ON i.id=pi.image_id`+where+" AND (? OR i.enabled=TRUE) ORDER BY p.id,i.id", append(append([]any{}, args...), admin), func(rows *sql.Rows) error {
			var pid string
			var image api.OsImage
			if err := rows.Scan(&pid, &image.ID, &image.Family, &image.Name, &image.Version, &image.MinDiskGB); err != nil {
				return err
			}
			byID[pid].OSImages = append(byID[pid].OSImages, image)
			return nil
		}); err != nil {
			return nil, err
		}
		for i := range batch {
			batch[i].Available = batch[i].Available && len(batch[i].Prices) > 0
		}
	}
	return products, nil
}
