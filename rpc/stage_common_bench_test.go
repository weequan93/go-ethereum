package rpc

import (
	"context"
	"encoding/json"
	"testing"
)

type stageCommonService struct{}

func (*stageCommonService) GetLogs(ctx context.Context, value string) string { return value }

// This identical benchmark also compiles against the preserved pre-diagnostic
// RPC source overlay; it never enables diagnostics in either version.
func BenchmarkStageRPCCommon(b *testing.B) {
	s := NewServer()
	defer s.Stop()
	if err := s.RegisterName("eth", &stageCommonService{}); err != nil {
		b.Fatal(err)
	}
	h := &handler{reg: &s.services}
	msg := &jsonrpcMessage{Version: vsn, Method: "eth_getLogs", ID: json.RawMessage(`1`), Params: json.RawMessage(`["ok"]`)}
	cp := &callProc{ctx: context.Background()}
	b.ReportAllocs()
	for b.Loop() {
		if response := h.handleCall(cp, msg); response.Error != nil {
			b.Fatal(response.Error)
		}
	}
}
