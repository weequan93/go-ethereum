package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/internal/rpcdiag"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"golang.org/x/time/rate"
)

type stageTestService struct{}

func (*stageTestService) GetLogs(ctx context.Context, value string) (any, error) {
	if trace := rpcdiag.FromContext(ctx); trace != nil {
		trace.AddTime(rpcdiag.SyncInitial, 5*time.Millisecond)
		trace.Add(rpcdiag.Results, 1)
	}
	switch value {
	case "cancel":
		return nil, ctx.Err()
	case "error":
		return nil, errors.New("private-error")
	case "marshal":
		return &MarshalErrObj{}, nil
	}
	return []string{value}, nil
}
func (s *stageTestService) SendRawTransaction(ctx context.Context, value string) (any, error) {
	return s.GetLogs(ctx, value)
}

func stageServer(t testing.TB, enabled bool) (*Server, *stageOutput) {
	t.Helper()
	s := NewServer()
	output := &stageOutput{records: make(chan stageRecord, stageQueueSize), stop: make(chan struct{})}
	s.services.stageOutput = output // A deliberately undrained sink keeps tests deterministic.
	if err := s.RegisterName("eth", &stageTestService{}); err != nil {
		t.Fatal(err)
	}
	if enabled {
		s.SetStageDiagnosticsThreshold(time.Nanosecond)
	}
	t.Cleanup(s.Stop)
	return s, output
}
func stageCall(s *Server, ctx context.Context, method string, id json.RawMessage, params string) *jsonrpcMessage {
	h := &handler{reg: &s.services, log: log.NewLogger(log.DiscardHandler())}
	return h.handleCallMsg(&callProc{ctx: ctx}, &jsonrpcMessage{Version: vsn, Method: method, ID: id, Params: json.RawMessage(params)})
}

func TestStageDiagnosticsDefaultOffAndMethodAllowlist(t *testing.T) {
	s, _ := stageServer(t, false)
	ctx := context.Background()
	got, trace, config := s.services.beginStageDiagnostics(ctx, "eth_getLogs")
	if got != ctx || trace != nil || config != nil {
		t.Fatal("default enabled")
	}
	s.SetStageDiagnosticsThreshold(time.Second)
	got, trace, config = s.services.beginStageDiagnostics(ctx, "eth_call")
	if got != ctx || trace != nil || config != nil {
		t.Fatal("unrelated method instrumented")
	}
	s.SetStageDiagnosticsThreshold(0)
	_, trace, _ = s.services.beginStageDiagnostics(ctx, "eth_getLogs")
	if trace != nil {
		t.Fatal("disable ignored")
	}
}

func TestStageDiagnosticsResponseAndErrorEquivalence(t *testing.T) {
	baseline, _ := stageServer(t, false)
	observed, output := stageServer(t, true)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		params string
		ctx    context.Context
	}{
		{`["private-request-result-value"]`, context.Background()},
		{`["error"]`, context.Background()}, {`["marshal"]`, context.Background()},
		{`["cancel"]`, canceled}, {`[123]`, context.Background()},
	} {
		first := stageCall(baseline, test.ctx, "eth_getLogs", json.RawMessage(`1`), test.params)
		second := stageCall(observed, test.ctx, "eth_getLogs", json.RawMessage(`1`), test.params)
		a, _ := json.Marshal(first)
		b, _ := json.Marshal(second)
		if !bytes.Equal(a, b) {
			t.Fatalf("response changed: %s vs %s", a, b)
		}
	}
	if len(output.records) != 5 {
		t.Fatalf("completion records=%d", len(output.records))
	}
	classes := map[int64]bool{}
	for len(output.records) > 0 {
		record := <-output.records
		classes[record.snapshot.Fields[rpcdiag.FailureKind]] = true
		encoded, _ := json.Marshal(record.snapshot.LogFields())
		for _, secret := range []string{"private-request-result-value", "private-error"} {
			if bytes.Contains(encoded, []byte(secret)) {
				t.Fatal("diagnostic payload leak")
			}
		}
	}
	for _, kind := range []int64{0, rpcdiag.FailureOther, rpcdiag.FailureEncode, rpcdiag.FailureCanceled, rpcdiag.FailureParams} {
		if !classes[kind] {
			t.Fatalf("missing error class %d", kind)
		}
	}
}

func TestStageDiagnosticsHTTPBatchAndNotifications(t *testing.T) {
	body := `[{"jsonrpc":"2.0","id":1,"method":"eth_getLogs","params":["one"]},{"jsonrpc":"2.0","method":"eth_getLogs","params":["notification"]},{"jsonrpc":"2.0","id":2,"method":"eth_sendRawTransaction","params":["two"]}]`
	var responses [][]byte
	for _, enabled := range []bool{false, true} {
		s, output := stageServer(t, enabled)
		request := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		responses = append(responses, response.Body.Bytes())
		if enabled && len(output.records) != 3 {
			t.Fatalf("batch completion accounting=%d", len(output.records))
		}
	}
	if !bytes.Equal(responses[0], responses[1]) {
		t.Fatalf("batch output/order changed: %s vs %s", responses[0], responses[1])
	}
}

