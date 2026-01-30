package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

// =============================================================================
// Constants
// =============================================================================

// Rate limiting constants - easily modifiable
const (
	ClientRateLimitPerDay = 5  // Maximum requests per client (IP) per day
	GlobalRateLimitPerDay = 50 // Maximum total requests per day across all clients
	RateLimitTTL          = 24 * time.Hour
)

// Gemini Army API
const GeminiArmyBaseURL = "https://gemini-army.vercel.app"

// =============================================================================
// AI Extraction Prompt
// =============================================================================

// AIExtractionPrompt generates the prompt for extracting insights from a note
func AIExtractionPrompt(existingTags []string, noteContent string) string {
	tagsStr := "(none)"
	if len(existingTags) > 0 {
		tagsStr = strings.Join(existingTags, ", ")
	}
	return fmt.Sprintf(`Extract 3-7 key insights from this note as separate cards.

Requirements:
- Each card: 50-200 words
- Self-contained and understandable alone
- Preserve important details, quotes, data
- Keep markdown formatting
- Suggest relevant tags from existing list when applicable

Existing tags: %s

Note content:
%s

Return JSON:
{
  "cards": [
    {
      "content": "card content in markdown",
      "suggested_tags": ["tag1", "tag2"]
    }
  ]
}`, tagsStr, noteContent)
}

// =============================================================================
// Types
// =============================================================================

// AIExtractionRequest represents the incoming request body
type AIExtractionRequest struct {
	Content      string   `json:"content"`
	ExistingTags []string `json:"existing_tags"`
}

// GeminiArmyRequest represents the request to Gemini Army API
type GeminiArmyRequest struct {
	Prompt string `json:"prompt"`
}

// UsageMetadata represents token usage from Gemini
type UsageMetadata struct {
	PromptTokenCount     int `json:"prompt_token_count"`
	CandidatesTokenCount int `json:"candidates_token_count"`
	TotalTokenCount      int `json:"total_token_count"`
}

// AIExtractionResponse represents the response from this API
type AIExtractionResponse struct {
	Text          string         `json:"text"`
	Model         string         `json:"model"`
	UsageMetadata *UsageMetadata `json:"usage_metadata,omitempty"`
	FinishReason  string         `json:"finish_reason,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error"`
}

// =============================================================================
// Redis Client
// =============================================================================

var redisClient *redis.Client
var ctx = context.Background()

func initRedis() error {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		return fmt.Errorf("REDIS_URL environment variable is not set")
	}

	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("failed to parse REDIS_URL: %w", err)
	}

	redisClient = redis.NewClient(opt)

	// Test connection
	_, err = redisClient.Ping(ctx).Result()
	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	log.Println("Connected to Redis successfully")
	return nil
}

// =============================================================================
// Rate Limiting
// =============================================================================

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
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

