package billing

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Accept-Language is a preference, not a financial input. Unsupported language
// tags fall back to Simplified Chinese; currency never follows this header.
func requestLocale(header string) string {
	locale, best := "zh-CN", -1.0
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag, q := strings.ToLower(strings.TrimSpace(fields[0])), 1.0
		if len(fields) > 2 {
			continue
		}
		if len(fields) == 2 {
			parameter := strings.TrimSpace(fields[1])
			if !strings.HasPrefix(parameter, "q=") {
				continue
			}
			value, err := strconv.ParseFloat(strings.TrimPrefix(parameter, "q="), 64)
			if err != nil || !(value > 0 && value <= 1) {
				continue
			}
			q = value
		}
		selected := ""
		if tag == "en" || strings.HasPrefix(tag, "en-") {
			selected = "en"
		}
		if tag == "zh" || tag == "zh-cn" || tag == "zh-sg" || tag == "zh-hans" || strings.HasPrefix(tag, "zh-hans-") {
			selected = "zh-CN"
		}
		if selected != "" && q > best {
			locale, best = selected, q
		}
	}
	return locale
}

var englishErrors = map[string]string{
	"INVALID_TAXONOMY":               "Check the types, regions, nodes, ordering, parent relationships and reason.",
	"TAXONOMY_CONFLICT":              "The catalog categories changed. Reload before editing.",
	"TAXONOMY_IN_USE":                "Products still reference these categories. Unpublish or reassign them first.",
	"INVALID_CATALOG":                "Check the product name, specifications, prices and reason. Publishing requires a valid price in at least one enabled currency.",
	"CATALOG_CONFLICT":               "The product, currency settings or prices changed. Reload before editing.",
	"INVALID_ACCOUNT_QUERY":          "Invalid account filters or cursor. Start a new query.",
	"INVALID_ACCOUNT":                "Check the account name, email, role, password, revision and reason.",
	"ACCOUNT_EXISTS":                 "An account with this email already exists.",
	"ACCOUNT_CONFLICT":               "The account changed. Reload before editing.",
	"SELF_ACCOUNT_CHANGE":            "You cannot disable or change the role of your own account.",
	"LAST_ACTIVE_ADMIN":              "Keep at least one enabled administrator.",
	"ACCOUNT_MANAGEMENT_UNAVAILABLE": "Account management is unavailable.",
	"INVALID_AUDIT_QUERY":            "Invalid audit filters or cursor. Check the time range or start a new query.",
	"UPDATE_UNAVAILABLE":             "The official release could not be verified. Check host networking and private repository read access.",
	"UPDATE_BUSY":                    "A release check is in progress. Try again later.",
	"UPDATE_STALE":                   "Release or host state changed. Check again before selecting an update.",
	"UPDATE_INCOMPATIBLE":            "The target is incompatible or not newer. Complete host compatibility checks first.",
	"UPDATE_ACK_REQUIRED":            "Explicitly acknowledge that the host administrator must launch the protected update tool on the server.",
	"PLUGIN_UNAVAILABLE":             "The plugin is unavailable. Check installation, approval and compatible versions.",
	"INVALID_EMAIL_SETTINGS":         "Check SMTP settings and credentials. Changing the server requires replacing both credentials; clearing requires both credentials and disabling SMTP.",
	"SMTP_DISABLED":                  "Save and enable SMTP settings before sending a test.",
	"SMTP_KEY_UNAVAILABLE":           "The SMTP encryption key is unavailable. Contact the server administrator.",
	"SMTP_UNAVAILABLE":               "The SMTP server did not confirm acceptance. Check configuration and server status.",
	"SMTP_DELIVERY_UNCERTAIN":        "Delivery outcome is uncertain. Check SMTP server records before any manual retry.",
	"INVALID_CURRENCY_CONFIG":        "Check currency fields; the default must be enabled and existing codes cannot be removed.",
	"CURRENCY_CONFLICT":              "Currency settings changed. Reload before editing.",
	"CURRENCY_PRECISION_LOCKED":      "Decimal places cannot change after prices or billing records exist.",
	"CURRENCY_DISABLED":              "This currency is disabled for new orders.",
	"UNSUPPORTED_CURRENCY":           "This currency has not been configured.",
	"INVALID_PLAN_PRICES":            "Use a positive exact price within the currency precision; conversion and rounding are not supported.",
	"PRICE_CHANGED":                  "The quote changed. Review the current price and confirm again before ordering.",
	"INVALID_QUOTE":                  "Provide the complete quoted amount, decimal places, price revision and explicit currency.",
	"PRICE_CONFLICT":                 "Plan prices changed. Reload before editing.",
	"INTERNAL_ERROR":                 "Unable to complete the request. Please try again later.",
	"JSON_REQUIRED":                  "Use an application/json request.",
	"INVALID_BODY":                   "Invalid JSON object or unsupported fields.",
	"INVALID_HOST":                   "The request host does not match this site.",
	"NOT_READY":                      "The service is not ready.",
	"NOT_FOUND":                      "The requested resource was not found.",
	"METHOD_NOT_ALLOWED":             "This request method is not supported.",
	"CSRF_INVALID":                   "Your security token is invalid. Refresh the page and try again.",
	"AUTH_REQUIRED":                  "Please sign in first.",
	"FORBIDDEN":                      "You do not have permission to perform this action.",
	"INVALID_SETTINGS":               "Check the required fields, language, currency, contact details and HTTPS URLs.",
	"SETTINGS_CONFLICT":              "Another administrator changed these settings. Reload before editing.",
	"RATE_LIMITED":                   "Too many attempts. Please try again later.",
	"INVALID_CREDENTIALS":            "The email address or password is incorrect.",
	"VALIDATION_ERROR":               "Choose a plan and enter a hostname with 1–63 letters, digits or hyphens.",
	"IDEMPOTENCY_KEY_REQUIRED":       "Provide an idempotency key of 8–128 supported characters.",
	"IDEMPOTENCY_CONFLICT":           "This request key was already used for a different order.",
	"PLAN_UNAVAILABLE":               "This plan is not available in the selected currency.",
	"INVALID_CURRENCY":               "Currency codes must contain exactly three uppercase ASCII letters.",
	"INVOICE_NOT_PAYABLE":            "This invoice cannot be paid in its current state.",
	"PAYMENT_NOT_CONFIGURED":         "Payments are currently unavailable. Check the provider for any existing payment.",
	"PAYMENT_CURRENCY_UNSUPPORTED":   "The payment provider does not support this invoice currency.",
	"PAYMENT_PROVIDER_ERROR":         "The payment result is unconfirmed. Check the original provider before retrying.",
	"PAYMENT_PENDING":                "A payment attempt already exists. Check its provider result before paying again.",
	"PAYMENT_CALLBACK_INVALID":       "The payment notification could not be verified.",
	"PAYMENT_CONFLICT":               "The payment notification conflicts with a saved transaction.",
	"PAYMENT_STORAGE_UNAVAILABLE":    "Payment records are temporarily unavailable. Retry later.",
}

