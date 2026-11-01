package billing

import (
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type accountReader interface {
	accountQueryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func parseAccountQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, problem(400, "INVALID_QUERY", "Invalid query")
	}
	for key, values := range q {
		if len(values) != 1 || !slices.Contains(allowed, key) {
			return nil, problem(400, "INVALID_QUERY", "Invalid query parameter")
		}
	}
	return q, nil
}

type supportTicket struct {
	financeapi.Ticket
	ClientID     string  `json:"client_id"`
	ClientEmail  string  `json:"client_email"`
	AssigneeID   *string `json:"assignee_id"`
	AssigneeName *string `json:"assignee_name"`
}

const ticketSelect = `SELECT t.id,t.number,t.subject,t.department_id,t.status,t.priority,t.service_id,t.last_reply_at,t.created_at,t.user_id,u.email,t.assignee_id,s.name FROM tickets t JOIN users u ON u.id=t.user_id LEFT JOIN users s ON s.id=t.assignee_id`

func scanTicket(row interface{ Scan(...any) error }) (supportTicket, error) {
	var v supportTicket
	var last, created time.Time
	err := row.Scan(&v.ID, &v.Number, &v.Subject, &v.DepartmentID, &v.Status, &v.Priority, &v.ServiceID, &last, &created, &v.ClientID, &v.ClientEmail, &v.AssigneeID, &v.AssigneeName)
	v.LastReplyAt, v.CreatedAt = stamp(last), stamp(created)
	v.Messages = []financeapi.TicketMessage{}
	return v, err
}
func ticketOutput(v supportTicket, admin bool) any {
	if admin {
		return v
	}
	return v.Ticket
}
func ticketSummary(v supportTicket, admin bool) any {
	raw, _ := json.Marshal(ticketOutput(v, admin))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	delete(out, "messages")
	return out
}
func readTicket(ctx context.Context, q accountReader, id, owner string, admin bool) (supportTicket, error) {
	where := " WHERE t.id=?"
	args := []any{id}
	if !admin {
		where += " AND t.user_id=?"
		args = append(args, owner)
	}
	v, err := scanTicket(q.QueryRowContext(ctx, ticketSelect+where, args...))
	if err != nil {
		return v, err
	}
	filter := ""
	if !admin {
		filter = " AND internal=FALSE"
	}
	rows, err := q.QueryContext(ctx, "SELECT id,author_type,author_name,body,internal,created_at FROM ticket_messages WHERE ticket_id=?"+filter+" ORDER BY created_at,id", id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var message financeapi.TicketMessage
		var at time.Time
		if err = rows.Scan(&message.ID, &message.AuthorType, &message.AuthorName, &message.Body, &message.Internal, &at); err != nil {
			return v, err
		}
		message.CreatedAt = stamp(at)
		v.Messages = append(v.Messages, message)
	}
	return v, rows.Err()
}
func lockTicket(ctx context.Context, tx *sql.Tx, id, owner string, admin bool) error {
	query := "SELECT id FROM tickets WHERE id=?"
	args := []any{id}
	if !admin {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	var found string
	return tx.QueryRowContext(ctx, query+" FOR UPDATE", args...).Scan(&found)
}
func (a *App) supportHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	if path == "/support/departments" || path == "/admin/departments" {
		a.departmentsHTTP(w, r, strings.HasPrefix(path, "/admin/"))
		return true
	}
	admin := strings.HasPrefix(path, "/admin/tickets")
	base := "/tickets"
	if admin {
		base = "/admin/tickets"
	}
	if path != base && !strings.HasPrefix(path, base+"/") {
		return false
	}
	if path == base {
		if r.Method == "GET" {
			a.listTickets(w, r, u, admin)
		} else if r.Method == "POST" && !admin {
			a.openTicket(w, r, u)
		} else {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	}
	tail := strings.Split(strings.TrimPrefix(path, base+"/"), "/")
	id := tail[0]
	if !accountIDPattern.MatchString(id) {
		fail(w, r, 404, "NOT_FOUND", "Ticket not found")
		return true
	}
	if len(tail) == 1 && r.Method == "GET" {
		m, err := a.accountDB()
		if err != nil {
			a.accountResult(w, r, nil, err)
			return true
		}
		tx, err := m.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			a.internal(w, r, err)
			return true
		}
		defer tx.Rollback()
		v, err := readTicket(r.Context(), tx, id, u.ID, admin)
		if err == nil {
			err = tx.Commit()
		}
		a.accountResult(w, r, ticketOutput(v, admin), err)
	} else if len(tail) == 1 && r.Method == "PATCH" && admin {
		a.updateTicket(w, r, u, id)
	} else if len(tail) == 2 && tail[1] == "replies" && r.Method == "POST" {
		a.replyTicket(w, r, u, id, admin)
	} else if len(tail) == 2 && tail[1] == "close" && r.Method == "POST" && !admin {
		a.closeTicket(w, r, u, id)
	} else {
		fail(w, r, 404, "NOT_FOUND", "Ticket endpoint not found")
	}
	return true
}
func (a *App) departmentsHTTP(w http.ResponseWriter, r *http.Request, admin bool) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	where := ""
	if !admin {
		where = " WHERE visible=TRUE"
	}
	rows, err := m.DB.QueryContext(r.Context(), "SELECT document FROM support_departments"+where+" ORDER BY sort_order,id")
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []financeapi.Department{}
	for rows.Next() {
		var raw []byte
		var v financeapi.Department
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &v)
		}
		if err != nil {
			a.internal(w, r, err)
			return
		}
		out = append(out, v)
	}
	a.accountResult(w, r, out, rows.Err())
}
func (a *App) listTickets(w http.ResponseWriter, r *http.Request, u User, admin bool) {
	allowed := []string{"cursor", "limit", "status"}
	if admin {
		allowed = append(allowed, "department_id", "q")
	}
	q, err := parseAccountQuery(r, allowed...)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	status := q.Get("status")
	if status != "" && !slices.Contains([]string{"open", "answered", "customer_reply", "on_hold", "closed"}, status) {
		a.accountResult(w, r, nil, problem(400, "INVALID_QUERY", "Invalid ticket status"))
		return
	}
	if !accountText(q.Get("q"), 120) || len(q.Get("department_id")) > 64 {
		a.accountResult(w, r, nil, problem(400, "INVALID_QUERY", "Invalid ticket filter"))
		return
	}
	limit, err := queryLimit(r)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	cursor, err := a.readAccountCursor(r, u.ID)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	where := " WHERE 1=1"
	args := []any{}
	if !admin {
		where += " AND t.user_id=?"
		args = append(args, u.ID)
	}
	if status != "" {
		where += " AND t.status=?"
		args = append(args, status)
	}
	if q.Get("department_id") != "" {
		where += " AND t.department_id=?"
		args = append(args, q.Get("department_id"))
	}
	if q.Get("q") != "" {
		where += " AND (LOCATE(?,t.subject)>0 OR LOCATE(?,t.number)>0 OR LOCATE(?,u.email)>0)"
		args = append(args, q.Get("q"), q.Get("q"), q.Get("q"))
	}
	if !cursor.At.IsZero() {
		where += " AND (t.last_reply_at<? OR (t.last_reply_at=? AND t.id<?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(r.Context(), ticketSelect+where+" ORDER BY t.last_reply_at DESC,t.id DESC LIMIT ?", args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	items := []any{}
	var next *string
	var last supportTicket
	for rows.Next() {
		v, e := scanTicket(rows)
		if e != nil {
			a.internal(w, r, e)
			return
		}
		if len(items) == limit {
			at, _ := time.Parse(time.RFC3339Nano, last.LastReplyAt)
			s := a.makeAccountCursor(r, u.ID, at, last.ID)
			next = &s
			break
		}
		items = append(items, ticketSummary(v, admin))
		last = v
	}
	a.accountResult(w, r, map[string]any{"items": items, "next_cursor": next}, rows.Err())
}
func (a *App) openTicket(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.OpenTicketInput
	if !accountBody(w, r, "openTicket", &in) {
		return
	}
	v, err := a.accountTransaction(r, u.ID, "ticket-open", in, true, func(tx *sql.Tx) (any, error) {
		if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
			return nil, err
		}
		var found string
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM support_departments WHERE id=? AND visible=TRUE", in.DepartmentID).Scan(&found); err != nil {
			return nil, err
		}
		var service any
		if in.ServiceID != "" {
			if err := tx.QueryRowContext(r.Context(), "SELECT id FROM services WHERE id=? AND user_id=?", in.ServiceID, u.ID).Scan(&found); err != nil {
				return nil, err
			}
			service = in.ServiceID
		}
		id := ID()
		number := "T-" + strings.ToUpper(id[:12])
		now := time.Now().UTC().Truncate(time.Microsecond)
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO tickets(id,number,user_id,department_id,subject,status,priority,service_id,created_at,last_reply_at) VALUES(?,?,?,?,?,'open',?,?,?,?)", id, number, u.ID, in.DepartmentID, in.Subject, in.Priority, service, now, now); err != nil {
			return nil, err
		}
		if err := insertTicketMessage(r.Context(), tx, id, u, "client", in.Body, false, now); err != nil {
			return nil, err
		}
		if err := a.queueTicketEmail(r.Context(), tx, u.ID, id, number, in.Subject, in.Body, "ticket_opened"); err != nil {
			return nil, err
		}
		if err := insertAccountAudit(r.Context(), tx, u.ID, u.ID, "ticket.opened", id); err != nil {
			return nil, err
		}
		ticket, err := readTicket(r.Context(), tx, id, u.ID, false)
		return ticket.Ticket, err
	})
	a.accountResult(w, r, v, err)
}
func insertTicketMessage(ctx context.Context, tx *sql.Tx, id string, author User, kind, body string, internal bool, at time.Time) error {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ticket_messages WHERE ticket_id=?", id).Scan(&count); err != nil {
		return err
	}
	if count >= 1000 {
		return problem(409, "TICKET_MESSAGE_LIMIT", "Open a new ticket to continue this conversation")
	}
	var authorID any
	if author.ID != "" {
		authorID = author.ID
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO ticket_messages(id,ticket_id,author_id,author_type,author_name,body,internal,created_at) VALUES(?,?,?,?,?,?,?,?)", ID(), id, authorID, kind, author.Name, body, internal, at)
	return err
}
func (a *App) queueTicketEmail(ctx context.Context, tx *sql.Tx, user, id, number, subject, body, key string) error {
	return a.queueEmailTx(ctx, tx, user, key, map[string]string{"url": a.Config.Origin + "/tickets/" + id, "ticket_number": number, "ticket_subject": subject, "body": body}, false)
}
func (a *App) replyTicket(w http.ResponseWriter, r *http.Request, u User, id string, admin bool) {
	var in financeapi.AdminReplyTicketInput
	if admin {
		if !accountBody(w, r, "adminReplyTicket", &in) {
			return
		}
	} else {
		var input financeapi.ReplyTicketInput
		if !accountBody(w, r, "replyTicket", &input) {
			return
		}
		in.Body = input.Body
	}
	operation := "ticket-reply:" + id
	if admin {
		operation = "staff-ticket-reply:" + id
	}
	v, err := a.accountTransaction(r, u.ID, operation, in, true, func(tx *sql.Tx) (any, error) {
		if err := lockTicket(r.Context(), tx, id, u.ID, admin); err != nil {
			return nil, err
		}
		ticket, err := readTicket(r.Context(), tx, id, u.ID, admin)
		if err != nil {
			return nil, err
		}
		kind, status := "client", "customer_reply"
		if admin {
			kind, status = "staff", "answered"
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		if err = insertTicketMessage(r.Context(), tx, id, u, kind, in.Body, in.Internal, now); err != nil {
			return nil, err
		}
		action := "ticket.replied"
		if in.Internal {
			action = "ticket.internal_note"
		} else {
			if _, err = tx.ExecContext(r.Context(), "UPDATE tickets SET status=?,last_reply_at=? WHERE id=?", status, now, id); err != nil {
				return nil, err
			}
			if admin {
				if err = a.queueTicketEmail(r.Context(), tx, ticket.ClientID, id, ticket.Number, ticket.Subject, in.Body, "ticket_reply"); err != nil {
					return nil, err
				}
			}
		}
		// Internal notes have no client-owned audit row, email or public timestamp.
		owner := ticket.ClientID
		if in.Internal {
			owner = u.ID
		}
		if err = insertAccountAudit(r.Context(), tx, owner, u.ID, action, id); err != nil {
			return nil, err
		}
		ticket, err = readTicket(r.Context(), tx, id, u.ID, admin)
		return ticketOutput(ticket, admin), err
	})
	a.accountResult(w, r, v, err)
}
func (a *App) closeTicket(w http.ResponseWriter, r *http.Request, u User, id string) {
	var in financeapi.CloseTicketInput
	if !accountBody(w, r, "closeTicket", &in) {
		return
	}
	v, err := a.accountTransaction(r, u.ID, "ticket-close:"+id, in, false, func(tx *sql.Tx) (any, error) {
		if err := lockTicket(r.Context(), tx, id, u.ID, false); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE tickets SET status='closed' WHERE id=?", id); err != nil {
			return nil, err
		}
		if err := insertAccountAudit(r.Context(), tx, u.ID, u.ID, "ticket.closed", id); err != nil {
			return nil, err
		}
		ticket, err := readTicket(r.Context(), tx, id, u.ID, false)
		return ticket.Ticket, err
	})
	a.accountResult(w, r, v, err)
}
func (a *App) updateTicket(w http.ResponseWriter, r *http.Request, u User, id string) {
	var fields map[string]json.RawMessage
	if !accountBody(w, r, "adminUpdateTicket", &fields) {
		return
	}
	raw, _ := json.Marshal(fields)
	var in financeapi.AdminUpdateTicketInput
	if err := json.Unmarshal(raw, &in); err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	v, err := a.accountTransaction(r, u.ID, "ticket-update:"+id, fields, false, func(tx *sql.Tx) (any, error) {
		if err := lockTicket(r.Context(), tx, id, u.ID, true); err != nil {
			return nil, err
		}
		ticket, err := readTicket(r.Context(), tx, id, u.ID, true)
		if err != nil {
			return nil, err
		}
		if in.Status != "" {
			ticket.Status = in.Status
		}
		if in.Priority != "" {
			ticket.Priority = in.Priority
		}
		if in.DepartmentID != "" {
			var found string
			if err = tx.QueryRowContext(r.Context(), "SELECT id FROM support_departments WHERE id=?", in.DepartmentID).Scan(&found); err != nil {
				return nil, err
			}
			ticket.DepartmentID = in.DepartmentID
		}
		if _, set := fields["assignee_id"]; set {
			if in.AssigneeID != nil {
				var found string
				if err = tx.QueryRowContext(r.Context(), "SELECT u.id FROM users u JOIN staff_members s ON s.user_id=u.id WHERE u.id=? AND u.role='admin' AND u.disabled=FALSE", *in.AssigneeID).Scan(&found); err != nil {
					return nil, err
				}
			}
			ticket.AssigneeID = in.AssigneeID
		}
		if _, err = tx.ExecContext(r.Context(), "UPDATE tickets SET status=?,priority=?,department_id=?,assignee_id=? WHERE id=?", ticket.Status, ticket.Priority, ticket.DepartmentID, ticket.AssigneeID, id); err != nil {
			return nil, err
		}
		if err = insertAccountAudit(r.Context(), tx, ticket.ClientID, u.ID, "ticket.updated", id); err != nil {
			return nil, err
		}
		ticket, err = readTicket(r.Context(), tx, id, u.ID, true)
		return ticket, err
	})
	a.accountResult(w, r, v, err)
}

func (a *App) closeInactiveTickets(ctx context.Context, s financeapi.AutomationSettings, at time.Time) (AutomationResult, error) {
	result := AutomationResult{}
	if s.TicketAutocloseHours <= 0 {
		return result, nil
	}
	m, err := a.accountDB()
	if err != nil {
		return result, err
	}
	cutoff := at.UTC().Add(-time.Duration(s.TicketAutocloseHours) * time.Hour)
	for range 200 {
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return result, err
		}
		var id, owner string
		err = tx.QueryRowContext(ctx, "SELECT id,user_id FROM tickets WHERE status='answered' AND last_reply_at<=? ORDER BY last_reply_at,id LIMIT 1 FOR UPDATE SKIP LOCKED", cutoff).Scan(&id, &owner)
		if err == sql.ErrNoRows {
			tx.Rollback()
			return result, nil
		}
		if err != nil {
			tx.Rollback()
			return result, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE tickets SET status='closed' WHERE id=? AND status='answered'", id)
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,'ticket.auto_closed',?,?,'system','Closed after the configured inactivity period')", ID(), owner, id, at.UTC())
		}
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return result, err
		}
		result.Processed++
	}
	return result, nil
}
