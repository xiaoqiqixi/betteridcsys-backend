package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"strings"
)

func readServiceSummaries06(ctx context.Context, q currencyQuerier, ids []string, owner string) ([]api.AdminServiceSummary, error) {
	out := make([]api.AdminServiceSummary, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	marks, args := make([]string, len(ids)), make([]any, len(ids))
	for i, id := range ids {
		marks[i], args[i] = "?", id
	}
	query := "SELECT " + service06Columns + " FROM services s JOIN users u ON u.id=s.user_id WHERE s.id IN (" + strings.Join(marks, ",") + ")"
	if owner != "" {
		query += " AND s.user_id=?"
		args = append(args, owner)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	byID := make(map[string]api.AdminServiceSummary, len(ids))
	for rows.Next() {
		record, err := scanService06(rows)
		if err != nil {
			return out, err
		}
		v := record.View
		byID[v.ID] = api.AdminServiceSummary{ID: v.ID, Label: v.Label, ProductName: v.ProductName, Kind: v.Kind, Status: v.Status, Region: v.Region, Country: v.Country, Hostname: v.Hostname, PrimaryIPv4: v.PrimaryIPv4, Cycle: v.Cycle, Recurring: v.Recurring, NextDueAt: v.NextDueAt, AutoRenew: v.AutoRenew, Power: v.Power, CreatedAt: v.CreatedAt, ClientID: v.ClientID, ClientEmail: v.ClientEmail}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, id := range ids {
		v, ok := byID[id]
		if !ok {
			return out, ErrNotFound
		}
		out = append(out, v)
	}
	return out, nil
}

func customerServiceSummary06(v api.AdminServiceSummary) api.ServiceSummary {
	return api.ServiceSummary{ID: v.ID, Label: v.Label, ProductName: v.ProductName, Kind: v.Kind, Status: v.Status, Region: v.Region, Country: v.Country, Hostname: v.Hostname, PrimaryIPv4: v.PrimaryIPv4, Cycle: v.Cycle, Recurring: v.Recurring, NextDueAt: v.NextDueAt, AutoRenew: v.AutoRenew, Power: v.Power, CreatedAt: v.CreatedAt}
}
