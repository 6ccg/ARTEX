package traffic

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

type streamCapture struct{ request, response *capturedBody }

type capturedBody struct {
	mu     sync.Mutex
	reader io.Reader
	file   *os.File
	hash   hash.Hash
	prefix []byte
	length int64
	eof    bool
	err    error
}

func newCapturedBody(t *Traffic, reader io.Reader) *capturedBody {
	file, err := os.CreateTemp(filepath.Join(t.dir, "_blobs"), ".capture-*")
	return &capturedBody{reader: reader, file: file, hash: sha256.New(), err: err}
}

func (b *capturedBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.reader.Read(p)
	if n > 0 && b.err == nil {
		_, b.err = b.file.Write(p[:n])
		_, _ = b.hash.Write(p[:n])
		b.length += int64(n)
		b.prefix = append(b.prefix, p[:min(n, maxIndexBody-len(b.prefix))]...)
	}
	if err == io.EOF {
		b.eof = true
	} else if err != nil {
		b.err = errors.Join(b.err, err)
	}
	return n, err // Recording failure must not change the forwarded response.
}

func (b *capturedBody) finish(t *Traffic, contentType string) (storedBody, int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file == nil {
		return storedBody{}, 0, b.err
	}
	defer os.Remove(b.file.Name())
	err := errors.Join(b.err, b.file.Close())
	if err != nil {
		return storedBody{}, 0, err
	}
	if !b.eof {
		return storedBody{}, 0, errors.New("stream body did not reach EOF")
	}
	if b.length <= maxInlineBody {
		return t.spill(b.prefix, contentType), b.length, nil
	}
	h := hex.EncodeToString(b.hash.Sum(nil))
	destination := filepath.Join(t.dir, "_blobs", "sha256", h[:2], h+".bin")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return storedBody{}, 0, err
	}
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		if err := os.Rename(b.file.Name(), destination); err != nil {
			return storedBody{}, 0, err
		}
	} else if err != nil {
		return storedBody{}, 0, err
	}
	body := storedBody{hash: h}
	if isBinaryBody(contentType, b.prefix) {
		body.inline = []byte(binaryTag(contentType, b.prefix, b.length))
	} else {
		body.inline = []byte(truncateUTF8(b.prefix, blobPreview))
		body.index = string(b.prefix)
	}
	return body, b.length, nil
}

func (b *capturedBody) discard() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		b.file.Close()
		os.Remove(b.file.Name())
	}
}

func (s *sink) streamed(f *mproxy.Flow) *streamCapture {
	value, loaded := s.streams.LoadOrStore(f, &streamCapture{})
	capture := value.(*streamCapture)
	if !loaded {
		s.t.captures.Go(func() {
			<-f.Done()
			defer s.streams.Delete(f)
			defer capture.request.discard()
			defer capture.response.discard()
			if f.Response == nil {
				return
			}
			s.t.wmu.Lock()
			defer s.t.wmu.Unlock()
			body := func(stream *capturedBody, inline []byte, contentType string) (storedBody, int64, error) {
				if stream != nil {
					return stream.finish(s.t, contentType)
				}
				return s.t.spill(inline, contentType), int64(len(inline)), nil
			}
			req, reqLen, reqErr := body(capture.request, f.Request.Body, f.Request.Header.Get("Content-Type"))
			resp, respLen, respErr := body(capture.response, f.Response.Body, f.Response.Header.Get("Content-Type"))
			if err := errors.Join(reqErr, respErr); err != nil {
				log.Printf("[traffic] 流式正文录制未完成 %s: %v", f.Request.URL, err)
				return
			}
			s.t.recordBodies(f, req, resp, reqLen, respLen)
		})
	}
	return capture
}

func (s *sink) StreamRequestModifier(f *mproxy.Flow, reader io.Reader) io.Reader {
	if !f.Stream {
		return reader
	}
	capture := s.streamed(f)
	capture.request = newCapturedBody(s.t, reader)
	return capture.request
}

func (s *sink) StreamResponseModifier(f *mproxy.Flow, reader io.Reader) io.Reader {
	if !f.Stream {
		return reader
	}
	capture := s.streamed(f)
	capture.response = newCapturedBody(s.t, reader)
	return capture.response
}
