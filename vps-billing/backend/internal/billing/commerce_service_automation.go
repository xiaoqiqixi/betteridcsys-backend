package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"time"
)

func (a *App) registerServiceDispatch06() {
	_ = a.RegisterAutomationJob("dispatch_provisioning", func(ctx context.Context, settings api.AutomationSettings, at time.Time) (AutomationResult, error) {
		m, ok := a.Store.(*MySQL)
		if !ok {
			return AutomationResult{}, ErrCommerceUnavailable
		}
		rows, err := m.DB.QueryContext(ctx, `SELECT t.id,t.status,t.action FROM service_tasks t JOIN services s ON s.id=t.service_id WHERE t.status IN ('queued','running','uncertain') AND s.controller_id IS NOT NULL AND (t.action<>'provision' OR t.status<>'queued' OR ?=TRUE) AND t.updated_at<=? ORDER BY t.updated_at,t.id LIMIT 50`, settings.AutoProvision, at.Add(-15*time.Second))
		if err != nil {
			return AutomationResult{}, err
		}
		type target struct{ ID, Status, Action string }
		targets := []target{}
		for rows.Next() {
			var t target
			if err = rows.Scan(&t.ID, &t.Status, &t.Action); err != nil {
				rows.Close()
				return AutomationResult{}, err
			}
			targets = append(targets, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return AutomationResult{}, err
		}
		result := AutomationResult{}
		for _, t := range targets {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			task, err := a.DispatchServiceTask06(ctx, t.ID, t.Status != "queued", false)
			if err != nil {
				result.Failed++
				_, _ = m.DB.ExecContext(ctx, "UPDATE service_tasks SET updated_at=UTC_TIMESTAMP(6) WHERE id=? AND status IN ('queued','running','uncertain')", t.ID)
			} else if task.Status != t.Status {
				result.Processed++
			}
		}
		// Rotate one approved catalog per tick so many unavailable controllers cannot
		// starve lifecycle work. Newly discovered images require administrator enablement.
		if runtime, ok := a.Config.Services.(*ServiceControllers06); ok && runtime != nil && runtime.Services != nil && len(runtime.IDs) > 0 && ctx.Err() == nil {
			controller := runtime.IDs[int(at.Unix()/60)%len(runtime.IDs)]
			client, err := runtime.Services(ctx, controller)
			if err == nil {
				images, e := client.Images(ctx, digest("catalog:" + controller)[:32])
				err = e
				if err == nil {
					err = m.SyncImages06(ctx, controller, images)
				}
			}
			if err != nil {
				result.Failed++
			}
		}
		return result, nil
	})
}
