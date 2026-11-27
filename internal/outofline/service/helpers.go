package service

import (
	"encoding/json"
	"fmt"

	"boggle-api/internal/outofline/content"
)

func marshalCards(cards []content.Card) string {
	if cards == nil {
		cards = []content.Card{}
	}
	raw, _ := json.Marshal(cards)
	return string(raw)
}

func parseCards(raw string) ([]content.Card, error) {
	if raw == "" || raw == "null" {
		return []content.Card{}, nil
	}
	var cards []content.Card
	if err := json.Unmarshal([]byte(raw), &cards); err != nil {
		return nil, fmt.Errorf("parsing cards: %w", err)
	}
	if cards == nil {
		cards = []content.Card{}
	}
	return cards, nil
}

func marshalIDs(ids []string) string {
	if ids == nil {
		ids = []string{}
	}
	raw, _ := json.Marshal(ids)
	return string(raw)
}

func parseIDs(raw string) ([]string, error) {
	if raw == "" || raw == "null" {
		return []string{}, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("parsing card ids: %w", err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func parsePrompt(raw string) (content.Prompt, error) {
	if raw == "" || raw == "{}" || raw == "null" {
		return content.Prompt{}, fmt.Errorf("no prompt is active")
	}
	var prompt content.Prompt
	if err := json.Unmarshal([]byte(raw), &prompt); err != nil {
		return content.Prompt{}, fmt.Errorf("parsing prompt: %w", err)
	}
	if prompt.ID == "" || prompt.Text == "" {
		return content.Prompt{}, fmt.Errorf("no prompt is active")
	}
	if prompt.Pick != 1 && prompt.Pick != 2 {
		return content.Prompt{}, fmt.Errorf("prompt has invalid pick count")
	}
	return prompt, nil
}

// removeCards returns the selected cards in request order and the untouched
// hand. It rejects unknown, duplicated, or excess IDs instead of silently
// discarding anything the client sent.
func removeCards(hand []content.Card, ids []string) ([]content.Card, []content.Card, error) {
	if len(ids) == 0 {
		return nil, nil, gameErr(400, "choose a card first")
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || requested[id] {
			return nil, nil, gameErr(400, "duplicate or empty card selected")
		}
		requested[id] = true
	}

	byID := make(map[string]content.Card, len(hand))
	remaining := make([]content.Card, 0, len(hand))
	for _, card := range hand {
		if requested[card.ID] {
			byID[card.ID] = card
			continue
		}
		remaining = append(remaining, card)
	}
	if len(byID) != len(requested) {
		return nil, nil, gameErr(400, "that card is not in your hand")
	}

	selected := make([]content.Card, 0, len(ids))
	for _, id := range ids {
		selected = append(selected, byID[id])
	}
	return selected, remaining, nil
}
