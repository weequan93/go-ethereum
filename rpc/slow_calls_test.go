package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

func TestSlowCallThresholdAndRateLimit(t *testing.T) {
	s := NewServer()
	if s.services.reportSlowSuccess(time.Hour) {
		t.Fatal("logging enabled by default")
	}
	s.SetSlowCallThreshold(time.Second)
	if s.services.reportSlowSuccess(time.Second - time.Nanosecond) {
		t.Fatal("fast call logged")
	}
	config := s.services.slowCallLog.Load()
	config.limiter.SetLimit(0) // no refill: deterministically check the shared burst.
	logged := 0
	for i := 0; i < 100; i++ {
		if s.services.reportSlowSuccess(time.Second) {
			logged++
		}
	}
	if logged != 10 {
		t.Fatalf("logged %d calls, want burst10", logged)
	}
	s.SetSlowCallThreshold(0)
	if s.services.reportSlowSuccess(time.Hour) {
		t.Fatal("disable ignored")
	}
}

func TestSlowCallResultAndPayloadPrivacy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := newTestServer()
		if enabled {
			s.SetSlowCallThreshold(time.Nanosecond)
		}
		var output bytes.Buffer
		h := &handler{reg: &s.services, log: log.NewLogger(log.JSONHandlerWithLevel(&output, slog.LevelInfo))}
		secret := "private-request-and-result-value"
		params, _ := json.Marshal([]any{secret, 7, nil})
		msg := &jsonrpcMessage{Version: vsn, ID: json.RawMessage(`"` + strings.Repeat("large-id", 100) + `"`), Method: "test_echo", Params: params}
		response := h.handleCallMsg(&callProc{ctx: context.Background()}, msg)
		if response.Error != nil || !bytes.Contains(response.Result, []byte(secret)) {
			t.Fatalf("RPC result changed: %s", response.Result)
		}
		if bytes.Contains(output.Bytes(), []byte(secret)) || bytes.Contains(output.Bytes(), []byte(strings.Repeat("large-id", 100))) {
			t.Fatal("timing log exposed payload or unbounded ID")
		}
		if enabled {
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["outcome"] != "success" || entry["msg"] != "Served test_echo" {
				t.Fatalf("unexpected timing record: %v", entry)
			}
		} else if output.Len() != 0 {
			t.Fatal("default log level changed")
		}
	}
}

func TestSlowCallThresholdConcurrentUpdate(t *testing.T) {
	s := NewServer()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				s.SetSlowCallThreshold(time.Duration(j % 2))
				s.services.reportSlowSuccess(time.Second)
			}
		}()
	}
	wg.Wait()
}

func TestSlowCallDisabledAllocations(t *testing.T) {
	s := NewServer()
	defer s.Stop()
	for _, threshold := range []time.Duration{0, -time.Second} {
		s.SetSlowCallThreshold(threshold)
		if s.services.slowCallLog.Load() != nil {
			t.Fatal("disabled slow logging allocated a limiter/config")
		}
		if got := testing.AllocsPerRun(1000, func() {
			if s.services.reportSlowSuccess(time.Hour) {
				panic("disabled slow logging reported a call")
			}
		}); got != 0 {
			t.Fatalf("disabled slow logging allocations: %v", got)
		}
	}
}
