package billing

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrAuditQuery = errors.New("invalid audit query")
var auditAction = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
var auditOwner = regexp.MustCompile(`^[0-9a-f]{32}$`)

type AuditQuery struct {
	Limit                         int
	Action, ActorEmail, OwnerID   string
	From, Until, Snapshot, Before time.Time
	BeforeID                      string
}

type AuditPage struct {
	Items   []Audit
	HasMore bool
}

func auditText(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf, unicode.Cs) {
			return false
		}
	}
	return true
}

func normalizeAuditQuery(q AuditQuery, uid string) (AuditQuery, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || (q.Action != "" && !auditAction.MatchString(q.Action)) || !auditText(q.ActorEmail, 254) || (q.OwnerID != "" && !auditOwner.MatchString(q.OwnerID)) || (uid != "" && q.OwnerID != "" && q.OwnerID != uid) {
		return q, ErrAuditQuery
	}
	if q.Snapshot.IsZero() {
		q.Snapshot = time.Now().UTC().Truncate(time.Microsecond)
	}
	for _, t := range []*time.Time{&q.From, &q.Until, &q.Snapshot, &q.Before} {
		if t.IsZero() {
			continue
		}
		*t = t.UTC()
		if t.Year() < 1000 || t.Year() > 9999 || t.Nanosecond()%1000 != 0 {
			return q, ErrAuditQuery
		}
	}
	if (!q.From.IsZero() && !q.Until.IsZero() && !q.From.Before(q.Until)) || (q.Before.IsZero() != (q.BeforeID == "")) || !auditText(q.BeforeID, 32) || (!q.Before.IsZero() && q.Before.After(q.Snapshot)) {
		return q, ErrAuditQuery
	}
	return q, nil
}

func auditMatches(event Audit, uid string, q AuditQuery) bool {
	t := event.CreatedAt.UTC().Truncate(time.Microsecond)
	return (uid == "" || event.UserID == uid) && (q.OwnerID == "" || event.UserID == q.OwnerID) &&
		(q.Action == "" || event.Action == q.Action) && (q.ActorEmail == "" || event.ActorEmail == q.ActorEmail) &&
		(q.From.IsZero() || !t.Before(q.From)) && (q.Until.IsZero() || t.Before(q.Until)) && !t.After(q.Snapshot) &&
		(q.Before.IsZero() || t.Before(q.Before) || (t.Equal(q.Before) && event.ID < q.BeforeID))
}

