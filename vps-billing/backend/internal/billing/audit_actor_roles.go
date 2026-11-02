package billing

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

func (m *MySQL) auditActorRoles(ctx context.Context, events []Audit) (map[string]string, error) {
	roles := map[string]string{}
	if len(events) > 100 {
		return roles, ErrAuditQuery
	}
	emails, selects, args := []string{}, []string{}, []any{}
	for _, event := range events {
		email := event.ActorEmail
		if email == "" || email == "system" || strings.HasPrefix(email, "payment:") || roles[email] != "" {
			continue
		}
		roles[email] = "staff" // Unknown historical actors remain conservative.
		ascii := true
		for i := range len(email) {
			if email[i] > 127 {
				ascii = false
				break
			}
		}
		if !ascii {
			continue
		}
		selects = append(selects, "SELECT "+strconv.Itoa(len(emails))+" n,CONVERT(? USING ascii) email")
		emails, args = append(emails, email), append(args, email)
	}
	if len(emails) == 0 {
		return roles, nil
	}
	// Let MySQL apply the same ASCII account collation as UserByEmail, including
	// case and padding rules. Ordinals associate results with original audit
	// identities without rewriting their immutable spelling or Unicode text.
	rows, err := m.DB.QueryContext(ctx, "SELECT actors.n,u.role FROM ("+strings.Join(selects, " UNION ALL ")+") actors LEFT JOIN users u ON u.email=actors.email", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var role sql.NullString
		if err := rows.Scan(&n, &role); err != nil {
			return nil, err
		}
		if role.Valid && role.String == "customer" {
			roles[emails[n]] = "client"
		}
	}
	return roles, rows.Err()
}
