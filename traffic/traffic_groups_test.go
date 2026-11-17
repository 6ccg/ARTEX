package traffic

import "testing"

func TestRouteGroupsAndExactDrilldown(t *testing.T) {
	tr, _ := openTraffic(t)
	tr.record(newFlow("group.example", "GET", "/api?a=1", nil, []byte("one")))
	failed := newFlow("group.example", "GET", "/api?a=2", nil, []byte("two"))
	failed.Response.StatusCode = 500
	tr.record(failed)
	tr.record(newFlow("group.example", "POST", "/api", nil, []byte("three-body")))
	tr.record(newFlow("other.example", "GET", "/api", nil, []byte("other")))
	f := PageQuery{RespMin: -1, RespMax: -1}
	groups, total, matched, err := tr.GroupPage(f, 0, 1)
	if err != nil || total != 3 || matched != 4 || len(groups) != 1 {
		t.Fatalf("page groups=%+v total=%d matched=%d err=%v", groups, total, matched, err)
	}
	groups, total, matched, err = tr.GroupPage(PageQuery{Hosts: []string{"group.example"}, RespMin: -1, RespMax: -1}, 0, 10)
	if err != nil || total != 2 || matched != 3 {
		t.Fatalf("scope %d %d %v", total, matched, err)
	}
	for _, g := range groups {
		if g.Method == "GET" {
			if g.Count != 2 || g.Statuses[200] != 1 || g.Statuses[500] != 1 || g.RespLen != 6 {
				t.Fatalf("distribution %+v", g)
			}
			rows, n, err := tr.Page(PageQuery{RouteURL: g.URL, Method: g.Method, RespMin: -1, RespMax: -1}, 0, 1)
			if err != nil || n != 2 || len(rows) != 1 {
				t.Fatalf("drilldown %d %d %v", n, len(rows), err)
			}
			rows, n, err = tr.Page(PageQuery{RouteURL: g.URL, Method: g.Method, RespMin: -1, RespMax: -1}, 1, 1)
			if err != nil || n != 2 || len(rows) != 1 {
				t.Fatalf("drilldown next page %d %d %v", n, len(rows), err)
			}
		}
	}
	groups, total, matched, err = tr.GroupPage(PageQuery{Status: "5xx", RespMin: -1, RespMax: -1}, 0, 10)
	if err != nil || total != 1 || matched != 1 || len(groups) != 1 || groups[0].Count != 1 || groups[0].Statuses[500] != 1 {
		t.Fatalf("filtered distribution=%+v total=%d matched=%d err=%v", groups, total, matched, err)
	}
	for _, sort := range []string{"status", "resp_len"} {
		groups, _, _, err = tr.GroupPage(PageQuery{Sort: sort, RespMin: -1, RespMax: -1}, 0, 1)
		wantMethod := "GET"
		if sort == "resp_len" {
			wantMethod = "POST"
		}
		if err != nil || len(groups) != 1 || groups[0].Method != wantMethod {
			t.Fatalf("sort %s groups=%+v err=%v", sort, groups, err)
		}
	}
}
