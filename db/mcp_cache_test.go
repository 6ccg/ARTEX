package db

import (
	"encoding/json"
	"testing"
)

func TestMCPToolSchemaCacheAndInvalidation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m := &MCPServer{Name: "test-lazy-cache", Transport: "http", URL: "http://example.test", Enabled: true}
	id, err := d.SaveMCP(m)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteMCP(id)
	m.ID = id
	err = d.SaveMCPTools(id, []MCPTool{{Name: "mcp__test-lazy-cache__ping", Description: "ping", Schema: json.RawMessage(`{"type":"object","required":["x"]}`)}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.MCPToolsDetailed(id)
	if err != nil || len(got) != 1 || len(got[0].Schema) == 0 {
		t.Fatalf("cache: %+v %v", got, err)
	}
	m.URL = "http://example2.test"
	if _, err = d.SaveMCP(m); err != nil {
		t.Fatal(err)
	}
	got, err = d.MCPToolsDetailed(id)
	if err != nil || len(got) != 1 || len(got[0].Schema) != 0 {
		t.Fatalf("invalidation: %+v %v", got, err)
	}
}
