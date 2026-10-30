package filtermaps

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/internal/rpcdiag"
)

func BenchmarkSyncLogIndexWaitDiagnostics(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "Disabled"
		if enabled {
			name = "Enabled"
		}
		b.Run(name, func(b *testing.B) {
			f := &FilterMaps{matcherSyncCh: make(chan *FilterMapsMatcherBackend), disabledCh: make(chan struct{})}
			fm := &FilterMapsMatcherBackend{f: f}
			done := make(chan struct{})
			go func() {
				defer close(done)
				for request := range f.matcherSyncCh {
					request.syncCh <- SyncRange{}
				}
			}()
			b.Cleanup(func() { close(f.matcherSyncCh); <-done })
			b.ReportAllocs()
			for b.Loop() {
				ctx := context.Background()
				if enabled {
					ctx, _ = rpcdiag.New(ctx)
				}
				if _, err := fm.SyncLogIndex(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
