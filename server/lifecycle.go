package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/Autumn-27/artex/agent"
)

func (s *Runtime) stopping() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.closing || errors.Is(context.Cause(serverContext(s)), agent.AbortShutdown)
}

// HTTP requests and traffic callbacks are external producers. Their admission
// must stop before Wait; work they spawn inherits their nonzero WaitGroup count.
func (s *Runtime) beginExternalWork() bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.closing {
		return false
	}
	s.backgroundWG.Add(1)
	return true
}

func (s *Server) withRequestLifetime(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.beginExternalWork() {
			writeErr(w, http.StatusServiceUnavailable, "服务正在关闭")
			return
		}
		defer s.backgroundWG.Done()
		if s.ctx != nil {
			ctx, cancel := context.WithCancel(r.Context())
			stop := context.AfterFunc(s.ctx, cancel)
			defer stop()
			defer cancel()
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// Stop producers before Manager closes their database and file stores.
func (s *Runtime) Close() {
	if s != nil {
		s.closeOnce.Do(func() {
			s.lifecycleMu.Lock()
			s.closing = true
			s.lifecycleMu.Unlock()
			if s.engine != nil {
				s.engine.close(s.cancel)
			} else if s.cancel != nil {
				s.cancel()
			}
			s.archiveWG.Wait()
			s.proxyPoolWG.Wait()
			s.backgroundWG.Wait()
			if s.side != nil {
				s.flushSideSnapshots()
			}
			if s.stopLogWriter != nil {
				s.stopLogWriter()
			}
		})
	}
}
