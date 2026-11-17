package traffic

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strings"
)

// Group only request metadata. Bodies, query values and capture IDs stay intact.
const routeURLSQL = `substr(url,1,min(instr(url||'?','?'),instr(url||'#','#'))-1)`

func ensureRouteIndex(ctx context.Context, conn *sql.Conn) error {
	var hasRoute bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM pragma_table_xinfo('exchanges') WHERE name='route_url'
)`).Scan(&hasRoute); err != nil {
		return err
	}
	if !hasRoute {
		if _, err := conn.ExecContext(ctx, `ALTER TABLE exchanges ADD COLUMN route_url TEXT
 GENERATED ALWAYS AS (`+routeURLSQL+`) VIRTUAL`); err != nil {
			return err
		}
	}
	// A named virtual column lets SQLite serve the aggregation from the index
	// alone. An expression index still fetches original URLs for grouped output.
	_, err := conn.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_ex_route_stats
 ON exchanges(method,route_url,status,ts,resp_len)`)
	return err
}

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
	col := map[string]string{"status": "max_status", "resp_len": "resp_len", "ts": "last_ts"}[f.Sort]
	if col == "" {
		col = "last_ts"
	}
	dir := "DESC"
	if strings.EqualFold(f.Order, "asc") {
		dir = "ASC"
	}
	// Reduce captures once by route and status. Both totals and the page then
	// read the smaller route aggregates, including when the requested page is empty.
	order := col + " " + dir + ",route_url,method"
	q := `WITH by_status AS (
 SELECT method,route_url,status,count(*) AS captures,
 min(ts) AS first_ts,max(ts) AS last_ts,sum(resp_len) AS resp_len
 FROM exchanges` + where + ` GROUP BY method,route_url,status
), routes AS (
 SELECT method,route_url,sum(captures) AS captures,min(first_ts) AS first_ts,max(last_ts) AS last_ts,
 sum(resp_len) AS resp_len,max(status) AS max_status,json_group_object(status,captures) AS statuses
 FROM by_status GROUP BY method,route_url
), totals AS (
 SELECT count(*) AS total,COALESCE(sum(captures),0) AS matched FROM routes
), page AS (
 SELECT * FROM routes WHERE method IS NOT NULL AND route_url IS NOT NULL
 ORDER BY ` + order + ` LIMIT ? OFFSET ?
)
SELECT method,route_url,COALESCE(captures,0),COALESCE(first_ts,0),COALESCE(last_ts,0),COALESCE(resp_len,0),
 COALESCE(statuses,''),total,matched FROM totals LEFT JOIN page ON true ORDER BY ` + order
	qargs := append(append([]any{}, args...), size, page*size)
	rows, e := t.db.Query(q, qargs...)
	if e != nil {
		err = e
		return
	}
	defer rows.Close()
	for rows.Next() {
		var g RouteGroup
		var method, route sql.NullString
		var statuses string
		if err = rows.Scan(&method, &route, &g.Count, &g.FirstTS, &g.LastTS, &g.RespLen, &statuses, &total, &matched); err != nil {
			return
		}
		if !method.Valid {
			continue
		}
		g.Method, g.URL = method.String, route.String
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