// checkRateLimit checks both client and global rate limits
// Returns (allowed bool, clientCount int64, globalCount int64, error)
func checkRateLimit(clientIP string) (bool, int64, int64, error) {
	today := getTodayKey()
	clientKey := fmt.Sprintf("ratelimit:client:%s:%s", clientIP, today)
	globalKey := fmt.Sprintf("ratelimit:global:%s", today)

	// Get current counts
	clientCount, err := redisClient.Get(ctx, clientKey).Int64()
	if err != nil && err != redis.Nil {
		return false, 0, 0, err
	}

	globalCount, err := redisClient.Get(ctx, globalKey).Int64()
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

// incrementRateLimit increments both client and global counters
func incrementRateLimit(clientIP string) error {
	today := getTodayKey()
	clientKey := fmt.Sprintf("ratelimit:client:%s:%s", clientIP, today)
	globalKey := fmt.Sprintf("ratelimit:global:%s", today)

	pipe := redisClient.Pipeline()

	// Increment client counter
	pipe.Incr(ctx, clientKey)
	pipe.Expire(ctx, clientKey, RateLimitTTL)

	// Increment global counter
	pipe.Incr(ctx, globalKey)
	pipe.Expire(ctx, globalKey, RateLimitTTL)

	_, err := pipe.Exec(ctx)
	return err
}

// =============================================================================
// Handlers
// =============================================================================

func aiExtractionHandler(w http.ResponseWriter, r *http.Request) {
	// Only allow POST
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Method not allowed"})
		return
	}

	// Get client IP and check rate limits
	clientIP := getClientIP(r)
	allowed, clientCount, globalCount, err := checkRateLimit(clientIP)
	if err != nil {
		log.Printf("Rate limit check error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Internal server error"})
		return
	}

	if !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Client-Limit", fmt.Sprintf("%d", ClientRateLimitPerDay))
		w.Header().Set("X-RateLimit-Client-Remaining", "0")
		w.Header().Set("X-RateLimit-Global-Limit", fmt.Sprintf("%d", GlobalRateLimitPerDay))
		w.WriteHeader(http.StatusTooManyRequests)

		if clientCount >= ClientRateLimitPerDay {
			json.NewEncoder(w).Encode(ErrorResponse{Error: "Client rate limit exceeded. Maximum 5 requests per day."})
		} else {
			json.NewEncoder(w).Encode(ErrorResponse{Error: "Global rate limit exceeded. Please try again later."})
		}
		return
	}

	// Parse request body
	var req AIExtractionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Invalid request body"})
		return
	}

	// Validate required fields
	if req.Content == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Content is required"})
		return
	}

	// Generate prompt
	prompt := AIExtractionPrompt(req.ExistingTags, req.Content)

	// Call Gemini Army API
	geminiReq := GeminiArmyRequest{
		Prompt: prompt,
	}
	geminiBody, err := json.Marshal(geminiReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Failed to create request"})
		return
	}

	armyAccessKey := os.Getenv("ARMY_ACCESS_KEY")
	if armyAccessKey == "" {
		log.Println("ARMY_ACCESS_KEY not set")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Server configuration error"})
		return
	}

	httpReq, err := http.NewRequest("POST", GeminiArmyBaseURL+"/generate", bytes.NewBuffer(geminiBody))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Failed to create request"})
		return
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", armyAccessKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		log.Printf("Gemini Army API error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Failed to call AI service"})
		return
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "Failed to read AI response"})
		return
	}

	// Check if Gemini Army returned an error
	if resp.StatusCode != http.StatusOK {
		log.Printf("Gemini Army API returned status %d: %s", resp.StatusCode, string(respBody))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
		return
	}

	// Increment rate limit counters (only after successful request)
	if err := incrementRateLimit(clientIP); err != nil {
		log.Printf("Failed to increment rate limit: %v", err)
		// Don't fail the request, just log the error
	}

	// Return the Gemini Army response directly
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Client-Limit", fmt.Sprintf("%d", ClientRateLimitPerDay))
	w.Header().Set("X-RateLimit-Client-Remaining", fmt.Sprintf("%d", ClientRateLimitPerDay-clientCount-1))
	w.Header().Set("X-RateLimit-Global-Limit", fmt.Sprintf("%d", GlobalRateLimitPerDay))
	w.Header().Set("X-RateLimit-Global-Remaining", fmt.Sprintf("%d", GlobalRateLimitPerDay-globalCount-1))
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

// =============================================================================
// Main
// =============================================================================

func main() {
	// Load .env file
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Initialize Redis
	if err := initRedis(); err != nil {
		log.Fatalf("Failed to initialize Redis: %v", err)
	}
	defer redisClient.Close()

	// Set up routes
	http.HandleFunc("/ai-extraction", aiExtractionHandler)
	http.HandleFunc("/health", healthHandler)

	// Get port
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server starting on port %s", port)
	log.Printf("Rate limits: %d requests/client/day, %d requests/global/day", ClientRateLimitPerDay, GlobalRateLimitPerDay)

	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
