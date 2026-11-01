package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

var ErrCommerceInput = errors.New("invalid commerce input")
var ErrRevisionConflict = errors.New("commerce revision conflict")
var ErrCommerceUnavailable = errors.New("commerce resource unavailable")

type commerceCatalogStore interface {
	Products06(context.Context, bool, string, string, string) ([]api.AdminProduct, error)
	Product06(context.Context, bool, string) (api.AdminProduct, error)
	SaveProduct06(context.Context, User, string, api.AdminCreateProductInput, string, bool) (api.AdminProduct, error)
	Images06(context.Context) ([]api.AdminOsImage, error)
	PatchImage06(context.Context, User, string, *string, *bool) (api.AdminOsImage, error)
}

func commerceBody(w http.ResponseWriter, r *http.Request, operation string, out any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "Use application/json")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil || uniqueJSON(raw) != nil || api.ValidateInput(operation, raw) != nil {
		fail(w, r, 400, "VALIDATION_ERROR", "Request does not match the API contract")
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, r, 400, "VALIDATION_ERROR", "Invalid request fields")
		return false
	}
	return true
}
func (a *App) commerceError(w http.ResponseWriter, r *http.Request, err error) {
	var apiError *apiProblem
	if errors.As(err, &apiError) {
		fail(w, r, apiError.status, apiError.code, apiError.message)
		return
	}
	switch {
	case errors.Is(err, ErrPaymentState):
		fail(w, r, 409, "INVOICE_NOT_PAYABLE", "Invoice state does not permit this operation")
	case errors.Is(err, ErrPaymentConflict), errors.Is(err, ErrPaymentUncertain):
		fail(w, r, 409, "PAYMENT_PENDING", "Resolve the existing payment before changing this invoice")
	case errors.Is(err, ErrCurrency):
		fail(w, r, 409, "CURRENCY_MISMATCH", "Use the account currency and its enabled price")
	case errors.Is(err, ErrInsufficientCredit):
		fail(w, r, 409, "INSUFFICIENT_CREDIT", "Insufficient account credit")
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "Resource not found")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "Permission denied")
	case errors.Is(err, ErrRevisionConflict):
		fail(w, r, 409, "REVISION_CONFLICT", "Reload the current revision")
	case errors.Is(err, ErrConflict):
		fail(w, r, 409, "IDEMPOTENCY_KEY_REUSED", "This key was used with a different request")
	case errors.Is(err, commerce.ErrQuoteChanged):
		fail(w, r, 409, "QUOTE_CHANGED", "The quote changed or expired; request a new quote")
	case errors.Is(err, ErrCommerceInput), errors.Is(err, commerce.ErrInput), errors.Is(err, commerce.ErrMoney):
		fail(w, r, 400, "VALIDATION_ERROR", "Check the product, currency, price and option configuration")
	case errors.Is(err, ErrCommerceUnavailable), errors.Is(err, commerce.ErrProductUnavailable):
		fail(w, r, 409, "PRODUCT_UNAVAILABLE", "Product is unavailable")
	default:
		a.internal(w, r, err)
	}
}
func publicProduct(p api.AdminProduct) api.Product {
	return api.Product{ID: p.ID, Slug: p.Slug, Kind: p.Kind, TypeID: p.TypeID, RegionID: p.RegionID, NodeID: p.NodeID, Name: p.Name, Description: p.Description, Badges: p.Badges, Specs: p.Specs, Prices: p.Prices, Options: p.Options, OSImages: p.OSImages, Stock: p.Stock, Available: p.Available, SortOrder: p.SortOrder}
}
func imagePublic(v api.AdminOsImage) api.OsImage {
	return api.OsImage{ID: v.ID, Family: v.Family, Name: v.Name, Version: v.Version, MinDiskGB: v.MinDiskGB}
}
func text06(v api.LocalizedText) string {
	if v["en"] != "" {
		return v["en"]
	}
	return v["zh-CN"]
}

