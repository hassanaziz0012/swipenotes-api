package shared

import (
	"fmt"
	"strings"
	"time"
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
// Types
// =============================================================================

// AIExtractionRequest represents the incoming request body
type AIExtractionRequest struct {
	Content          string   `json:"content"`
	ExistingTags     []string `json:"existing_tags"`
	ExistingProjects []string `json:"existing_projects"`
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
// AI Extraction Prompt
// =============================================================================

// AIExtractionPrompt generates the prompt for extracting insights from a note
func AIExtractionPrompt(existingTags []string, existingProjects []string, noteContent string) string {
	tagsStr := "(none)"
	if len(existingTags) > 0 {
		tagsStr = strings.Join(existingTags, ", ")
	}
	projectsStr := "(none)"
	if len(existingProjects) > 0 {
		projectsStr = strings.Join(existingProjects, ", ")
	}
	return fmt.Sprintf(`Extract 3-7 key insights from this note as separate cards.

Requirements:
- Each card: 50-200 words
- Self-contained and understandable alone
- Preserve important details, quotes, data
- Keep markdown formatting
- Suggest relevant tags from existing list when applicable, otherwise suggest new tags.
- Tag names will be in the format: "tag-name" (lowercase; no spaces; use dashes to separate words).
- Suggest a relevant project from existing list when applicable

Existing tags: %s
Existing projects: %s

Note content:
%s

Return JSON:
{
  "cards": [
    {
      "content": "card content in markdown",
      "suggested_tags": ["tag1", "tag2"],
      "suggested_project": "project name or null"
    }
  ]
}`, tagsStr, projectsStr, noteContent)
}
