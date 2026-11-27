package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"boggle-api/internal/misclue/content"
	"boggle-api/internal/misclue/llm"

	"github.com/gin-gonic/gin"
)

type Handler struct {
	llm *llm.Client
}

func New(llm *llm.Client) *Handler { return &Handler{llm: llm} }

type PlayerState struct {
	Seat        int    `json:"seat"`
	Name        string `json:"name"`
	Alive       bool   `json:"alive"`
	IsAI        bool   `json:"isAi"`
	CharacterID string `json:"characterId,omitempty"`
}

type ClueState struct {
	Seat int    `json:"seat"`
	Name string `json:"name"`
	Text string `json:"text"`
}

type EliminatedState struct {
	Seat int    `json:"seat"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type PublicState struct {
	Players    []PlayerState     `json:"players"`
	Clues      []ClueState       `json:"clues"`
	Eliminated []EliminatedState `json:"eliminated"`
}

type PrivateState struct {
	Role string `json:"role"`
	Word string `json:"word"`
}

type Rules struct {
	MaxClueLength int `json:"maxClueLength"`
}

type TurnRequest struct {
	Action       string       `json:"action"`
	CharacterID  string       `json:"characterId"`
	Seat         int          `json:"seat"`
	PublicState  PublicState  `json:"publicState"`
	PrivateState PrivateState `json:"privateState"`
	Rules        Rules        `json:"rules"`
}

type TurnResponse struct {
	OK         bool   `json:"ok"`
	Action     string `json:"action"`
	Clue       string `json:"clue,omitempty"`
	TargetSeat int    `json:"targetSeat,omitempty"`
	Guess      string `json:"guess,omitempty"`
	Fallback   bool   `json:"fallback,omitempty"`
	Error      string `json:"error,omitempty"`
}

func (h *Handler) Turn(c *gin.Context) {
	var req TurnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req.Action = strings.TrimSpace(strings.ToLower(req.Action))
	req.CharacterID = strings.TrimSpace(strings.ToLower(req.CharacterID))
	if req.Rules.MaxClueLength <= 0 || req.Rules.MaxClueLength > 60 {
		req.Rules.MaxClueLength = 32
	}
	char, ok := content.ByID(req.CharacterID)
	if !ok {
		char = content.Character{ID: req.CharacterID, Name: "Agent", Persona: "You are a careful Misclue player."}
	}
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
		started := time.Now()
		resp, err := h.generate(ctx, char, req)
		elapsed := time.Since(started)
		cancel()
		if err == nil && validate(req, resp) {
			resp.OK = true
			resp.Action = req.Action
			c.JSON(http.StatusOK, resp)
			return
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("invalid_response")
		}
		log.Printf("misclue ai attempt failed: action=%s character=%s seat=%d model=%s attempt=%d elapsed=%s err=%v", req.Action, req.CharacterID, req.Seat, h.llm.Model(), attempt, elapsed.Round(time.Millisecond), lastErr)
	}

	c.JSON(http.StatusBadGateway, TurnResponse{
		OK:     false,
		Action: req.Action,
		Error:  "AI turn failed. Please retry.",
	})
}

func (h *Handler) generate(ctx context.Context, char content.Character, req TurnRequest) (TurnResponse, error) {
	system := buildSystem(char, req)
	user := buildUser(req)
	raw, err := h.llm.CompleteJSON(ctx, system, user)
	if err != nil {
		return TurnResponse{}, err
	}
	obj, err := extractJSON(raw)
	if err != nil {
		return TurnResponse{}, err
	}
	return parseAction(req.Action, obj)
}

func buildSystem(char content.Character, req TurnRequest) string {
	return fmt.Sprintf(`You are playing Misclue, a social deduction word game.
Character persona: %s

Rules:
- Roles: Natives share the same secret word. Tourists have a similar but different word. Drifter has NO word and must bluff.
- You must only use information you are given. Never invent hidden facts.
- Return ONLY valid JSON. No markdown, no explanation outside JSON.
- Clues must be short (1-4 words), must not contain the secret word, must not be empty.
- Vote target must be an alive seat number that is not yourself.
- Drifter guess should be a short word/phrase guess of the Native word.
`, char.Persona)
}

func buildUser(req TurnRequest) string {
	publicJSON, _ := json.Marshal(req.PublicState)
	privateJSON, _ := json.Marshal(req.PrivateState)
	switch req.Action {
	case "clue":
		return fmt.Sprintf(`Action: clue
Your seat: %d
Public state JSON: %s
Your private state JSON: %s
Max clue length: %d

Return JSON: {"clue":"..."}`, req.Seat, publicJSON, privateJSON, req.Rules.MaxClueLength)
	case "vote":
		return fmt.Sprintf(`Action: vote
Your seat: %d
Public state JSON: %s
Your private state JSON: %s

Return JSON: {"targetSeat": <seat number>}`, req.Seat, publicJSON, privateJSON)
	case "drifter_guess":
		return fmt.Sprintf(`Action: drifter_guess
Your seat: %d
Public state JSON: %s
Your private state JSON: %s

You are the Drifter and were eliminated. Guess the Natives' secret word.
Return JSON: {"guess":"..."}`, req.Seat, publicJSON, privateJSON)
	default:
		return fmt.Sprintf(`Action: %s
Public state JSON: %s
Private state JSON: %s
Return JSON.`, req.Action, publicJSON, privateJSON)
	}
}

func extractJSON(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func parseAction(action string, obj map[string]any) (TurnResponse, error) {
	switch action {
	case "clue":
		clue, _ := obj["clue"].(string)
		return TurnResponse{Clue: strings.TrimSpace(clue)}, nil
	case "vote":
		return TurnResponse{TargetSeat: intFromAny(obj["targetSeat"])}, nil
	case "drifter_guess":
		guess, _ := obj["guess"].(string)
		return TurnResponse{Guess: strings.TrimSpace(guess)}, nil
	default:
		return TurnResponse{}, fmt.Errorf("unknown action")
	}
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

var wordRE = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9 '-]*`)

func validate(req TurnRequest, resp TurnResponse) bool {
	switch req.Action {
	case "clue":
		clue := strings.TrimSpace(resp.Clue)
		if clue == "" || len([]rune(clue)) > req.Rules.MaxClueLength {
			return false
		}
		if containsSecret(clue, req.PrivateState.Word) {
			return false
		}
		// avoid exact repeats
		for _, c := range req.PublicState.Clues {
			if strings.EqualFold(strings.TrimSpace(c.Text), clue) {
				return false
			}
		}
		return true
	case "vote":
		if resp.TargetSeat == 0 || resp.TargetSeat == req.Seat {
			return false
		}
		for _, p := range req.PublicState.Players {
			if p.Seat == resp.TargetSeat && p.Alive {
				return true
			}
		}
		return false
	case "drifter_guess":
		guess := strings.TrimSpace(resp.Guess)
		return guess != "" && len([]rune(guess)) <= 40
	default:
		return false
	}
}

func containsSecret(clue, secret string) bool {
	secret = strings.ToLower(strings.TrimSpace(secret))
	if secret == "" {
		return false
	}
	clueLower := strings.ToLower(clue)
	if strings.Contains(clueLower, secret) {
		return true
	}
	// also check individual words of secret
	for _, part := range strings.Fields(secret) {
		if len(part) < 3 {
			continue
		}
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(part) + `\b`).MatchString(clueLower) {
			return true
		}
	}
	return false
}
