package traffic

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

func TestRepeatedStreamBodyReusesOpenBlob(t *testing.T) {
	tr, _ := openTraffic(t)
	want := bytes.Repeat([]byte("x"), maxInlineBody+1)
	capture := func() storedBody {
		t.Helper()
		body := newCapturedBody(tr, bytes.NewReader(want))
		if _, err := io.Copy(io.Discard, body); err != nil {
			t.Fatal(err)
		}
		stored, _, err := body.finish(tr, "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		return stored
	}
	first := capture()
	reader, _, err := tr.Blob(first.hash)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if second := capture(); second.hash != first.hash {
		t.Fatal("duplicate body did not reuse blob")
	}
}

func TestStreamedTrafficPreservesBodies(t *testing.T) {
	large := bytes.Repeat([]byte("stream body\n"), 600000)
	for _, tc := range []struct {
		name, contentType string
		request, response []byte
	}{
		{"large response", "text/plain", nil, large},
		{"large upload", "application/octet-stream", large, []byte("accepted")},
		{"event stream", "text/event-stream", nil, []byte("data: first\n\ndata: last\n\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, tc.request) {
					t.Errorf("upstream request differs: %v", err)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.Write(tc.response)
			}))
			defer upstream.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := listener.Addr().String()
			listener.Close()
			dir := t.TempDir()
			tr, err := Open(dir, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			go tr.Start()
			proxyURL, _ := url.Parse("http://" + addr)
			client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			var response *http.Response
			for range 100 {
				response, err = client.Post(upstream.URL, "application/octet-stream", bytes.NewReader(tc.request))
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || !bytes.Equal(got, tc.response) {
				t.Fatalf("forwarded body differs: %v", err)
			}
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dir, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			id := onlyExchangeID(t, reopened)
			err = reopened.ReadEvidence(context.Background(), []string{id}, func(e EvidenceExchange) error {
				request, reqErr := io.ReadAll(e.Request)
				body, respErr := io.ReadAll(e.Response)
				if reqErr != nil || respErr != nil || !bytes.Equal(request, tc.request) || !bytes.Equal(body, tc.response) || e.ReqLen != int64(len(tc.request)) || e.RespLen != int64(len(tc.response)) {
					t.Errorf("captured request/response mismatch: sizes=%d/%d errors=%v/%v", e.ReqLen, e.RespLen, reqErr, respErr)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if files, _ := filepath.Glob(filepath.Join(dir, "_blobs", ".capture-*")); len(files) != 0 {
				t.Fatalf("temporary capture files remain: %v", files)
			}
		})
	}
}

func TestStreamRecordingFailurePreservesForwardedBody(t *testing.T) {
	tr, dir := openTraffic(t)
	want := []byte("forward despite recorder failure")
	body := newCapturedBody(tr, bytes.NewReader(want))
	if body.err != nil {
		t.Fatal(body.err)
	}
	if err := body.file.Close(); err != nil {
		t.Fatal(err)
	}
	defer body.discard()
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("recording failure changed forwarded body: %q %v", got, err)
	}
	if _, _, err := body.finish(tr, "text/plain"); err == nil {
		t.Fatal("failed recording was reported as a complete body")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "_blobs", ".capture-*")); len(files) != 0 {
		t.Fatalf("failed capture left temporary files: %v", files)
	}
}