func TestStageDiagnosticsThresholdCountsAreIndependentOfDetailDrops(t *testing.T) {
	c := stageCounters{metrics.NewCounter(), metrics.NewCounter(), metrics.NewCounter(), metrics.NewCounter()}
	for _, d := range []time.Duration{0, time.Second - 1, time.Second, 3*time.Second - 1, 3 * time.Second, 5*time.Second - 1, 5 * time.Second} {
		c.complete(d)
	}
	if c.total.Snapshot().Count() != 7 || c.ge1s.Snapshot().Count() != 5 || c.ge3s.Snapshot().Count() != 3 || c.ge5s.Snapshot().Count() != 1 {
		t.Fatal("threshold boundaries/accounting changed")
	}
	s, output := stageServer(t, true)
	config := s.services.stageDiagnostics.Load()
	config.limiter = rate.NewLimiter(0, 1000)
	before := config.counts[0][0].total.Snapshot().Count()
	drops := config.dropped.Snapshot().Count()
	for i := 0; i < 100; i++ {
		stageCall(s, context.Background(), "eth_getLogs", json.RawMessage(`1`), `["ok"]`)
	}
	if len(output.records) != stageQueueSize || config.dropped.Snapshot().Count()-drops != 100-stageQueueSize || config.counts[0][0].total.Snapshot().Count()-before != 100 {
		t.Fatal("bounded sink changed denominator or blocked producer")
	}
}

func TestStageDiagnosticsConcurrentUpdateShutdownAndInFlightCompletion(t *testing.T) {
	s, _ := stageServer(t, true)
	ctx, trace, config := s.services.beginStageDiagnostics(context.Background(), "eth_getLogs")
	if rpcdiag.FromContext(ctx) != trace {
		t.Fatal("missing trace")
	}
	before := config.counts[0][0].total.Snapshot().Count()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.SetStageDiagnosticsThreshold(time.Duration(j % 2))
				stageCall(s, context.Background(), "eth_getLogs", json.RawMessage(`1`), `["ok"]`)
			}
		}()
	}
	wg.Wait()
	s.Stop()
	s.services.finishStageDiagnostics(trace, config, "eth_getLogs", false)
	if config.counts[0][0].total.Snapshot().Count() <= before {
		t.Fatal("in-flight completion lost after disable/stop")
	}
	s.SetStageDiagnosticsThreshold(time.Second)
	if s.services.stageDiagnostics.Load() != nil {
		t.Fatal("stopped server re-enabled")
	}
}

func TestStageDiagnosticsOutputWorkerAndStop(t *testing.T) {
	output := &stageOutput{records: make(chan stageRecord, 1), stop: make(chan struct{})}
	written := make(chan stageRecord, 1)
	done := make(chan struct{})
	go func() { defer close(done); output.run(func(r stageRecord) { written <- r }) }()
	output.records <- stageRecord{method: "eth_getLogs"}
	select {
	case record := <-written:
		if record.method != "eth_getLogs" {
			t.Fatal("wrong record")
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not drain")
	}
	close(output.stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func BenchmarkStageDiagnosticsRPC(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "Disabled"
		if enabled {
			name = "Enabled"
		}
		b.Run(name, func(b *testing.B) {
			s, _ := stageServer(b, enabled)
			if enabled {
				s.SetStageDiagnosticsThreshold(time.Hour)
			}
			h := &handler{reg: &s.services, log: log.NewLogger(log.DiscardHandler())}
			msg := &jsonrpcMessage{Version: vsn, Method: "eth_getLogs", ID: json.RawMessage(`1`), Params: json.RawMessage(`["ok"]`)}
			cp := &callProc{ctx: context.Background()}
			b.ReportAllocs()
			for b.Loop() {
				response := h.handleCall(cp, msg)
				if response.Error != nil {
					b.Fatal(response.Error)
				}
			}
		})
	}
}

func TestStageDiagnosticsDisabledResources(t *testing.T) {
	s := NewServer()
	defer s.Stop()
	ctx := context.Background()
	before := metrics.DefaultRegistry.Get("rpc/diagnostics/eth_getLogs/success/completed")
	for _, threshold := range []time.Duration{0, -time.Second} {
		s.SetStageDiagnosticsThreshold(threshold)
		if s.services.stageOutput != nil || s.services.stageDiagnostics.Load() != nil {
			t.Fatal("disabled diagnostics allocated output/limiter/config")
		}
		if got := testing.AllocsPerRun(1000, func() {
			callCtx, trace, config := s.services.beginStageDiagnostics(ctx, "eth_getLogs")
			if callCtx != ctx || trace != nil || config != nil || !trace.Start().IsZero() {
				panic("disabled stage diagnostics created a trace/timer")
			}
		}); got != 0 {
			t.Fatalf("disabled stage allocations: %v", got)
		}
	}
	if metrics.DefaultRegistry.Get("rpc/diagnostics/eth_getLogs/success/completed") != before {
		t.Fatal("disabled diagnostics registered a counter")
	}
}
