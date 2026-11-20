package exp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/metrics"
)

func TestWriteOriginHandler(t *testing.T) {
	registry := metrics.NewRegistry()
	handler := writeOriginHandler(registry)
	request := httptest.NewRequest(http.MethodGet, "/debug/metrics/pebble-write-origin", nil)
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, request)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("disabled instrumentation: got status %d", missing.Code)
	}
	for _, source := range []string{"compaction", "flush", "wal", "manifest", "other"} {
		counter := metrics.GetOrRegisterCounter("l2chaindata/write/origin/"+source+"/bytes", registry)
		if source == "flush" {
			counter.Inc(128)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body struct {
		Generation string
		Bytes      map[string]int64
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Generation == "" || len(body.Bytes) != 5 ||
		body.Bytes["flush"] != 128 || body.Bytes["compaction"] != 0 {
		t.Fatalf("invalid sample: %s", response.Body.String())
	}
}
