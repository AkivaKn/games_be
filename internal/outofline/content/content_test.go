package content

import "testing"

func TestLoadDecks(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	infos := reg.DeckInfos()
	if len(infos) != 7 {
		t.Fatalf("deck count = %d, want 7", len(infos))
	}
	if infos[0].ID != "base" {
		t.Fatalf("first deck = %q, want base", infos[0].ID)
	}
	want := map[string]struct {
		prompts int
		answers int
		adult   bool
	}{
		"base":             {100, 310, false},
		"group-chat":       {40, 110, false},
		"work-mode":        {40, 110, false},
		"after-hours":      {40, 110, true},
		"bad-decisions":    {40, 110, false},
		"deadpan-doom":     {40, 110, false},
		"family-gathering": {40, 110, false},
	}
	for _, info := range infos {
		expected, ok := want[info.ID]
		if !ok {
			t.Fatalf("unexpected deck %q", info.ID)
		}
		if info.PromptCount != expected.prompts || info.AnswerCount != expected.answers {
			t.Errorf("%s counts = %d/%d, want %d/%d",
				info.ID, info.PromptCount, info.AnswerCount, expected.prompts, expected.answers)
		}
		if info.Adult != expected.adult {
			t.Errorf("%s adult = %v, want %v", info.ID, info.Adult, expected.adult)
		}
	}

	if _, err := reg.Resolve([]string{"group-chat"}); err == nil {
		t.Fatal("Resolve accepted a selection without the base deck")
	}
	if _, err := reg.Resolve([]string{"base", "missing"}); err == nil {
		t.Fatal("Resolve accepted an unknown deck")
	}
	decks, err := reg.Resolve([]string{"base", "after-hours"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !Adult(decks) {
		t.Fatal("Adult = false for a selection containing after-hours")
	}
}

func TestPromptPicks(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, info := range reg.DeckInfos() {
		deck, _ := reg.Deck(info.ID)
		for _, prompt := range deck.Prompts {
			if prompt.Pick != 1 && prompt.Pick != 2 {
				t.Errorf("%s pick = %d", prompt.ID, prompt.Pick)
			}
			if prompt.Text == "" {
				t.Errorf("%s has empty text", prompt.ID)
			}
		}
	}
}

func TestNoBannedTrademark(t *testing.T) {
	reg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, deck := range reg.DeckInfos() {
		if deck.Name == "Cards Against Humanity" {
			t.Fatal("banned deck name found")
		}
	}
}
