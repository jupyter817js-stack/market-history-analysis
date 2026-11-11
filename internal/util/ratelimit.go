package util

import (
	"context"
	"time"
)

type TokenBucket struct {
	rate    float64
	tokens  chan struct{}
	stop    chan struct{}
	stopped bool
}

func NewTokenBucket(perSecond float64, burst int) *TokenBucket {
	if burst <= 0 {
		burst = 1
	}
	if perSecond <= 0 {
		perSecond = 1
	}
	tb := &TokenBucket{
		rate:   perSecond,
		tokens: make(chan struct{}, burst),
		stop:   make(chan struct{}),
	}
	for i := 0; i < burst; i++ {
		tb.tokens <- struct{}{}
	}
	go tb.refill()
	return tb
}

func (tb *TokenBucket) refill() {
	interval := time.Duration(float64(time.Second) / tb.rate)
	if interval <= 0 {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-tb.stop:
			return
		case <-t.C:
			select {
			case tb.tokens <- struct{}{}:
			default:
			}
		}
	}
}

func (tb *TokenBucket) Wait(ctx context.Context) error {
	select {
	case <-tb.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (tb *TokenBucket) Close() {
	if tb.stopped {
		return
	}
	tb.stopped = true
	close(tb.stop)
}
