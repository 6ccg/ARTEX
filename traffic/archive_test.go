package traffic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivePreservesLegacyBinaryBody(t *testing.T) {
	source, dir := openTraffic(t)
	insertArchiveRecoveryExchange(t, source, "legacy", "archive.example")
	body := []byte{0, 255, 128, 'x'}
	legacy := filepath.Join(dir, "legacy")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "request.http"), []byte("GET / HTTP/1.1\r\nHost: archive.example\r\n\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "response.http"), append([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\n\r\n"), body...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := source.db.Exec(`UPDATE exchanges SET path='legacy',resp_len=? WHERE id='legacy'`, len(body)); err != nil {
		t.Fatal(err)
	}
	archive := t.TempDir()
	if _, err := source.ExportHosts([]string{"archive.example"}, archive); err != nil {
		t.Fatal(err)
	}
	destination, _ := openTraffic(t)
	if _, err := destination.ImportArchive(archive); err != nil {
		t.Fatal(err)
	}
	err := destination.ReadEvidence(context.Background(), []string{"legacy"}, func(e EvidenceExchange) error {
		got, err := io.ReadAll(e.Response)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("restored legacy body=%x, want %x: %v", got, body, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRepairsSameSizeCorruptBlob(t *testing.T) {
	source, _ := openTraffic(t)
	body := bytes.Repeat([]byte("x"), maxInlineBody+1)
	source.record(newFlow("archive.example", "GET", "/large", nil, body))
	archive := t.TempDir()
	if _, err := source.ExportHosts([]string{"archive.example"}, archive); err != nil {
		t.Fatal(err)
	}
	destination, _ := openTraffic(t)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	path := filepath.Join(destination.dir, "_blobs", "sha256", hash[:2], hash+".bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("z"), len(body)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.ImportArchive(archive); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("restore reused a corrupt blob of the expected size: %v", err)
	}
	// Missing manifest references must fail even when full-text indexing is off.
	raw, err := os.ReadFile(filepath.Join(archive, "traffic.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot ArchiveSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Blobs = nil
	raw, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "traffic.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	missing, _ := openTraffic(t)
	missing.fts = false
	if count, err := missing.ImportArchive(archive); err == nil || count != 0 {
		t.Fatalf("incomplete archive imported %d exchanges: %v", count, err)
	}
}

func TestTrafficArchiveRestoresFullBodySearch(t *testing.T) {
	source, _ := openTraffic(t)
	body := append(bytes.Repeat([]byte("prefix "), maxInlineBody/7+1), []byte(" archiveuniquetail")...)
	source.record(newFlow("archive.example", "GET", "/large", nil, body))
	archive := t.TempDir()
	if _, err := source.ExportHosts([]string{"archive.example"}, archive); err != nil {
		t.Fatal(err)
	}
	destination, _ := openTraffic(t)
	if _, err := destination.ImportArchive(archive); err != nil {
		t.Fatal(err)
	}
	var matches int
	if err := destination.db.QueryRow(`SELECT count(*) FROM ex_fts WHERE ex_fts MATCH ?`, "archiveuniquetail").Scan(&matches); err != nil || matches != 1 {
		t.Fatalf("restored body tail is not searchable: matches=%d err=%v", matches, err)
	}
}

func TestTrafficArchiveRoundTripAndCurrentRowWins(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const (
		id   = "archive-exchange-1"
		host = "archive.example"
	)
	requestHead := "POST /v1/test HTTP/1.1\nHost: archive.example"
	responseHead := "HTTP/1.1 200 OK\nContent-Type: application/json"
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "POST", "/v1/test", "https://archive.example/v1/test", 200,
		"application/json", 7, 11, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.DB().Exec(`INSERT INTO exchange_bodies(id,req_head,req_body,resp_head,resp_body)
VALUES(?,?,?,?,?)`, id, requestHead, []byte("payload"), responseHead, []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}

	archiveDir := filepath.Join(t.TempDir(), "traffic")
	count, err := tr.ExportHosts([]string{host, host}, archiveDir)
	if err != nil || count != 1 {
		t.Fatalf("ExportHosts count=%d err=%v", count, err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "traffic.json")); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExact([]string{host})
	if err != nil {
		t.Fatal(err)
	}
	if stage.Deleted() != 1 {
		t.Fatalf("staged deleted=%d, want 1", stage.Deleted())
	}
	if err := stage.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Get(id); err == nil {
		t.Fatal("deleted exchange remained readable")
	}
	imported, err := tr.ImportArchive(archiveDir)
	if err != nil || imported != 1 {
		t.Fatalf("ImportArchive imported=%d err=%v", imported, err)
	}
	req, resp, err := tr.Get(id)
	if err != nil || !strings.Contains(req, "payload") || !strings.Contains(resp, `{"ok":true}`) {
		t.Fatalf("restored exchange req=%q resp=%q err=%v", req, resp, err)
	}
	if _, err := tr.DB().Exec(`UPDATE exchange_bodies SET resp_body=? WHERE id=?`, []byte(`{"current":true}`), id); err != nil {
		t.Fatal(err)
	}
	imported, err = tr.ImportArchive(archiveDir)
	if err != nil || imported != 0 {
		t.Fatalf("idempotent import imported=%d err=%v", imported, err)
	}
	_, resp, err = tr.Get(id)
	if err != nil || !strings.Contains(resp, `{"current":true}`) || strings.Contains(resp, `{"ok":true}`) {
		t.Fatalf("archive overwrote current exchange resp=%q err=%v", resp, err)
	}
}

func TestRecoverArchiveHostDeleteStageRollsBackBeforePostgresCommit(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const host = "rollback-archive.example"
	insertArchiveRecoveryExchange(t, tr, "rollback-exchange", host)
	legacyDir := filepath.Join(trafficDir, sanitize(host))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "request.http"), []byte("request"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExactForArchive([]string{host}, 17, 42)
	if err != nil {
		t.Fatal(err)
	}
	simulateTrafficStageProcessExit(t, stage)
	if err := tr.RecoverHostDeleteStages(func(id, taskID int64) (bool, error) {
		if id != 17 || taskID != 42 {
			t.Fatalf("archive id=%d task=%d, want 17/42", id, taskID)
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, "request.http")); err != nil {
		t.Fatalf("legacy traffic tree not restored: %v", err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exchange count=%d err=%v, want 1", count, err)
	}
}

func TestRecoverArchiveHostDeleteStageCompletesAfterPostgresCommit(t *testing.T) {
	trafficDir := t.TempDir()
	tr, err := Open(trafficDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	const host = "committed-archive.example"
	insertArchiveRecoveryExchange(t, tr, "committed-exchange", host)
	legacyDir := filepath.Join(trafficDir, sanitize(host))
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := tr.StageDeleteHostsExactForArchive([]string{host}, 18, 43)
	if err != nil {
		t.Fatal(err)
	}
	simulateTrafficStageProcessExit(t, stage)
	if err := tr.RecoverHostDeleteStages(func(id, taskID int64) (bool, error) { return id == 18 && taskID == 43, nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyDir); !os.IsNotExist(err) {
		t.Fatalf("committed legacy traffic tree remains: %v", err)
	}
	var count int
	if err := tr.DB().QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil || count != 0 {
		t.Fatalf("exchange count=%d err=%v, want 0", count, err)
	}
}

func insertArchiveRecoveryExchange(t *testing.T, tr *Traffic, id, host string) {
	t.Helper()
	if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`, id, 1, host, "GET", "/", "https://"+host+"/", 200, "text/plain", 0, 0, ""); err != nil {
		t.Fatal(err)
	}
}

func simulateTrafficStageProcessExit(t *testing.T, stage *HostDeleteStage) {
	t.Helper()
	if err := stage.tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	stage.done = true
	stage.traffic.wmu.Unlock()
}
