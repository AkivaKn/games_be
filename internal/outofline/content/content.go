// Package content owns the original prompt and answer cards for Out of Line.
//
// The cards are compiled into the binary instead of seeded into Postgres:
// they are static product content, not player data. Keeping them server-side
// means a browser never receives the full deck — only the current prompt and
// the player's own hand.
package content

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed decks/*.json
var embedded embed.FS

// Card is one playable answer.
type Card struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// Prompt is one judge prompt. Pick is the number of answer cards required.
type Prompt struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Pick int    `json:"pick"`
}

// Deck is one selectable pack.
type Deck struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Rating      string   `json:"rating"`
	Prompts     []Prompt `json:"prompts"`
	Answers     []Card   `json:"answers"`
}

// DeckInfo is the public metadata the setup screen is allowed to see.
type DeckInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Rating      string `json:"rating"`
	PromptCount int    `json:"prompt_count"`
	AnswerCount int    `json:"answer_count"`
	Adult       bool   `json:"adult"`
}

// Registry is the validated, indexed card collection.
type Registry struct {
	decks    []Deck
	byID     map[string]Deck
	prompts  map[string]Prompt
	answers  map[string]Card
	promptBy map[string]string // prompt id -> deck id
	answerBy map[string]string // answer id -> deck id
}

// Load reads and validates every embedded deck. Invalid content is a startup
// failure: a broken deck must never become a live game.
func Load() (*Registry, error) {
	entries, err := embedded.ReadDir("decks")
	if err != nil {
		return nil, fmt.Errorf("reading deck directory: %w", err)
	}

	reg := &Registry{
		byID:     make(map[string]Deck),
		prompts:  make(map[string]Prompt),
		answers:  make(map[string]Card),
		promptBy: make(map[string]string),
		answerBy: make(map[string]string),
	}

	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".json" {
			continue
		}
		raw, err := embedded.ReadFile(path.Join("decks", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		var deck Deck
		if err := json.Unmarshal(raw, &deck); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}
		if err := reg.add(deck); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
	}

	if len(reg.decks) == 0 {
		return nil, fmt.Errorf("no decks found")
	}
	if _, ok := reg.byID["base"]; !ok {
		return nil, fmt.Errorf("base deck is required")
	}

	sort.Slice(reg.decks, func(i, j int) bool {
		if reg.decks[i].ID == "base" {
			return true
		}
		if reg.decks[j].ID == "base" {
			return false
		}
		return reg.decks[i].Name < reg.decks[j].Name
	})
	return reg, nil
}

func (r *Registry) add(deck Deck) error {
	deck.ID = strings.TrimSpace(deck.ID)
	deck.Name = strings.TrimSpace(deck.Name)
	deck.Rating = strings.ToLower(strings.TrimSpace(deck.Rating))
	if deck.ID == "" || deck.Name == "" {
		return fmt.Errorf("deck id and name are required")
	}
	if deck.Rating != "teen" && deck.Rating != "adult" {
		return fmt.Errorf("deck %s has invalid rating %q", deck.ID, deck.Rating)
	}
	if _, exists := r.byID[deck.ID]; exists {
		return fmt.Errorf("duplicate deck id %q", deck.ID)
	}
	if len(deck.Prompts) == 0 || len(deck.Answers) == 0 {
		return fmt.Errorf("deck %s must contain prompts and answers", deck.ID)
	}

	seenPromptText := make(map[string]string)
	seenAnswerText := make(map[string]string)
	for i := range deck.Prompts {
		p := &deck.Prompts[i]
		p.ID = strings.TrimSpace(p.ID)
		p.Text = strings.TrimSpace(p.Text)
		if p.ID == "" || p.Text == "" {
			return fmt.Errorf("deck %s has an empty prompt", deck.ID)
		}
		if p.Pick != 1 && p.Pick != 2 {
			return fmt.Errorf("prompt %s has invalid pick %d", p.ID, p.Pick)
		}
		if _, exists := r.prompts[p.ID]; exists {
			return fmt.Errorf("duplicate prompt id %q", p.ID)
		}
		key := strings.ToLower(p.Text)
		if prev, exists := seenPromptText[key]; exists {
			return fmt.Errorf("duplicate prompt text in %s: %q and %q", deck.ID, prev, p.ID)
		}
		seenPromptText[key] = p.ID
		r.prompts[p.ID] = *p
		r.promptBy[p.ID] = deck.ID
	}
	for i := range deck.Answers {
		a := &deck.Answers[i]
		a.ID = strings.TrimSpace(a.ID)
		a.Text = strings.TrimSpace(a.Text)
		if a.ID == "" || a.Text == "" {
			return fmt.Errorf("deck %s has an empty answer", deck.ID)
		}
		if _, exists := r.answers[a.ID]; exists {
			return fmt.Errorf("duplicate answer id %q", a.ID)
		}
		key := strings.ToLower(a.Text)
		if prev, exists := seenAnswerText[key]; exists {
			return fmt.Errorf("duplicate answer text in %s: %q and %q", deck.ID, prev, a.ID)
		}
		seenAnswerText[key] = a.ID
		r.answers[a.ID] = *a
		r.answerBy[a.ID] = deck.ID
	}
	r.byID[deck.ID] = deck
	r.decks = append(r.decks, deck)
	return nil
}

// DeckInfos returns public metadata with the base deck first.
func (r *Registry) DeckInfos() []DeckInfo {
	out := make([]DeckInfo, 0, len(r.decks))
	for _, deck := range r.decks {
		out = append(out, DeckInfo{
			ID:          deck.ID,
			Name:        deck.Name,
			Description: deck.Description,
			Rating:      deck.Rating,
			PromptCount: len(deck.Prompts),
			AnswerCount: len(deck.Answers),
			Adult:       deck.Rating == "adult",
		})
	}
	return out
}

// Resolve validates a selected deck list. The base deck is mandatory.
func (r *Registry) Resolve(ids []string) ([]Deck, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("select at least the base deck")
	}
	seen := make(map[string]bool, len(ids))
	decks := make([]Deck, 0, len(ids))
	hasBase := false
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		deck, ok := r.byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown deck %q", id)
		}
		seen[id] = true
		if id == "base" {
			hasBase = true
		}
		decks = append(decks, deck)
	}
	if !hasBase {
		return nil, fmt.Errorf("the base deck is required")
	}
	return decks, nil
}

// Adult reports whether any selected deck is 18+.
func Adult(decks []Deck) bool {
	for _, deck := range decks {
		if deck.Rating == "adult" {
			return true
		}
	}
	return false
}

// PromptIDs returns a fresh slice of prompt IDs from the selected decks.
func (r *Registry) PromptIDs(decks []Deck) []string {
	ids := make([]string, 0)
	for _, deck := range decks {
		for _, p := range deck.Prompts {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

// AnswerIDs returns a fresh slice of answer IDs from the selected decks.
func (r *Registry) AnswerIDs(decks []Deck) []string {
	ids := make([]string, 0)
	for _, deck := range decks {
		for _, a := range deck.Answers {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// Prompt resolves one prompt by ID.
func (r *Registry) Prompt(id string) (Prompt, bool) {
	p, ok := r.prompts[id]
	return p, ok
}

// Answer resolves one answer by ID.
func (r *Registry) Answer(id string) (Card, bool) {
	a, ok := r.answers[id]
	return a, ok
}

// Deck returns one deck by ID.
func (r *Registry) Deck(id string) (Deck, bool) {
	d, ok := r.byID[id]
	return d, ok
}
