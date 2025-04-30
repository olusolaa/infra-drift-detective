package util

import (
	"context"
	"sync"

	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"golang.org/x/time/rate"
)

const (
	defaultRateLimitRPS = 20
	minRateLimitRPS     = 1
	maxRateLimitRPS     = 100
)

var (
	apiLimiter  *rate.Limiter
	limiterOnce sync.Once
)

func InitializeLimiter(rps int, logger ports.Logger) {
	limiterOnce.Do(func() {
		limitValue := defaultRateLimitRPS
		if rps >= minRateLimitRPS && rps <= maxRateLimitRPS {
			limitValue = rps
		} else if rps != 0 {
			logger.Warnf(nil, "Invalid AWS API RPS configured (%d), using default %d RPS. Valid range: %d-%d.", rps, defaultRateLimitRPS, minRateLimitRPS, maxRateLimitRPS)
		}
		limit := rate.Limit(limitValue)
		apiLimiter = rate.NewLimiter(limit, limitValue)
		logger.Infof(nil, "Initialized global AWS API rate limiter: %d RPS", limitValue)
	})
}

func Wait(ctx context.Context, logger ports.Logger) error {
	if apiLimiter == nil {
		logger.Errorf(ctx, nil, "AWS API rate limiter accessed before initialization, initializing with default")
		InitializeLimiter(defaultRateLimitRPS, logger)
	}
	err := apiLimiter.Wait(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logger.Warnf(ctx, "Error waiting for AWS API rate limiter: %v", err)
		}
		return err
	}
	return nil
}