func (a *App) queryCurrency(w http.ResponseWriter, r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, r, 400, "INVALID_CURRENCY", "请检查币种参数")
		return "", false
	}
	config, err := a.Store.Currencies(r.Context(), false)
	if err != nil {
		a.internal(w, r, err)
		return "", false
	}
	code := config.DefaultCurrency
	if values, supplied := query["currency"]; supplied {
		if len(values) != 1 || !validCurrencyCode(values[0]) {
			fail(w, r, 400, "INVALID_CURRENCY", "币种代码必须是三个大写英文字母")
			return "", false
		}
		code = values[0]
	}
	if _, known := config.find(code); !known {
		a.currencyError(w, r, ErrCurrency)
		return "", false
	}
	return code, true
}

// Operator-authored product text and historical snapshots are never translated
// or rewritten. Only the known in-memory demonstration catalog is localized.
func localizeDemoPlans(plans []Plan) {
	examples := Catalog()
	names := []string{"Starter Cloud", "Standard Cloud", "Performance Cloud"}
	descriptions := []string{"For personal sites and development", "For growing applications", "More compute and storage capacity"}
	for n := range plans {
		for j, example := range examples {
			if plans[n].ID == example.ID {
				if plans[n].Name == example.Name {
					plans[n].Name = names[j]
				}
				if plans[n].Description == example.Description {
					plans[n].Description = descriptions[j]
				}
				// Regions now use operator-authored taxonomy labels in every locale.
			}
		}
	}
}
