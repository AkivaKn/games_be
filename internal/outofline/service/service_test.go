package service

import (
	"testing"

	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/models"
)

func TestNextJudgeSeatRotatesAndWraps(t *testing.T) {
	players := []models.Player{
		{Seat: 3, Name: "Three"},
		{Seat: 1, Name: "One"},
		{Seat: 5, Name: "Five"},
	}
	cases := map[int]int{1: 3, 3: 5, 5: 1, 99: 1}
	for current, want := range cases {
		if got := nextJudgeSeat(players, current); got != want {
			t.Errorf("nextJudgeSeat(%d) = %d, want %d", current, got, want)
		}
	}
}

func TestNextFreeSeat(t *testing.T) {
	players := []models.Player{{Seat: 1}, {Seat: 3}}
	if got := nextFreeSeat(players); got != 2 {
		t.Fatalf("nextFreeSeat = %d, want 2", got)
	}
	full := make([]models.Player, 0, MaxPlayers)
	for i := 1; i <= MaxPlayers; i++ {
		full = append(full, models.Player{Seat: i})
	}
	if got := nextFreeSeat(full); got != 0 {
		t.Fatalf("nextFreeSeat(full) = %d, want 0", got)
	}
}

func TestRemoveCards(t *testing.T) {
	hand := []content.Card{
		{ID: "a", Text: "A"},
		{ID: "b", Text: "B"},
		{ID: "c", Text: "C"},
	}
	selected, remaining, err := removeCards(hand, []string{"c", "a"})
	if err != nil {
		t.Fatalf("removeCards: %v", err)
	}
	if len(selected) != 2 || selected[0].ID != "c" || selected[1].ID != "a" {
		t.Fatalf("selected = %#v", selected)
	}
	if len(remaining) != 1 || remaining[0].ID != "b" {
		t.Fatalf("remaining = %#v", remaining)
	}

	if _, _, err := removeCards(hand, []string{"a", "a"}); err == nil {
		t.Fatal("removeCards accepted duplicate ids")
	}
	if _, _, err := removeCards(hand, []string{"missing"}); err == nil {
		t.Fatal("removeCards accepted a card not in hand")
	}
}

func TestRoundTripJSON(t *testing.T) {
	cards := []content.Card{{ID: "x", Text: "X"}}
	parsed, err := parseCards(marshalCards(cards))
	if err != nil || len(parsed) != 1 || parsed[0].ID != "x" {
		t.Fatalf("cards round trip: %#v, %v", parsed, err)
	}
	ids, err := parseIDs(marshalIDs([]string{"a", "b"}))
	if err != nil || len(ids) != 2 || ids[1] != "b" {
		t.Fatalf("ids round trip: %#v, %v", ids, err)
	}
	prompt := content.Prompt{ID: "p", Text: "Prompt ___", Pick: 2}
	parsedPrompt, err := parsePrompt(marshalPrompt(prompt))
	if err != nil || parsedPrompt.Pick != 2 || parsedPrompt.Text != prompt.Text {
		t.Fatalf("prompt round trip: %#v, %v", parsedPrompt, err)
	}
}

func TestCleanName(t *testing.T) {
	if got := cleanName("   ", "Host"); got != "Host" {
		t.Fatalf("cleanName blank = %q", got)
	}
	if got := cleanName("  A very long player name that exceeds the cap  ", "Host"); len([]rune(got)) != 24 {
		t.Fatalf("cleanName long = %q (%d runes)", got, len([]rune(got)))
	}
}
