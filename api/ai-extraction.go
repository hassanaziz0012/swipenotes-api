package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hassanaziz0012/swipenotes-api/pkg/shared"
)

// Handler is the Vercel serverless function handler for /api/ai-extraction
func Handler(w http.ResponseWriter, r *http.Request) {
	// Only allow POST
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Method not allowed"})
		return
	}

	// Get Redis client
	redisClient, err := shared.GetRedisClient()
	if err != nil {
		log.Printf("Redis initialization error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Internal server error"})
		return
	}

	// Get client IP and check rate limits
	clientIP := shared.GetClientIP(r)
	allowed, clientCount, globalCount, err := shared.CheckRateLimit(redisClient, clientIP)
	if err != nil {
		log.Printf("Rate limit check error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Internal server error"})
		return
	}

	if !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Client-Limit", fmt.Sprintf("%d", shared.ClientRateLimitPerDay))
		w.Header().Set("X-RateLimit-Client-Remaining", "0")
		w.Header().Set("X-RateLimit-Global-Limit", fmt.Sprintf("%d", shared.GlobalRateLimitPerDay))
		w.WriteHeader(http.StatusTooManyRequests)

		if clientCount >= shared.ClientRateLimitPerDay {
			json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Client rate limit exceeded. Maximum 5 requests per day."})
		} else {
			json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Global rate limit exceeded. Please try again later."})
		}
		return
	}

	// Parse request body
	var req shared.AIExtractionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Invalid request body"})
		return
	}

	// Validate required fields
	if req.Content == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Content is required"})
		return
	}

	// Generate prompt
	prompt := shared.AIExtractionPrompt(req.ExistingTags, req.ExistingProjects, req.Content)

	// Call Gemini Army API
	geminiReq := shared.GeminiArmyRequest{
		Prompt: prompt,
	}
	geminiBody, err := json.Marshal(geminiReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to create request"})
		return
	}

	armyAccessKey := os.Getenv("ARMY_ACCESS_KEY")
	if armyAccessKey == "" {
		log.Println("ARMY_ACCESS_KEY not set")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Server configuration error"})
		return
	}

	httpReq, err := http.NewRequest("POST", shared.GeminiArmyBaseURL+"/generate", bytes.NewBuffer(geminiBody))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to create request"})
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
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to call AI service"})
		return
	}
	defer resp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to read AI response"})
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
	if err := shared.IncrementRateLimit(redisClient, clientIP); err != nil {
		log.Printf("Failed to increment rate limit: %v", err)
		// Don't fail the request, just log the error
	}

	// Return the Gemini Army response directly
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Client-Limit", fmt.Sprintf("%d", shared.ClientRateLimitPerDay))
	w.Header().Set("X-RateLimit-Client-Remaining", fmt.Sprintf("%d", shared.ClientRateLimitPerDay-clientCount-1))
	w.Header().Set("X-RateLimit-Global-Limit", fmt.Sprintf("%d", shared.GlobalRateLimitPerDay))
	w.Header().Set("X-RateLimit-Global-Remaining", fmt.Sprintf("%d", shared.GlobalRateLimitPerDay-globalCount-1))
	w.WriteHeader(http.StatusOK)
	w.Write(respBody)
}
