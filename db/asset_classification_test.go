package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHTTPServiceIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"https://EXAMPLE.com:443/api?x=1#result", "https://example.com"},
		{"http://example.com:8080/app/", "http://example.com:8080"},
		{"http://[::1]:80/a", "http://[::1]"},
	} {
		got, err := HTTPServiceURL(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q %v", tc.raw, got, err)
		}
	}
	if _, err := HTTPServiceURL("ftp://example.com/a"); err == nil {
		t.Fatal("accepted non-HTTP service")
	}
}

func TestAssetClassificationUpgradePreservesReferences(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	name := fmt.Sprintf("classification_%d", time.Now().UnixNano())
	if _, err = conn.ExecContext(ctx, `CREATE SCHEMA `+name+`; SET search_path TO `+name+`; SET check_function_bodies=false`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `SET search_path TO public; DROP SCHEMA `+name+` CASCADE`)
	if _, err = conn.ExecContext(ctx, strings.ReplaceAll(schemaSQL, "public.", name+".")); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `
INSERT INTO settings(key,value) VALUES('database_schema_revision','2');
INSERT INTO explorations(id,goal) VALUES(1,'classification');
INSERT INTO tasks(id,description,goal,exploration_id) VALUES(1,'classification','goal',1);
INSERT INTO assets(id,type,domain) VALUES(100,'root_domain','203.0.113.91');
INSERT INTO assets(id,type,ip) VALUES(101,'ip','203.0.113.91');
INSERT INTO assets(id,type,service_type,url,domain) VALUES(102,'service','http','https://opt.example/api?x=1','opt.example'),(103,'service','http','https://opt.example','opt.example'),(105,'service','http','https://opt.example/new?y=2','opt.example');
INSERT INTO assets(id,type,url,method) VALUES(104,'endpoint','https://opt.example/api','POST');
INSERT INTO assets(id,type,url,method) VALUES(106,'endpoint','opt.example/legacy','POST');
INSERT INTO exploration_nodes(id,exploration_id,kind,state,payload) VALUES(500,1,'fact','confirmed','{"asset_ids":[100,102]}');
INSERT INTO exploration_anchors(node_id,asset_id) VALUES(500,100),(500,102);
INSERT INTO task_asset_links(task_id,asset_id,source,source_summary) VALUES(1,100,'manual','original provenance'),(1,101,'system',''),(1,102,'agent','path found');
INSERT INTO findings(id,task_id,asset_ids) VALUES(600,1,'[100,102]');
INSERT INTO conversations(id,agent_key,title) VALUES(20,'reporter','工具触发 · report_finding · task#1');
INSERT INTO conversation_activities(conversation_id,kind,detail) VALUES(20,'user','{"name":"Specific finding with \"quotes\""}');
`); err != nil {
		t.Fatal(err)
	}
	if err = initializeSchema(ctx, conn); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM assets WHERE type='root_domain' AND try_inet(domain) IS NOT NULL`,
		`SELECT count(*) FROM assets WHERE id IN(100,102)`,
	} {
		var n int
		if err = conn.QueryRowContext(ctx, q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d %v", q, n, err)
		}
	}
	var n int
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE id=106 AND url='opt.example/legacy'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy address %d %v", n, err)
	}
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM assets WHERE type='endpoint' AND url='https://opt.example/new' AND method='UNKNOWN' AND params[1]->>'name'='y'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unknown method and query params %d %v", n, err)
	}
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM exploration_anchors WHERE node_id=500 AND asset_id IN(101,103,104)`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("anchors %d %v", n, err)
	}
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM findings WHERE id=600 AND asset_ids @> '[101,103]'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("finding references %d %v", n, err)
	}
	var source string
	if err = conn.QueryRowContext(ctx, `SELECT source FROM task_asset_links WHERE task_id=1 AND asset_id=101`).Scan(&source); err != nil || source != "manual" {
		t.Fatalf("source %q %v", source, err)
	}
	if err = conn.QueryRowContext(ctx, `SELECT count(*) FROM conversations WHERE id=20 AND source='automation' AND source_task_id=1 AND title='Specific finding with "quotes"'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("conversation classification %d %v", n, err)
	}
	if err = initializeSchema(ctx, conn); err != nil {
		t.Fatalf("second startup: %v", err)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE conversations SET title=$1 WHERE id=20`, `Specific finding with \`); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, `UPDATE settings SET value='3' WHERE key='database_schema_revision'`); err != nil {
		t.Fatal(err)
	}
	if err = initializeSchema(ctx, conn); err != nil {
		t.Fatalf("revision 3 title repair: %v", err)
	}
	var title string
	if err = conn.QueryRowContext(ctx, `SELECT title FROM conversations WHERE id=20`).Scan(&title); err != nil || title != `Specific finding with "quotes"` {
		t.Fatalf("repaired title %q %v", title, err)
	}
}

func TestConversationStatsIndependentOfHistoryPage(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c, err := d.CreateConversation("auto", "stats fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteConversation(c.ID)
	for _, a := range []Activity{{Kind: "usage", InputTokens: intp(10)}, {Kind: "usage", InputTokens: intp(25)}, {Kind: "result", InputTokens: intp(25)}, {Kind: "usage", InputTokens: intp(7)}} {
		if _, err = d.AppendConvActivity(c.ID, a); err != nil {
			t.Fatal(err)
		}
	}
	before, err := d.ConversationStats(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, more, err := d.ConvActivityPage(c.ID, 0, 2)
	if err != nil || !more || len(page) != 2 {
		t.Fatalf("page %v %v %v", page, more, err)
	}
	after, err := d.ConversationStats(c.ID)
	if err != nil || before != after || after.Turns != 3 || after.InputTokens != 32 {
		t.Fatalf("stats before=%+v after=%+v %v", before, after, err)
	}
}
