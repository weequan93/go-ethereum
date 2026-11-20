package rpcdiag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

type requestParamsContextKey struct{}

// WithRequestParams passes the original JSON-RPC params to the concrete
// eth_getLogs implementation without exposing them to unrelated RPC methods.
func WithRequestParams(ctx context.Context, params []byte) context.Context {
	return context.WithValue(ctx, requestParamsContextKey{}, params)
}

// RequestParams returns the original JSON-RPC params attached to ctx.
func RequestParams(ctx context.Context) []byte {
	params, _ := ctx.Value(requestParamsContextKey{}).([]byte)
	return params
}

// SetRequestParams attaches the original JSON-RPC params to a request-local
// trace. The caller must provide request-owned bytes and must not mutate them
// while the trace is live. The bytes are retained without serialization work;
// they are emitted only if the completed call qualifies for detail logging.
func (t *Trace) SetRequestParams(params []byte) {
	if t == nil {
		return
	}
	t.requestParams = params
	t.hasRequestParams = true
}

// appendRequestParams adds the exact request params, a digest, and bounded-size
// status. Large params are represented by a prefix so a caller cannot make a
// slow diagnostic log unbounded; the digest still identifies the full query.
func (s Snapshot) appendRequestParams(fields []any) []any {
	if !s.HasRequestParams {
		return fields
	}
	const maxParamsLogBytes = 8192
	digest := sha256.Sum256(s.RequestParams)
	fields = append(fields,
		"request_params_sha256", hex.EncodeToString(digest[:]),
		"request_params_bytes", len(s.RequestParams),
	)
	if len(s.RequestParams) <= maxParamsLogBytes {
		return append(fields, "request_params_json", string(s.RequestParams), "request_params_truncated", false)
	}
	return append(fields,
		"request_params_json_prefix", string(s.RequestParams[:maxParamsLogBytes]),
		"request_params_truncated", true,
	)
}