func (a *App) commerceCatalogHTTP(w http.ResponseWriter, r *http.Request, p string, actor User) bool {
	admin := strings.HasPrefix(p, "/admin/")
	path := strings.TrimPrefix(p, "/admin")
	if path != "/products" && !strings.HasPrefix(path, "/products/") && path != "/os-images" && !strings.HasPrefix(path, "/os-images/") {
		return false
	}
	s, ok := a.Store.(commerceCatalogStore)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Commerce storage is unavailable")
		return true
	}
	id := strings.TrimPrefix(path, "/products/")
	if path == "/products" {
		id = ""
	}
	duplicate := strings.HasSuffix(id, "/duplicate")
	if duplicate {
		id = strings.TrimSuffix(id, "/duplicate")
	}
	if id != "" && !validCatalogID(id) && !strings.HasPrefix(path, "/os-images") {
		fail(w, r, 404, "NOT_FOUND", "Resource not found")
		return true
	}
	if strings.HasPrefix(path, "/os-images") {
		if !admin {
			fail(w, r, 404, "NOT_FOUND", "Resource not found")
			return true
		}
		if path == "/os-images" && r.Method == "GET" {
			out, err := s.Images06(r.Context())
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, out)
			}
			return true
		}
		if strings.HasPrefix(path, "/os-images/") && r.Method == "PATCH" {
			var in struct {
				Name    *string `json:"name"`
				Enabled *bool   `json:"enabled"`
			}
			if !commerceBody(w, r, "adminUpdateOsImage", &in) {
				return true
			}
			out, err := s.PatchImage06(r.Context(), actor, strings.TrimPrefix(path, "/os-images/"), in.Name, in.Enabled)
			if err != nil {
				a.commerceError(w, r, err)
			} else {
				jsonOut(w, 200, out)
			}
			return true
		}
	} else if r.Method == "GET" && !duplicate {
		if id == "" {
			q := r.URL.Query()
			currency := q.Get("currency")
			if currency != "" && !validCurrencyCode(currency) {
				fail(w, r, 400, "INVALID_CURRENCY", "Invalid currency")
				return true
			}
			out, err := s.Products06(r.Context(), admin, currency, q.Get("type"), q.Get("region"))
			if err != nil {
				a.commerceError(w, r, err)
			} else if admin {
				jsonOut(w, 200, out)
			} else {
				items := []api.Product{}
				for _, v := range out {
					items = append(items, publicProduct(v))
				}
				jsonOut(w, 200, items)
			}
		} else {
			out, err := s.Product06(r.Context(), admin, id)
			if err != nil {
				a.commerceError(w, r, err)
			} else if admin {
				jsonOut(w, 200, out)
			} else {
				jsonOut(w, 200, publicProduct(out))
			}
		}
		return true
	} else if admin && ((r.Method == "POST" && (id == "" || duplicate)) || (r.Method == "PATCH" && id != "" && !duplicate)) {
		var in api.AdminCreateProductInput
		operation := "adminCreateProduct"
		if r.Method == "PATCH" {
			operation = "adminUpdateProduct"
		}
		key := r.Header.Get("Idempotency-Key")
		if r.Method == "POST" && !keyPattern.MatchString(key) {
			fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "Provide an 8–128 character Idempotency-Key")
			return true
		}
		if duplicate {
			var empty struct{}
			if !commerceBody(w, r, "adminDuplicateProduct", &empty) {
				return true
			}
		} else if !commerceBody(w, r, operation, &in) {
			return true
		}
		out, err := s.SaveProduct06(r.Context(), actor, id, in, key, duplicate)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	return true
}

func validateProduct06(in api.AdminCreateProductInput, config CurrencyConfiguration, taxonomy TaxonomyConfiguration) error {
	raw, err := json.Marshal(in)
	if err != nil || api.ValidateInput("adminCreateProduct", raw) != nil {
		return ErrCommerceInput
	}
	if in.Name == nil || text06(in.Name) == "" || len(in.Options) > 100 || len(in.Prices) > 224 {
		return ErrCommerceInput
	}
	if (in.ControllerID == nil) != (in.RemoteProductID == nil) {
		return ErrCommerceInput
	}
	if in.ControllerID != nil && (len(*in.ControllerID) > 64 || len(*in.RemoteProductID) > 128 || *in.ControllerID == "" || *in.RemoteProductID == "") {
		return ErrCommerceInput
	}
	found := false
	for _, n := range taxonomy.Nodes {
		if n.ID == in.NodeID {
			for _, r := range taxonomy.Regions {
				if r.ID == n.RegionID {
					_, found = taxonomyNode(taxonomy, r.TypeID, r.ID, n.ID, in.Visibility == "public")
				}
			}
		}
	}
	if !found {
		return ErrCommerceInput
	}
	checkPrices := func(prices []api.PricePoint) error {
		seen := map[string]bool{}
		for _, p := range prices {
			c, ok := config.find(p.Currency)
			k := p.Currency + ":" + p.Cycle
			if !ok || !c.Enabled || int64(c.DecimalPlaces) != p.DecimalPlaces || seen[k] || p.RecurringMinor < 0 || p.RecurringMinor > MaxMinor || p.SetupMinor < 0 || p.SetupMinor > MaxMinor {
				return ErrCommerceInput
			}
			seen[k] = true
		}
		return nil
	}
	if err := checkPrices(in.Prices); err != nil {
		return err
	}
	seenIDs, seenKeys := map[string]bool{}, map[string]bool{}
	for _, o := range in.Options {
		if seenIDs[o.ID] || seenKeys[o.Key] {
			return ErrCommerceInput
		}
		seenIDs[o.ID], seenKeys[o.Key] = true, true
		if o.Kind == "quantity" && (o.Max < o.Min || o.Max > MaxMinor || len(o.Choices) != 0) {
			return ErrCommerceInput
		}
		if o.Kind == "select" && len(o.Choices) == 0 {
			return ErrCommerceInput
		}
		if o.Kind != "quantity" && (len(o.UnitPrices) > 0 || o.Min != 0 || o.Max != 0) {
			return ErrCommerceInput
		}
		if o.Kind == "os_image" && len(o.Choices) != 0 {
			return ErrCommerceInput
		}
		if err := checkPrices(o.UnitPrices); err != nil {
			return err
		}
		choices := map[string]bool{}
		for _, c := range o.Choices {
			if choices[c.ID] {
				return ErrCommerceInput
			}
			choices[c.ID] = true
			if err := checkPrices(c.Prices); err != nil {
				return err
			}
		}
	}
	images := map[string]bool{}
	for _, id := range in.OSImageIDs {
		if images[id] {
			return ErrCommerceInput
		}
		images[id] = true
	}
	return nil
}
