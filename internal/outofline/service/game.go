package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/models"
	"boggle-api/internal/outofline/repository"

	"github.com/google/uuid"
)

// Start builds the deck, deals hands, and opens round one.
func (s *Service) Start(ctx context.Context, roomID, token string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if player.Seat != room.HostSeat {
			return gameErr(403, "only the host can start the game")
		}
		if room.Status != "lobby" {
			return gameErr(409, "the game has already started")
		}
		decks, err := s.selectedDecks(room)
		if err != nil {
			return err
		}
		players, err := repo.ListPlayers(ctx, room.ID)
		if err != nil {
			return err
		}
		if len(players) < MinPlayers {
			return gameErr(409, "at least %d players are needed", MinPlayers)
		}
		if len(players) > MaxPlayers {
			return gameErr(409, "too many players")
		}

		promptIDs := s.decks.PromptIDs(decks)
		shuffleStrings(promptIDs)
		answerIDs := s.decks.AnswerIDs(decks)
		shuffleStrings(answerIDs)
		if len(answerIDs) < len(players)*HandSize {
			return gameErr(409, "the selected decks do not contain enough answer cards")
		}
		drawPile := append([]string{}, answerIDs...)
		discard := []string{}

		for i := range players {
			hand, err := s.drawCards(&drawPile, &discard, HandSize)
			if err != nil {
				return err
			}
			players[i].Hand = marshalCards(hand)
			if err := repo.UpdatePlayerHand(ctx, players[i].ID, players[i].Hand); err != nil {
				return err
			}
		}

		prompt, promptDraw, err := s.takePrompt(promptIDs, decks)
		if err != nil {
			return err
		}
		round, err := s.newRound(room, 1, room.HostSeat, prompt)
		if err != nil {
			return err
		}
		if err := repo.CreateRound(ctx, round); err != nil {
			return err
		}

		room.Status = "active"
		room.Phase = "submitting"
		room.JudgeSeat = room.HostSeat
		room.RoundNo = 1
		room.Prompt = marshalPrompt(prompt)
		room.PromptDraw = marshalIDs(promptDraw)
		room.AnswerDraw = marshalIDs(drawPile)
		room.AnswerDiscard = marshalIDs(discard)
		return repo.UpdateRoom(ctx, room)
	})
}

// Submit accepts one player's card(s) and refills their hand.
func (s *Service) Submit(ctx context.Context, roomID, token string, cardIDs []string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if room.Status != "active" || room.Phase != "submitting" {
			return gameErr(409, "the table is not accepting cards right now")
		}
		if player.Seat == room.JudgeSeat {
			return gameErr(409, "the judge cannot submit a card")
		}
		round, err := repo.GetRound(ctx, room.ID, room.RoundNo)
		if err != nil {
			return err
		}
		if _, err := repo.GetSubmissionBySeat(ctx, round.ID, player.Seat); err == nil {
			return gameErr(409, "you have already submitted for this round")
		}
		prompt, err := parsePrompt(room.Prompt)
		if err != nil {
			return err
		}
		if err := s.recordSubmission(ctx, repo, room, player, round, prompt, cardIDs); err != nil {
			return err
		}

		players, err := repo.ListPlayers(ctx, room.ID)
		if err != nil {
			return err
		}
		submissions, err := repo.ListSubmissions(ctx, round.ID)
		if err != nil {
			return err
		}
		if len(submissions) >= len(players)-1 {
			room.Phase = "judging"
		}
		return repo.UpdateRoom(ctx, room)
	})
}

// Pick is the judge's winner selection.
func (s *Service) Pick(ctx context.Context, roomID, token, submissionID string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if room.Status != "active" || room.Phase != "judging" {
			return gameErr(409, "the table is not ready for a winner")
		}
		if player.Seat != room.JudgeSeat {
			return gameErr(403, "only the judge can pick a winner")
		}
		round, err := repo.GetRound(ctx, room.ID, room.RoundNo)
		if err != nil {
			return err
		}
		submission, err := repo.GetSubmission(ctx, submissionID)
		if err != nil || submission.RoundID != round.ID {
			return gameErr(404, "submission not found in this round")
		}
		return s.applyWinner(ctx, repo, room, round, submission)
	})
}

// Next advances from a reveal to the next round, or to the final standings.
func (s *Service) Next(ctx context.Context, roomID, token string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if room.Status != "active" || room.Phase != "reveal" {
			return gameErr(409, "there is no reveal to advance")
		}
		if player.Seat != room.HostSeat && player.Seat != room.JudgeSeat {
			return gameErr(403, "only the host or judge can advance the round")
		}
		return s.advanceRoom(ctx, repo, room)
	})
}