func (m *Memory) AuditPage(ctx context.Context, uid string, query AuditQuery) (AuditPage, error) {
	q, err := normalizeAuditQuery(query, uid)
	if err != nil {
		return AuditPage{}, err
	}
	if err = ctx.Err(); err != nil {
		return AuditPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []Audit{}
	for _, event := range m.audits {
		if auditMatches(event, uid, q) {
			event.CreatedAt = event.CreatedAt.UTC().Truncate(time.Microsecond)
			items = append(items, event)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	page := AuditPage{Items: items, HasMore: len(items) > q.Limit}
	if page.HasMore {
		page.Items = items[:q.Limit]
	}
	return page, nil
}

func (m *MySQL) AuditPage(ctx context.Context, uid string, query AuditQuery) (AuditPage, error) {
	q, err := normalizeAuditQuery(query, uid)
	if err != nil {
		return AuditPage{}, err
	}
	where := []string{"created_at<=?"}
	args := []any{q.Snapshot}
	if uid != "" {
		where, args = append(where, "user_id=?"), append(args, uid)
	}
	if q.OwnerID != "" {
		where, args = append(where, "user_id=?"), append(args, q.OwnerID)
	}
	if q.Action != "" {
		where, args = append(where, "action=? AND BINARY action=BINARY ?"), append(args, q.Action, q.Action)
	}
	if q.ActorEmail != "" {
		// The first predicate uses the normal index; the second preserves exact
		// case/bytes, including historical system actors such as payment:ID.
		where = append(where, "actor_email=? AND BINARY actor_email=BINARY ?")
		args = append(args, q.ActorEmail, q.ActorEmail)
	}
	if !q.From.IsZero() {
		where, args = append(where, "created_at>=?"), append(args, q.From)
	}
	if !q.Until.IsZero() {
		where, args = append(where, "created_at<?"), append(args, q.Until)
	}
	if !q.Before.IsZero() {
		where = append(where, "(created_at<? OR (created_at=? AND id<?))")
		args = append(args, q.Before, q.Before, q.BeforeID)
	}
	args = append(args, q.Limit+1)
	rows, err := m.DB.QueryContext(ctx, "SELECT id,user_id,action,target,created_at,actor_email,detail FROM audit_events WHERE "+strings.Join(where, " AND ")+" ORDER BY created_at DESC,id DESC LIMIT ?", args...)
	if err != nil {
		return AuditPage{}, err
	}
	defer rows.Close()
	items := []Audit{}
	for rows.Next() {
		var event Audit
		if err := rows.Scan(&event.ID, &event.UserID, &event.Action, &event.Target, &event.CreatedAt, &event.ActorEmail, &event.Detail); err != nil {
			return AuditPage{}, err
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, err
	}
	page := AuditPage{Items: items, HasMore: len(items) > q.Limit}
	if page.HasMore {
		page.Items = items[:q.Limit]
	}
	return page, nil
}

type auditCursor struct {
	Snapshot time.Time `json:"snapshot"`
	Before   time.Time `json:"before"`
	ID       string    `json:"id"`
}

func (a *App) auditCursorMAC(payload []byte, uid string, q AuditQuery) []byte {
	h := hmac.New(sha256.New, []byte(a.Config.Secret))
	_, _ = h.Write([]byte("aster:audit-page:1\x00"))
	// The tuple prevents separator ambiguity and binds cursors to the complete
	// filters, page size and server-authorized owner scope, not a client role.
	filters, _ := json.Marshal([]any{uid, q.Limit, q.Action, q.ActorEmail, q.OwnerID, q.From, q.Until})
	_, _ = h.Write(filters)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return h.Sum(nil)
}

func (a *App) encodeAuditCursor(uid string, q AuditQuery, last Audit) string {
	payload, _ := json.Marshal(auditCursor{q.Snapshot, last.CreatedAt.UTC().Truncate(time.Microsecond), last.ID})
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(a.auditCursorMAC(payload, uid, q))
}

func (a *App) decodeAuditCursor(value, uid string, q *AuditQuery) error {
	if len(value) > 1024 {
		return ErrAuditQuery
	}
	encoded, signature, ok := strings.Cut(value, ".")
	if !ok {
		return ErrAuditQuery
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	mac, macErr := hex.DecodeString(signature)
	if err != nil || macErr != nil || len(mac) != sha256.Size || !hmac.Equal(mac, a.auditCursorMAC(payload, uid, *q)) {
		return ErrAuditQuery
	}
	var cursor auditCursor
	d := json.NewDecoder(bytes.NewReader(payload))
	d.DisallowUnknownFields()
	if d.Decode(&cursor) != nil || d.Decode(&struct{}{}) != io.EOF || cursor.Snapshot.IsZero() || cursor.Before.IsZero() || cursor.ID == "" {
		return ErrAuditQuery
	}
	q.Snapshot, q.Before, q.BeforeID = cursor.Snapshot, cursor.Before, cursor.ID
	_, err = normalizeAuditQuery(*q, uid)
	return err
}

func readAuditQuery(values url.Values, admin bool) (AuditQuery, error) {
	allowed := map[string]bool{"limit": true, "cursor": true, "action": true, "actor_email": true, "owner_id": admin, "from": true, "until": true}
	for key, items := range values {
		if !allowed[key] || len(items) != 1 || items[0] == "" {
			return AuditQuery{}, ErrAuditQuery
		}
	}
	q := AuditQuery{Limit: 50, Action: values.Get("action"), ActorEmail: values.Get("actor_email"), OwnerID: values.Get("owner_id")}
	if values.Has("limit") {
		limit, err := strconv.Atoi(values.Get("limit"))
		if err != nil || limit < 1 || limit > 100 || strconv.Itoa(limit) != values.Get("limit") {
			return q, ErrAuditQuery
		}
		q.Limit = limit
	}
	for name, target := range map[string]*time.Time{"from": &q.From, "until": &q.Until} {
		if values.Has(name) {
			t, err := time.Parse(time.RFC3339Nano, values.Get(name))
			if err != nil {
				return q, ErrAuditQuery
			}
			*target = t
		}
	}
	return normalizeAuditQuery(q, "")
}

func (a *App) auditEventsHTTP(w http.ResponseWriter, r *http.Request, actor User, uid string) {
	if len(r.URL.RawQuery) > 4096 {
		fail(w, r, 400, "INVALID_AUDIT_QUERY", "日志筛选条件无效，请检查时间范围或重新查询")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(w, r, 400, "INVALID_AUDIT_QUERY", "日志筛选条件无效，请检查时间范围或重新查询")
		return
	}
	admin := strings.HasPrefix(r.URL.Path, "/api/v1/admin/")
	q, err := readAuditQuery(values, admin && actor.Role == "admin")
	if err == nil && values.Has("cursor") {
		err = a.decodeAuditCursor(values.Get("cursor"), uid, &q)
	}
	if err != nil {
		fail(w, r, 400, "INVALID_AUDIT_QUERY", "日志筛选条件无效，请检查时间范围或重新查询")
		return
	}
	page, err := a.Store.AuditPage(r.Context(), uid, q)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	type event struct {
		Audit
		OwnerID string `json:"owner_id,omitempty"`
	}
	items := make([]event, len(page.Items))
	for i, row := range page.Items {
		items[i].Audit = row
		if admin {
			items[i].OwnerID = row.UserID
		}
	}
	next := ""
	if page.HasMore && len(page.Items) > 0 {
		next = a.encodeAuditCursor(uid, q, page.Items[len(page.Items)-1])
	}
	jsonOut(w, 200, struct {
		Items      []event `json:"items"`
		HasMore    bool    `json:"has_more"`
		NextCursor string  `json:"next_cursor,omitempty"`
		Limit      int     `json:"limit"`
	}{items, page.HasMore, next, q.Limit})
}
