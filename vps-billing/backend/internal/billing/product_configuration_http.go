package billing

import (
	"aster/billing/internal/controllers"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"
)

func productConfigurationPath(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/admin/catalog/")
	if !ok || !strings.HasSuffix(id, "/product-configuration") {
		return "", false
	}
	id = strings.TrimSuffix(id, "/product-configuration")
	return id, validCatalogID(id)
}

func provisioningOrderPath(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/admin/orders/")
	if !ok || !strings.HasSuffix(id, "/provisioning-jobs") {
		return "", false
	}
	id = strings.TrimSuffix(id, "/provisioning-jobs")
	return id, validCancellationID(id)
}

func provisioningJobPath(path string) (string, bool) {
	id, ok := strings.CutPrefix(path, "/admin/provisioning-jobs/")
	return id, ok && validCancellationID(id)
}

// Product configuration has one intentional null: spec.resources for disabled
// or preset modules. Keep the financial parser untouched: it deliberately
// forbids all nulls and has a different field grammar.
func productConfigurationBody(w http.ResponseWriter, r *http.Request, target *ProductConfigurationInput) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "请使用 JSON 请求")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	valid := err == nil && utf8.Valid(raw)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var tokens func(int, string) bool
	tokens = func(depth int, path string) bool {
		if depth > 6 {
			return false
		}
		t, err := d.Token()
		if err != nil {
			return false
		}
		if t == nil {
			return path == "$.spec.resources"
		}
		if t == json.Delim('{') {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				k, ok := key.(string)
				if err != nil || !ok || seen[k] || !tokens(depth+1, path+"."+k) {
					return false
				}
				seen[k] = true
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		}
		_, delim := t.(json.Delim)
		return !delim // This fixed module schema has no arrays.
	}
	if valid {
		valid = tokens(0, "$")
		var extra any
		valid = valid && d.Decode(&extra) == io.EOF
	}
	var shape func(json.RawMessage, reflect.Type) bool
	shape = func(raw json.RawMessage, t reflect.Type) bool {
		if t == reflect.TypeOf((*controllers.Resources)(nil)) && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
		if t.Kind() == reflect.Pointer {
			return shape(raw, t.Elem())
		}
		if t.Kind() == reflect.Struct {
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil || len(fields) != t.NumField() {
				return false
			}
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				value, ok := fields[field.Tag.Get("json")]
				if !ok || !shape(value, field.Type) {
					return false
				}
			}
		}
		return true
	}
	if valid {
		valid = shape(raw, reflect.TypeOf(target))
	}
	if valid {
		d = json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		valid = d.Decode(target) == nil
	}
	if !valid {
		fail(w, r, 400, "INVALID_PRODUCT_CONFIGURATION", "产品配置格式不正确，请核对完整参数后重试")
	}
	return valid
}

func (a *App) productConfigurationError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrProductConfiguration), errors.Is(err, controllers.ErrConfiguration):
		fail(w, r, 400, "INVALID_PRODUCT_CONFIGURATION", "请检查主控标识、资源参数及操作原因")
	case errors.Is(err, ErrProductConfigurationConflict):
		fail(w, r, 409, "PRODUCT_CONFIGURATION_CONFLICT", "产品配置已被修改，请重新读取后编辑")
	case errors.Is(err, ErrProvisioningState):
		fail(w, r, 409, "PROVISIONING_STATE_CONFLICT", "当前订单或任务状态不允许下发，请先核对付款与执行结果")
	case errors.Is(err, ErrProvisioningUnavailable):
		fail(w, r, 409, "PROVISIONING_UNAVAILABLE", "此订单未保存可下发的产品配置，不能使用当前商品配置补开")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "需要有效的管理员权限")
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "未找到对应记录")
	default:
		a.internal(w, r, err)
	}
}

func (a *App) productConfigurationHTTP(w http.ResponseWriter, r *http.Request, actor User, planID string) {
	store, ok := a.Store.(ProductConfigurationStore)
	if !ok {
		fail(w, r, 503, "PRODUCT_CONFIGURATION_UNAVAILABLE", "产品配置暂不可用")
		return
	}
	var out ProductConfiguration
	var err error
	switch r.Method {
	case http.MethodGet:
		out, err = store.ProductConfiguration(r.Context(), actor, planID)
	case http.MethodPost:
		var input ProductConfigurationInput
		if !productConfigurationBody(w, r, &input) {
			return
		}
		out, err = store.UpdateProductConfiguration(r.Context(), actor, planID, input)
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	if err != nil {
		a.productConfigurationError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
}

func (a *App) provisioningHTTP(w http.ResponseWriter, r *http.Request, actor User, id string, create bool) {
	store, ok := a.Store.(ProvisioningJobStore)
	if !ok {
		fail(w, r, 503, "PROVISIONING_UNAVAILABLE", "开通任务暂不可用")
		return
	}
	if !create && r.Method == http.MethodGet {
		out, err := store.ProvisioningJob(r.Context(), actor, id)
		if err != nil {
			a.productConfigurationError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if !create || r.Method != http.MethodPost {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input struct {
		Reason string `json:"reason"`
	}
	if !financeBody(w, r, &input) {
		return
	}
	out, replay, err := store.CreateProvisioningJob(r.Context(), actor, id, input.Reason)
	if err != nil {
		a.productConfigurationError(w, r, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replay", "true")
	}
	jsonOut(w, status, out)
}