// Force is the host's recovery valve. It resolves one stalled phase at a
// time: auto-submit missing hands, auto-pick a random submission, or advance
// the reveal. It is intentionally manual; there are no turn timers.
func (s *Service) Force(ctx context.Context, roomID, token string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if player.Seat != room.HostSeat {
			return gameErr(403, "only the host can force the round")
		}
		if room.Status != "active" {
			return gameErr(409, "there is no active round to force")
		}
		round, err := repo.GetRound(ctx, room.ID, room.RoundNo)
		if err != nil {
			return err
		}
		prompt, err := parsePrompt(room.Prompt)
		if err != nil {
			return err
		}

		switch room.Phase {
		case "submitting":
			players, err := repo.ListPlayers(ctx, room.ID)
			if err != nil {
				return err
			}
			submissions, err := repo.ListSubmissions(ctx, round.ID)
			if err != nil {
				return err
			}
			submitted := make(map[int]bool, len(submissions))
			for _, submission := range submissions {
				submitted[submission.Seat] = true
			}
			for _, candidate := range players {
				if candidate.Seat == room.JudgeSeat || submitted[candidate.Seat] {
					continue
				}
				hand, err := parseCards(candidate.Hand)
				if err != nil {
					return err
				}
				if len(hand) < prompt.Pick {
					return gameErr(409, "player %s does not have enough cards", candidate.Name)
				}
				cardIDs := randomCardIDs(hand, prompt.Pick)
				if err := s.recordSubmission(ctx, repo, room, &candidate, round, prompt, cardIDs); err != nil {
					return err
				}
			}
			room.Phase = "judging"
			return repo.UpdateRoom(ctx, room)

		case "judging":
			submissions, err := repo.ListSubmissions(ctx, round.ID)
			if err != nil {
				return err
			}
			if len(submissions) == 0 {
				return gameErr(409, "there are no submissions to judge")
			}
			return s.applyWinner(ctx, repo, room, round, &submissions[rand.Intn(len(submissions))])

		case "reveal":
			return s.advanceRoom(ctx, repo, room)

		default:
			return gameErr(409, "this phase cannot be forced")
		}
	})
}

