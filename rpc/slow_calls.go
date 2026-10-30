// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package rpc

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"golang.org/x/time/rate"
)

type slowCallLog struct {
	threshold time.Duration
	limiter   *rate.Limiter
}

// SetSlowCallThreshold enables timing-only warnings for successful calls taking
// at least threshold. A non-positive threshold disables them. Reporting is
// limited across all connections to this server to one record/second with a
// burst of ten; request execution, deadlines and returned values are unchanged.
func (s *Server) SetSlowCallThreshold(threshold time.Duration) {
	var config *slowCallLog
	if threshold > 0 {
		config = &slowCallLog{threshold: threshold, limiter: rate.NewLimiter(1, 10)}
	}
	s.services.slowCallLog.Store(config)
}

func (r *serviceRegistry) reportSlowSuccess(duration time.Duration) bool {
	config := r.slowCallLog.Load()
	return config != nil && duration >= config.threshold && config.limiter.Allow()
}

func slowCallID(id json.RawMessage) string {
	if len(id) <= 128 {
		return (idForLog{id}).String()
	}
	// Arbitrarily large IDs must not turn the timing log into a payload log.
	digest := sha256.Sum256(id)
	return fmt.Sprintf("sha256:%x", digest)
}
