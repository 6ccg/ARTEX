package traffic

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Group only request metadata. Bodies, query values and capture IDs stay intact.
const routeWithoutQuerySQL = `CASE WHEN instr(url,'?')>0 THEN substr(url,1,instr(url,'?')-1) ELSE url END`
const routeURLSQL = `CASE WHEN instr(` + routeWithoutQuerySQL + `,'#')>0 THEN substr(` + routeWithoutQuerySQL + `,1,instr(` + routeWithoutQuerySQL + `,'#')-1) ELSE ` + routeWithoutQuerySQL + ` END`

type RouteGroup struct {
	ID       string      `json:"id"`
	URL      string      `json:"url"`
	Host     string      `json:"host"`
	Method   string      `json:"method"`
	Count    int         `json:"count"`
	FirstTS  int64       `json:"first_ts"`
	LastTS   int64       `json:"last_ts"`
	RespLen  int64       `json:"resp_len"`
	Statuses map[int]int `json:"statuses"`
}

// GroupPage aggregates the entire matching result before server-side pagination.
func (t *Traffic) GroupPage(f PageQuery, page, size int) (groups []RouteGroup, total, matched int, err error) {
	groups = []RouteGroup{}
	if page < 0 {
		page = 0
	}
	if size <= 0 || size > 500 {
		size = 100
	}
	where, args := t.pageFilter(f)
	base := `WITH filtered AS (SELECT *,` + routeURLSQL + ` AS route_url FROM exchanges` + where + `) `
	err = t.db.QueryRow(base+`SELECT (SELECT count(*) FROM (SELECT 1 FROM filtered GROUP BY method,route_url)),count(*) FROM filtered`, args...).Scan(&total, &matched)
	if err != nil {
		return
	}
	col := map[string]string{"status": "a.max_status", "resp_len": "a.resp_len", "ts": "a.last_ts"}[f.Sort]
	if col == "" {
		col = "a.last_ts"
	}
	dir := "DESC"
	if strings.EqualFold(f.Order, "asc") {
		dir = "ASC"
	}
	// Aggregate statuses once for the complete filtered set. A correlated status
	// subquery per route repeatedly scanned the capture index on every page load.
	q := base + `, route_stats AS (
 SELECT method,route_url,count(*) AS captures,min(ts) AS first_ts,max(ts) AS last_ts,
 sum(resp_len) AS resp_len,max(status) AS max_status FROM filtered GROUP BY method,route_url
), status_stats AS (
 SELECT method,route_url,json_group_object(status,n) AS statuses FROM (
  SELECT method,route_url,status,count(*) AS n FROM filtered GROUP BY method,route_url,status
 ) GROUP BY method,route_url
)
SELECT a.method,a.route_url,a.captures,a.first_ts,a.last_ts,a.resp_len,b.statuses,a.max_status
FROM route_stats a JOIN status_stats b USING(method,route_url)
ORDER BY ` + col + ` ` + dir + `,a.route_url,a.method LIMIT ? OFFSET ?`
	qargs := append(append([]any{}, args...), size, page*size)
	rows, e := t.db.Query(q, qargs...)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var g RouteGroup
		var statuses string
		var maxStatus int
		if err = rows.Scan(&g.Method, &g.URL, &g.Count, &g.FirstTS, &g.LastTS, &g.RespLen, &statuses, &maxStatus); err != nil {
			return
		}
		if err = json.Unmarshal([]byte(statuses), &g.Statuses); err != nil {
			return
		}
		g.ID = g.Method + " " + g.URL
		if u, e := url.Parse(g.URL); e == nil {
			g.Host = u.Host
		}
		groups = append(groups, g)
	}
	err = rows.Err()
	return
}
