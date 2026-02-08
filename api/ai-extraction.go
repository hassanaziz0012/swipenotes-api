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
	"github.com/redis/go-redis/v9"
)

// Handler is the Vercel serverless function handler for /api/ai-extraction
func Handler(w http.ResponseWriter, r *http.Request) {
	if !validateMethod(w, r) {
		return
	}

	redisClient := getRedisClient(w)
	if redisClient == nil {
		return
	}

	allowed, clientCount, globalCount := checkRateLimits(w, r, redisClient)
	if !allowed {
		return
	}

	req := parseRequest(w, r)
	if req == nil {
		return
	}

	prompt := shared.AIExtractionPrompt(req.ExistingTags, req.ExistingProjects, req.Content)
	geminiBody := createGeminiPayload(w, prompt)
	if geminiBody == nil {
		return
	}

	resp, respBody := executeGeminiCall(w, geminiBody)
	if resp == nil {
		return
	}
	defer resp.Body.Close()

	if !handleGeminiResponse(w, resp, respBody) {
		return
	}

	incrementLimits(redisClient, r)
	writeSuccessResponse(w, respBody, clientCount, globalCount)
}

func validateMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Method not allowed"})
		return false
	}
	return true
}

func getRedisClient(w http.ResponseWriter) *redis.Client {
	client, err := shared.GetRedisClient()
	if err != nil {
		log.Printf("Redis initialization error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Internal server error"})
		return nil
	}
	return client
}

func checkRateLimits(w http.ResponseWriter, r *http.Request, client *redis.Client) (bool, int64, int64) {
	clientIP := shared.GetClientIP(r)
	allowed, clientCount, globalCount, err := shared.CheckRateLimit(client, clientIP)
	if err != nil {
		log.Printf("Rate limit check error: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Internal server error"})
		return false, 0, 0
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
		return false, clientCount, globalCount
	}

	return true, clientCount, globalCount
}

func parseRequest(w http.ResponseWriter, r *http.Request) *shared.AIExtractionRequest {
	var req shared.AIExtractionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Invalid request body"})
		return nil
	}

	if req.Content == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Content is required"})
		return nil
	}
	return &req
}

func createGeminiPayload(w http.ResponseWriter, prompt string) []byte {
	geminiReq := shared.GeminiArmyRequest{
		Prompt: prompt,
	}
	body, err := json.Marshal(geminiReq)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to create request"})
		return nil
	}
	return body
}

func executeGeminiCall(w http.ResponseWriter, body []byte) (*http.Response, []byte) {
	armyAccessKey := os.Getenv("ARMY_ACCESS_KEY")
	if armyAccessKey == "" {
		log.Println("ARMY_ACCESS_KEY not set")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Server configuration error"})
		return nil, nil
	}

	httpReq, err := http.NewRequest("POST", shared.GeminiArmyBaseURL+"/generate", bytes.NewBuffer(body))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to create request"})
		return nil, nil
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
		return nil, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(shared.ErrorResponse{Error: "Failed to read AI response"})
		return nil, nil
	}

	return resp, respBody
}

func handleGeminiResponse(w http.ResponseWriter, resp *http.Response, body []byte) bool {
	if resp.StatusCode == http.StatusOK {
		return true
	}

	var geminiErr struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}

	// Try parsing the error response
	if err := json.Unmarshal(body, &geminiErr); err == nil {
		// Check for specific 503 UNAVAILABLE error
		if geminiErr.Error.Code == 503 && geminiErr.Error.Status == "UNAVAILABLE" {
			log.Printf("Gemini Army API unavailable: %v", geminiErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"message": "Our AI provider upstream is currently unavailable. Please try again in a couple minutes.",
				"status":  "provider_unavailable",
			})
			return false
		}
	}

	// Fallback to original behavior
	log.Printf("Gemini Army API returned status %d: %s", resp.StatusCode, string(body))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
	return false
}

func incrementLimits(client *redis.Client, r *http.Request) {
	clientIP := shared.GetClientIP(r)
	if err := shared.IncrementRateLimit(client, clientIP); err != nil {
		log.Printf("Failed to increment rate limit: %v", err)
	}
}

func writeSuccessResponse(w http.ResponseWriter, body []byte, clientCount, globalCount int64) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RateLimit-Client-Limit", fmt.Sprintf("%d", shared.ClientRateLimitPerDay))
	w.Header().Set("X-RateLimit-Client-Remaining", fmt.Sprintf("%d", shared.ClientRateLimitPerDay-clientCount-1))
	w.Header().Set("X-RateLimit-Global-Limit", fmt.Sprintf("%d", shared.GlobalRateLimitPerDay))
	w.Header().Set("X-RateLimit-Global-Remaining", fmt.Sprintf("%d", shared.GlobalRateLimitPerDay-globalCount-1))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}
