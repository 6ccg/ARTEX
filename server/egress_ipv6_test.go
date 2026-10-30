package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeEgressIPv6RejectsIPv4Echo(t *testing.T) {
	t.Setenv("ARTEX_EGRESS_URL", "")
	for _, ip := range []string{"2001:db8::1", "192.0.2.1"} {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, ip) }))
		result := probeEgressIPv6(context.Background(), proxy.URL, []string{"http://probe.invalid"})
		proxy.Close()
		if result.OK != (ip == "2001:db8::1") {
			t.Fatalf("IPv6 echo %s: %+v", ip, result)
		}
	}
}

func TestJudgeEgressAcceptsIPv6OnlyProxy(t *testing.T) {
	for _, ipv6OK := range []bool{true, false} {
		report := egressReport{Checks: []egressCheck{
			{Key: "direct", Result: egressProbeResult{OK: true}},
			{Key: "global", Result: egressProbeResult{OK: false}},
			{Key: "global_ipv6", Result: egressProbeResult{OK: ipv6OK}},
		}}
		if judgeEgress(report) != ipv6OK {
			t.Fatal("direct result overrode proxy IPv6 result")
		}
	}
}
