package billing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) accountCursor(actor User, q accountQuery, id string) string {
	h := hmac.New(sha256.New, []byte(a.Config.Secret))
	binding, _ := json.Marshal(struct {
		Actor, Role           string
		Revision              int64
		Q, FilterRole, Status string
		Limit                 int
		Before                string
	}{actor.ID, actor.Role, actor.Revision, q.Q, q.Role, q.Status, q.Limit, id})
	h.Write([]byte("aster:admin-accounts:1\x00"))
	h.Write(binding)
	return id + "." + hex.EncodeToString(h.Sum(nil))
}
func (a *App) parseAccountQuery(r *http.Request, actor User) (accountQuery, error) {
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return accountQuery{}, ErrAccountQuery
	}
	q := accountQuery{Limit: 50}
	for k, values := range v {
		if len(values) != 1 {
			return q, ErrAccountQuery
		}
		s := values[0]
		switch k {
		case "q":
			q.Q = s
		case "role":
			q.Role = s
		case "status":
			q.Status = s
		case "limit":
			q.Limit, err = strconv.Atoi(s)
			if err != nil || q.Limit < 1 || q.Limit > 100 {
				return q, ErrAccountQuery
			}
		case "cursor":
			if len(s) != 97 || s[32] != '.' {
				return q, ErrAccountQuery
			}
		default:
			return q, ErrAccountQuery
		}
	}
	q, err = normalizeAccountQuery(q)
	if err != nil {
		return q, err
	}
	if cursor, ok := v["cursor"]; ok {
		q.Before = cursor[0][:32]
		expected := a.accountCursor(actor, q, q.Before)
		if !auditOwner.MatchString(q.Before) || !hmac.Equal([]byte(cursor[0]), []byte(expected)) {
			return q, ErrAccountQuery
		}
	}
	return q, nil
}
func (a *App) accountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrAccountSession):
		fail(w, r, 401, "AUTH_REQUIRED", "登录已失效，请重新登录")
	case errors.Is(err, ErrAccountInput):
		fail(w, r, 400, "INVALID_ACCOUNT", "账户资料无效")
	case errors.Is(err, ErrAccountQuery):
		fail(w, r, 400, "INVALID_ACCOUNT_QUERY", "账户查询参数无效")
	case errors.Is(err, ErrAccountExists):
		fail(w, r, 409, "ACCOUNT_EXISTS", "邮箱已存在")
	case errors.Is(err, ErrAccountConflict), errors.Is(err, ErrConflict):
		fail(w, r, 409, "ACCOUNT_CONFLICT", "账户已更改，请刷新后重试")
	case errors.Is(err, ErrSelfAccountChange):
		fail(w, r, 409, "SELF_ACCOUNT_CHANGE", "不能降权或停用当前账户")
	case errors.Is(err, ErrLastActiveAdmin):
		fail(w, r, 409, "LAST_ACTIVE_ADMIN", "至少保留一位已启用管理员")
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "记录不存在")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "没有访问权限")
	default:
		a.internal(w, r, err)
	}
}
func (a *App) adminCustomerAccountsHTTP(w http.ResponseWriter, r *http.Request, actor User, path string) {
	if actor.Role != "admin" || actor.Disabled {
		fail(w, r, 403, "FORBIDDEN", "没有访问权限")
		return
	}
	store, ok := a.Store.(adminCustomerStore)
	if !ok {
		fail(w, r, 503, "ACCOUNT_MANAGEMENT_UNAVAILABLE", "账户管理暂不可用")
		return
	}
	suffix := strings.TrimPrefix(path, "/admin/customer-accounts")
	if suffix == "" && r.Method == http.MethodGet {
		q, err := a.parseAccountQuery(r, actor)
		if err != nil {
			a.accountError(w, r, err)
			return
		}
		page, err := store.AdminCustomerAccounts(r.Context(), actor.ID, q)
		if err != nil {
			a.accountError(w, r, err)
			return
		}
		items := make([]customerAccount, 0, len(page.Items))
		for _, u := range page.Items {
			items = append(items, accountDTO(u))
		}
		response := struct {
			Items      []customerAccount `json:"items"`
			HasMore    bool              `json:"has_more"`
			NextCursor string            `json:"next_cursor,omitempty"`
			Limit      int               `json:"limit"`
		}{Items: items, HasMore: page.HasMore, Limit: q.Limit}
		if page.HasMore && len(items) > 0 {
			response.NextCursor = a.accountCursor(actor, q, items[len(items)-1].ID)
		}
		jsonOut(w, 200, response)
		return
	}
	parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if suffix != "" && (len(parts) > 2 || !strings.HasPrefix(suffix, "/") || !auditOwner.MatchString(parts[0]) || len(parts) == 2 && parts[1] != "password" && parts[1] != "revoke-sessions") {
		fail(w, r, 404, "NOT_FOUND", "记录不存在")
		return
	}
	if suffix != "" && len(parts) == 1 && r.Method == http.MethodGet {
		if r.URL.RawQuery != "" {
			fail(w, r, 400, "INVALID_ACCOUNT_QUERY", "账户查询参数无效")
			return
		}
		u, err := store.AdminCustomerAccount(r.Context(), actor.ID, parts[0])
		if err != nil {
			a.accountError(w, r, err)
			return
		}
		jsonOut(w, 200, accountDTO(u))
		return
	}
	if r.Method != http.MethodPost {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "请求方法不支持")
		return
	}
	if r.URL.RawQuery != "" {
		fail(w, r, 400, "INVALID_ACCOUNT_QUERY", "账户查询参数无效")
		return
	}
	var input accountWrite
	// financeBody already enforces JSON type, exact required keys, no duplicates,
	// no nulls and no unknown fields. Keep passwords out of generic response DTOs.
	if suffix == "" {
		var body struct {
			Name     string `json:"name"`
			Email    string `json:"email"`
			Role     string `json:"role"`
			Password string `json:"password"`
			Reason   string `json:"reason"`
		}
		if !financeBody(w, r, &body) {
			return
		}
		input = accountWrite{Operation: "create", Name: body.Name, Email: body.Email, Role: body.Role, Password: body.Password, Reason: body.Reason}
	} else if len(parts) == 1 {
		var body struct {
			Name     string `json:"name"`
			Role     string `json:"role"`
			Disabled bool   `json:"disabled"`
			Revision int64  `json:"revision"`
			Reason   string `json:"reason"`
		}
		if !financeBody(w, r, &body) {
			return
		}
		input = accountWrite{Operation: "update", ID: parts[0], Name: body.Name, Role: body.Role, Disabled: body.Disabled, Revision: body.Revision, Reason: body.Reason}
	} else if parts[1] == "password" {
		var body struct {
			Password string `json:"password"`
			Revision int64  `json:"revision"`
			Reason   string `json:"reason"`
		}
		if !financeBody(w, r, &body) {
			return
		}
		input = accountWrite{Operation: "password", ID: parts[0], Password: body.Password, Revision: body.Revision, Reason: body.Reason}
	} else {
		var body struct {
			Revision int64  `json:"revision"`
			Reason   string `json:"reason"`
		}
		if !financeBody(w, r, &body) {
			return
		}
		input = accountWrite{Operation: "revoke", ID: parts[0], Revision: body.Revision, Reason: body.Reason}
	}
	ctx := withAccountWriteSession(r.Context(), actor.ID, digest(a.token(r)))
	u, err := store.WriteCustomerAccount(ctx, actor.ID, input)
	if err != nil {
		a.accountError(w, r, err)
		return
	}
	status := 200
	if suffix == "" {
		status = 201
	}
	jsonOut(w, status, accountDTO(u))
}
