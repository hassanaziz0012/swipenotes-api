package shared

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// =============================================================================
// Redis Client (Singleton)
// =============================================================================

var (
	redisClient *redis.Client
	redisOnce   sync.Once
	redisErr    error
	ctx         = context.Background()
)

// GetRedisClient returns the singleton Redis client, initializing it if needed
func GetRedisClient() (*redis.Client, error) {
	redisOnce.Do(func() {
		redisURL := os.Getenv("REDIS_URL")
		if redisURL == "" {
			redisErr = fmt.Errorf("REDIS_URL environment variable is not set")
			return
		}

		opt, err := redis.ParseURL(redisURL)
		if err != nil {
			redisErr = fmt.Errorf("failed to parse REDIS_URL: %w", err)
			return
		}

		redisClient = redis.NewClient(opt)

		// Test connection
		_, err = redisClient.Ping(ctx).Result()
		if err != nil {
			redisErr = fmt.Errorf("failed to connect to Redis: %w", err)
			return
		}

		log.Println("Connected to Redis successfully")
	})

	return redisClient, redisErr
}

// =============================================================================
// Rate Limiting
// =============================================================================

// GetClientIP extracts the client IP from the request
func GetClientIP(r *http.Request) string {
	// Check X-Forwarded-For header first (for proxies)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP in the list
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP header
	xri := r.Header.Get("X-Real-IP")
	if xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	// Remove port if present
	if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
		ip = ip[:colonIdx]
	}
	return ip
}

// getTodayKey returns the date string for today (UTC)
func getTodayKey() string {
	return time.Now().UTC().Format("2006-01-02")
}

// CheckRateLimit checks both client and global rate limits
// Returns (allowed bool, clientCount int64, globalCount int64, error)
func CheckRateLimit(client *redis.Client, clientIP string) (bool, int64, int64, error) {
	today := getTodayKey()
	clientKey := fmt.Sprintf("ratelimit:client:%s:%s", clientIP, today)
	globalKey := fmt.Sprintf("ratelimit:global:%s", today)

	// Get current counts
	clientCount, err := client.Get(ctx, clientKey).Int64()
	if err != nil && err != redis.Nil {
		return false, 0, 0, err
	}

	globalCount, err := client.Get(ctx, globalKey).Int64()
	if err != nil && err != redis.Nil {
		return false, 0, 0, err
	}

	// Check limits
	if clientCount >= ClientRateLimitPerDay {
		return false, clientCount, globalCount, nil
	}
	if globalCount >= GlobalRateLimitPerDay {
		return false, clientCount, globalCount, nil
	}

	return true, clientCount, globalCount, nil
}

// IncrementRateLimit increments both client and global counters
func IncrementRateLimit(client *redis.Client, clientIP string) error {
	today := getTodayKey()
	clientKey := fmt.Sprintf("ratelimit:client:%s:%s", clientIP, today)
	globalKey := fmt.Sprintf("ratelimit:global:%s", today)

	pipe := client.Pipeline()

	// Increment client counter
	pipe.Incr(ctx, clientKey)
	pipe.Expire(ctx, clientKey, RateLimitTTL)

	// Increment global counter
	pipe.Incr(ctx, globalKey)
	pipe.Expire(ctx, globalKey, RateLimitTTL)

	_, err := pipe.Exec(ctx)
	return err
}
