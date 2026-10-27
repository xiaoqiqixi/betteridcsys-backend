package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"
)

// SMTP mutations are flat strict objects: duplicate fields and null values are
// rejected to avoid ambiguous enable/clear/replacement credential semantics.
func emailBody(w http.ResponseWriter, r *http.Request, v any, required ...string) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "请使用 JSON 请求")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<10))
	if err != nil || !utf8.Valid(raw) {
		fail(w, r, 400, "INVALID_BODY", "请求格式不正确")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, e := d.Token()
	valid := e == nil && first == json.Delim('{')
	seen := map[string]bool{}
	if valid {
		for d.More() {
			token, e := d.Token()
			key, ok := token.(string)
			if e != nil || !ok || seen[key] || !emailJSONField(key) {
				valid = false
				break
			}
			seen[key] = true
			var value json.RawMessage
			if d.Decode(&value) != nil || len(value) == 0 || bytes.Equal(value, []byte("null")) || value[0] == '{' || value[0] == '[' {
				valid = false
				break
			}
		}
	}
	if valid {
		end, e := d.Token()
		valid = e == nil && end == json.Delim('}')
		var extra any
		if d.Decode(&extra) != io.EOF {
			valid = false
		}
	}
	for _, key := range required {
		if !seen[key] {
			valid = false
		}
	}
	if valid {
		decode := json.NewDecoder(bytes.NewReader(raw))
		decode.DisallowUnknownFields()
		valid = decode.Decode(v) == nil
	}
	if !valid {
		fail(w, r, 400, "INVALID_BODY", "请求格式不正确或包含重复、不支持的字段")
		return false
	}
	return true
}

func emailJSONField(key string) bool {
	switch key {
	case "revision", "enabled", "host", "port", "tls_mode", "from_address", "from_name", "username", "password", "clear_username", "clear_password", "to":
		return true
	default:
		return false
	}
}

func (a *App) emailSettingsHTTP(w http.ResponseWriter, r *http.Request, actor User, test bool) {
	if test {
		if r.Method != "POST" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
			return
		}
		var input struct {
			Revision int64  `json:"revision"`
			To       string `json:"to"`
		}
		if !emailBody(w, r, &input, "revision", "to") {
			return
		}
		if err := a.SMTP.Test(r.Context(), a.Integrations, actor, input.Revision, input.To); err != nil {
			a.emailError(w, r, err)
			return
		}
		jsonOut(w, 200, map[string]string{"status": "accepted"})
		return
	}
	if r.Method == "GET" {
		settings, err := a.SMTP.Settings(r.Context())
		if err != nil {
			a.emailError(w, r, err)
			return
		}
		jsonOut(w, 200, settings)
		return
	}
	var input EmailSettingsUpdate
	if !emailBody(w, r, &input, "revision", "enabled", "host", "port", "tls_mode", "from_address", "from_name") {
		return
	}
	settings, err := a.SMTP.Update(r.Context(), actor, input)
	if err != nil {
		a.emailError(w, r, err)
		return
	}
	jsonOut(w, 200, settings)
}
func (a *App) emailError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrEmailSettings):
		fail(w, r, 400, "INVALID_EMAIL_SETTINGS", "请检查邮件设置和凭据；更换服务器需同时更换两项凭据，清除凭据需同时清除并关闭")
	case errors.Is(err, ErrSettingsConflict):
		fail(w, r, 409, "SETTINGS_CONFLICT", "设置已被其他管理员修改，请重新读取后再编辑")
	case errors.Is(err, ErrSMTPKey):
		fail(w, r, 503, "SMTP_KEY_UNAVAILABLE", "邮件加密密钥不可用，请联系服务器管理员")
	case errors.Is(err, ErrSMTPDisabled):
		fail(w, r, 409, "SMTP_DISABLED", "请先保存并启用 SMTP 配置")
	case errors.Is(err, ErrSMTPRate):
		w.Header().Set("Retry-After", "30")
		fail(w, r, 429, "RATE_LIMITED", "测试请求过于频繁，请至少等待 30 秒")
	case errors.Is(err, ErrSMTPUncertain):
		fail(w, r, 502, "SMTP_DELIVERY_UNCERTAIN", "邮件发送结果不确定，请先检查邮件服务器记录，不要直接重试")
	case errors.Is(err, ErrSMTPUnavailable):
		fail(w, r, 502, "SMTP_UNAVAILABLE", "SMTP 服务器未确认接受测试邮件，请检查配置或服务器状态")
	default:
		a.internal(w, r, err)
	}
}
