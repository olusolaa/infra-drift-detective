package limiter

import (
	"context"
	"fmt"
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
	rpsUsed     int = defaultRateLimitRPS
)

func Initialize(rps int, logger ports.Logger) {
	limiterOnce.Do(func() {
		limitValue := defaultRateLimitRPS
		logMsg := "Initializing global AWS API rate limiter"
		if rps >= minRateLimitRPS && rps <= maxRateLimitRPS {
			limitValue = rps
			logMsg = fmt.Sprintf("%s with configured rate", logMsg)
		} else if rps != 0 {
			logger.Warnf(nil, "Invalid AWS API RPS configured (%d), using default %d RPS. Valid range: %d-%d.", rps, defaultRateLimitRPS, minRateLimitRPS, maxRateLimitRPS)
			logMsg = fmt.Sprintf("%s with default rate (invalid config)", logMsg)
		} else {
			logMsg = fmt.Sprintf("%s with default rate", logMsg)
		}

		limit := rate.Limit(limitValue)
		apiLimiter = rate.NewLimiter(limit, limitValue)
		rpsUsed = limitValue
		logger.Infof(nil, "%s: %d RPS", logMsg, limitValue)
	})
}

func Wait(ctx context.Context, logger ports.Logger) error {
	if apiLimiter == nil {
		logger.Errorf(ctx, nil, "FATAL: AWS API rate limiter accessed before initialization.")
		Initialize(defaultRateLimitRPS, logger)
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
