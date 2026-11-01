package billing

import (
	"net/url"
	"testing"
)

func TestMySQLAnnouncementsPublicationPaginationAndPermissions(t *testing.T) {
	f, _, srv := accountServer(t)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	anon := client(t, srv.URL, "", "")
	input := map[string]any{"title": map[string]string{"en": "Maintenance"}, "body": map[string]string{"en": "Scheduled maintenance notice."}, "pinned": false, "published": false}
	draft := accountHTTP(t, owner, "POST", "/admin/announcements", "/admin/announcements", input, "").(map[string]any)
	public := accountHTTP(t, anon, "GET", "/announcements", "/announcements", nil, "").(map[string]any)
	if len(public["items"].([]any)) != 0 {
		t.Fatal("draft published")
	}
	input["id"], input["published"] = draft["id"], true
	accountHTTP(t, owner, "POST", "/admin/announcements", "/admin/announcements", input, "")
	delete(input, "id")
	input["pinned"] = true
	input["title"] = map[string]string{"en": "Pinned notice"}
	pinned := accountHTTP(t, owner, "POST", "/admin/announcements", "/admin/announcements", input, "").(map[string]any)
	first := accountHTTP(t, anon, "GET", "/announcements?limit=1", "/announcements", nil, "").(map[string]any)
	if first["items"].([]any)[0].(map[string]any)["id"] != pinned["id"] || first["next_cursor"] == nil {
		t.Fatal("pinned order", first)
	}
	second := accountHTTP(t, anon, "GET", "/announcements?limit=1&cursor="+url.QueryEscape(first["next_cursor"].(string)), "/announcements", nil, "").(map[string]any)
	if len(second["items"].([]any)) != 1 || second["items"].([]any)[0].(map[string]any)["id"] != draft["id"] {
		t.Fatal("pagination", second)
	}
	accountHTTP(t, owner, "GET", "/admin/announcements", "/admin/announcements", nil, "")
	role := accountHTTP(t, owner, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "No content", "description": "Read tickets", "permissions": []string{"tickets.read"}}, "").(map[string]any)
	accountHTTP(t, owner, "PATCH", "/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": role["id"]}, "")
	limited := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	for _, v := range []struct {
		method, path string
		body         any
	}{{"GET", "/admin/announcements", nil}, {"POST", "/admin/announcements", input}, {"DELETE", "/admin/announcements/" + draft["id"].(string), nil}} {
		accountFailure(t, limited, v.method, v.path, v.body, 403)
	}
	accountHTTP(t, owner, "DELETE", "/admin/announcements/"+draft["id"].(string), "/admin/announcements/{id}", nil, "")
	accountFailure(t, owner, "DELETE", "/admin/announcements/"+draft["id"].(string), nil, 404)
}