// recordSubmission validates a hand, refills it, and inserts the submission.
// It updates room.AnswerDraw/Discard in memory; the caller persists the room.
func (s *Service) recordSubmission(ctx context.Context, repo repository.Repository, room *models.Room, player *models.Player, round *models.Round, prompt content.Prompt, cardIDs []string) error {
	if len(cardIDs) != prompt.Pick {
		return gameErr(400, "this prompt requires %d card%s", prompt.Pick, plural(prompt.Pick))
	}
	hand, err := parseCards(player.Hand)
	if err != nil {
		return err
	}
	selected, remaining, err := removeCards(hand, cardIDs)
	if err != nil {
		return err
	}

	drawPile, err := parseIDs(room.AnswerDraw)
	if err != nil {
		return err
	}
	discard, err := parseIDs(room.AnswerDiscard)
	if err != nil {
		return err
	}
	// Draw replacements BEFORE discarding the played cards, so a freshly
	// recycled deck cannot hand the same card straight back.
	replacements, err := s.drawCards(&drawPile, &discard, prompt.Pick)
	if err != nil {
		return err
	}
	for _, card := range selected {
		discard = append(discard, card.ID)
	}
	newHand := append(remaining, replacements...)
	player.Hand = marshalCards(newHand)
	if err := repo.UpdatePlayerHand(ctx, player.ID, player.Hand); err != nil {
		return err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	submission := &models.Submission{
		ID:           id.String(),
		RoundID:      round.ID,
		Seat:         player.Seat,
		Cards:        marshalCards(selected),
		DisplayOrder: rand.Intn(1_000_000_000),
		CreatedAt:    time.Now().UTC(),
	}
	if err := repo.CreateSubmission(ctx, submission); err != nil {
		return err
	}
	room.AnswerDraw = marshalIDs(drawPile)
	room.AnswerDiscard = marshalIDs(discard)
	return nil
}

func (s *Service) applyWinner(ctx context.Context, repo repository.Repository, room *models.Room, round *models.Round, submission *models.Submission) error {
	if round.WinnerSubmissionID != "" {
		return gameErr(409, "this round already has a winner")
	}
	winner, err := repo.GetPlayerBySeat(ctx, room.ID, submission.Seat)
	if err != nil {
		return err
	}
	if err := repo.UpdateRoundWinner(ctx, round.ID, submission.ID); err != nil {
		return err
	}
	if err := repo.SetPlayerScore(ctx, winner.ID, winner.Score+1); err != nil {
		return err
	}
	room.Phase = "reveal"
	return repo.UpdateRoom(ctx, room)
}

func (s *Service) advanceRoom(ctx context.Context, repo repository.Repository, room *models.Room) error {
	players, err := repo.ListPlayers(ctx, room.ID)
	if err != nil {
		return err
	}
	for _, player := range players {
		if player.Score >= room.TargetScore {
			room.Status = "finished"
			room.Phase = "finished"
			return repo.UpdateRoom(ctx, room)
		}
	}

	decks, err := s.selectedDecks(room)
	if err != nil {
		return err
	}
	promptDraw, err := parseIDs(room.PromptDraw)
	if err != nil {
		return err
	}
	prompt, promptDraw, err := s.takePrompt(promptDraw, decks)
	if err != nil {
		return err
	}
	nextJudge := nextJudgeSeat(players, room.JudgeSeat)
	roundNo := room.RoundNo + 1
	round, err := s.newRound(room, roundNo, nextJudge, prompt)
	if err != nil {
		return err
	}
	if err := repo.CreateRound(ctx, round); err != nil {
		return err
	}
	room.Phase = "submitting"
	room.RoundNo = roundNo
	room.JudgeSeat = nextJudge
	room.Prompt = marshalPrompt(prompt)
	room.PromptDraw = marshalIDs(promptDraw)
	return repo.UpdateRoom(ctx, room)
}

func (s *Service) newRound(room *models.Room, roundNo, judgeSeat int, prompt content.Prompt) (*models.Round, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return &models.Round{
		ID:                 id.String(),
		RoomID:             room.ID,
		RoundNo:            roundNo,
		Prompt:             marshalPrompt(prompt),
		JudgeSeat:          judgeSeat,
		WinnerSubmissionID: "",
		CreatedAt:          time.Now().UTC(),
	}, nil
}

func (s *Service) takePrompt(draw []string, decks []content.Deck) (content.Prompt, []string, error) {
	if len(draw) == 0 {
		draw = s.decks.PromptIDs(decks)
		shuffleStrings(draw)
	}
	for len(draw) > 0 {
		id := draw[0]
		draw = draw[1:]
		if prompt, ok := s.decks.Prompt(id); ok {
			return prompt, draw, nil
		}
	}
	return content.Prompt{}, nil, fmt.Errorf("no prompts available")
}

func (s *Service) drawCards(draw, discard *[]string, count int) ([]content.Card, error) {
	if count <= 0 {
		return []content.Card{}, nil
	}
	if len(*draw) < count {
		*draw = append(*draw, *discard...)
		*discard = nil
		shuffleStrings(*draw)
	}
	if len(*draw) < count {
		return nil, gameErr(409, "the answer deck has run out")
	}
	ids := append([]string{}, (*draw)[:count]...)
	*draw = (*draw)[count:]
	cards := make([]content.Card, 0, count)
	for _, id := range ids {
		card, ok := s.decks.Answer(id)
		if !ok {
			return nil, fmt.Errorf("answer %q is missing from the deck registry", id)
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func shuffleStrings(values []string) {
	rand.Shuffle(len(values), func(i, j int) {
		values[i], values[j] = values[j], values[i]
	})
}

func randomCardIDs(hand []content.Card, count int) []string {
	copyHand := append([]content.Card{}, hand...)
	shuffleCards(copyHand)
	ids := make([]string, 0, count)
	for i := 0; i < count && i < len(copyHand); i++ {
		ids = append(ids, copyHand[i].ID)
	}
	return ids
}

func shuffleCards(cards []content.Card) {
	rand.Shuffle(len(cards), func(i, j int) {
		cards[i], cards[j] = cards[j], cards[i]
	})
}

func nextJudgeSeat(players []models.Player, current int) int {
	if len(players) == 0 {
		return 0
	}
	sorted := append([]models.Player{}, players...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seat < sorted[j].Seat })
	for i, player := range sorted {
		if player.Seat == current {
			return sorted[(i+1)%len(sorted)].Seat
		}
	}
	return sorted[0].Seat
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func marshalPrompt(prompt content.Prompt) string {
	raw, _ := json.Marshal(prompt)
	return string(raw)
}
