package billing

import "net/http"

// Only the administrator projection exposes ownership. The embedded Service
// keeps the customer response's fields and historical currency formatting intact.
type adminServiceView struct {
	Service
	CustomerID string `json:"customer_id"`
}

func (a *App) adminServicesHTTP(w http.ResponseWriter, r *http.Request) {
	services, err := a.Store.Services(r.Context(), "")
	if err != nil {
		a.internal(w, r, err)
		return
	}
	out := make([]adminServiceView, 0, len(services))
	for _, service := range services {
		out = append(out, adminServiceView{Service: service, CustomerID: service.UserID})
	}
	jsonOut(w, http.StatusOK, out)
}
