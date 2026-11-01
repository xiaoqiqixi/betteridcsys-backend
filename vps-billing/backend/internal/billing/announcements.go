package billing

import (
	"aster/billing/internal/financeapi"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (a *App) announcementsHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	admin := path == "/admin/announcements" || strings.HasPrefix(path, "/admin/announcements/")
	if !admin && path != "/announcements" {
		return false
	}
	if r.Method == "GET" && (path == "/announcements" || path == "/admin/announcements") {
		a.listAnnouncements(w, r, admin)
		return true
	}
	if admin && r.Method == "POST" && path == "/admin/announcements" {
		a.saveAnnouncement(w, r, u)
		return true
	}
	if admin && r.Method == "DELETE" && strings.HasPrefix(path, "/admin/announcements/") {
		id := strings.TrimPrefix(path, "/admin/announcements/")
		v, err := a.accountTransaction(r, u.ID, "delete-announcement", nil, false, func(tx *sql.Tx) (any, error) {
			result, err := tx.ExecContext(r.Context(), "DELETE FROM announcements WHERE id=?", id)
			if err != nil {
				return nil, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if n != 1 {
				return nil, ErrNotFound
			}
			return okData(), insertAccountAudit(r.Context(), tx, u.ID, u.ID, "announcement.deleted", id)
		})
		a.accountResult(w, r, v, err)
		return true
	}
	fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	return true
}
func scanAnnouncement(row interface{ Scan(...any) error }) (financeapi.AdminSaveAnnouncementResponse, error) {
	var v financeapi.AdminSaveAnnouncementResponse
	var title, body []byte
	var at time.Time
	err := row.Scan(&v.ID, &title, &body, &v.Pinned, &v.Published, &at)
	if err == nil {
		err = json.Unmarshal(title, &v.Title)
	}
	if err == nil {
		err = json.Unmarshal(body, &v.Body)
	}
	v.PublishedAt = stamp(at)
	return v, err
}
func (a *App) listAnnouncements(w http.ResponseWriter, r *http.Request, admin bool) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	query := "SELECT id,title,body,pinned,published,published_at FROM announcements"
	args := []any{}
	limit := 0
	if !admin {
		if _, err = parseAccountQuery(r, "cursor", "limit"); err != nil {
			a.accountResult(w, r, nil, err)
			return
		}
		limit, err = queryLimit(r)
		if err != nil {
			a.accountResult(w, r, nil, err)
			return
		}
		cursor, err := a.readAccountCursor(r, "announcements")
		if err != nil {
			a.accountResult(w, r, nil, err)
			return
		}
		query += " WHERE published=TRUE AND published_at<=UTC_TIMESTAMP(6)"
		if !cursor.At.IsZero() {
			query += " AND (pinned<? OR (pinned=? AND (published_at<? OR (published_at=? AND id<?))))"
			args = append(args, cursor.Pinned, cursor.Pinned, cursor.At, cursor.At, cursor.ID)
		}
	}
	query += " ORDER BY pinned DESC,published_at DESC,id DESC"
	if !admin {
		query += " LIMIT ?"
		args = append(args, limit+1)
	}
	rows, err := m.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	items := []any{}
	var next *string
	var last financeapi.AdminSaveAnnouncementResponse
	for rows.Next() {
		v, err := scanAnnouncement(rows)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if !admin && len(items) == limit {
			at, _ := time.Parse(time.RFC3339Nano, last.PublishedAt)
			raw, _ := json.Marshal(accountCursor{At: at, ID: last.ID, Pinned: last.Pinned})
			value := base64.RawURLEncoding.EncodeToString(raw)
			cursor := value + "." + a.cursorSignature(r, "announcements", value)
			next = &cursor
			break
		}
		if admin {
			items = append(items, v)
		} else {
			items = append(items, financeapi.Announcement{ID: v.ID, Title: v.Title, Body: v.Body, Pinned: v.Pinned, PublishedAt: v.PublishedAt})
		}
		last = v
	}
	if admin {
		a.accountResult(w, r, items, rows.Err())
	} else {
		a.accountResult(w, r, map[string]any{"items": items, "next_cursor": next}, rows.Err())
	}
}
func (a *App) saveAnnouncement(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.AdminSaveAnnouncementInput
	if !accountBody(w, r, "adminSaveAnnouncement", &in) {
		return
	}
	v, err := a.accountTransaction(r, u.ID, "save-announcement", in, false, func(tx *sql.Tx) (any, error) {
		at := time.Now().UTC().Truncate(time.Microsecond)
		id := in.ID
		if id != "" {
			var published bool
			var previous time.Time
			if err := tx.QueryRowContext(r.Context(), "SELECT published,published_at FROM announcements WHERE id=? FOR UPDATE", id).Scan(&published, &previous); err != nil {
				return nil, err
			}
			if published || !in.Published {
				at = previous
			}
		} else {
			id = ID()
		}
		title, _ := json.Marshal(in.Title)
		body, _ := json.Marshal(in.Body)
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO announcements(id,title,body,pinned,published,published_at) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE title=?,body=?,pinned=?,published=?,published_at=?", id, title, body, in.Pinned, in.Published, at, title, body, in.Pinned, in.Published, at); err != nil {
			return nil, err
		}
		out := financeapi.AdminSaveAnnouncementResponse{ID: id, Title: in.Title, Body: in.Body, Pinned: in.Pinned, Published: in.Published, PublishedAt: stamp(at)}
		return out, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "announcement.saved", id)
	})
	a.accountResult(w, r, v, err)
}
