package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"boggle-api/internal/codes"
	"boggle-api/internal/dberr"
	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/models"
	"boggle-api/internal/outofline/repository"

	"github.com/google/uuid"
	"github.com/vingarcia/ksql"
)

const (
	MinPlayers = 3
	MaxPlayers = 12
	HandSize   = 7
)

// GameError is a rule rejection the HTTP handler can map to a status and a
// readable message.
type GameError struct {
	Status  int
	Message string
}

func (e *GameError) Error() string { return e.Message }

func gameErr(status int, format string, args ...interface{}) error {
	return &GameError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// CreateInput is the setup payload for a new room.
type CreateInput struct {
	Name           string   `json:"name"`
	DeckIDs        []string `json:"decks"`
	TargetScore    int      `json:"target_score"`
	AdultConfirmed bool     `json:"adult_confirmed"`
}

// CreateResult is returned to the host before they open the WebSocket.
type CreateResult struct {
	Room   *models.Room
	Player *models.Player
}

// Preview is the public lobby card shown before a player joins.
type Preview struct {
	Code        string             `json:"code"`
	Status      string             `json:"status"`
	Phase       string             `json:"phase"`
	PlayerCount int                `json:"player_count"`
	MinPlayers  int                `json:"min_players"`
	MaxPlayers  int                `json:"max_players"`
	TargetScore int                `json:"target_score"`
	Decks       []content.DeckInfo `json:"decks"`
	Adult       bool               `json:"adult"`
}

// Service owns the rules, deck state, and room locks. The database is the
// source of truth; locks serialize simultaneous commands for one room.
type Service struct {
	db    ksql.DB
	repo  repository.Repository
	decks *content.Registry
	locks sync.Map // room id -> *sync.Mutex
}

func New(db ksql.DB, repo repository.Repository, decks *content.Registry) *Service {
	return &Service{db: db, repo: repo, decks: decks}
}

func (s *Service) lockRoom(roomID string) func() {
	value, _ := s.locks.LoadOrStore(roomID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) withRoomTx(ctx context.Context, roomID string, fn func(repository.Repository) error) error {
	unlock := s.lockRoom(roomID)
	defer unlock()
	return s.db.Transaction(ctx, func(tx ksql.Provider) error {
		return fn(repository.NewRepository(tx))
	})
}

// CreateRoom validates setup, creates the lobby, and seats the host.
func (s *Service) CreateRoom(ctx context.Context, in CreateInput) (*CreateResult, error) {
	decks, err := s.decks.Resolve(in.DeckIDs)
	if err != nil {
		return nil, gameErr(400, "%s", err.Error())
	}
	if content.Adult(decks) && !in.AdultConfirmed {
		return nil, gameErr(403, "adult confirmation is required for this room")
	}
	target := in.TargetScore
	if target == 0 {
		target = 5
	}
	if target < 3 || target > 7 {
		return nil, gameErr(400, "target score must be between 3 and 7")
	}

	deckIDs := make([]string, 0, len(decks))
	for _, deck := range decks {
		deckIDs = append(deckIDs, deck.ID)
	}
	deckJSON, _ := json.Marshal(deckIDs)
	now := time.Now().UTC()

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	room := &models.Room{
		ID:            id.String(),
		Status:        "lobby",
		Phase:         "lobby",
		HostSeat:      1,
		DeckIDs:       string(deckJSON),
		TargetScore:   target,
		JudgeSeat:     0,
		RoundNo:       0,
		Prompt:        "{}",
		PromptDraw:    "[]",
		AnswerDraw:    "[]",
		AnswerDiscard: "[]",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	for attempt := 0; ; attempt++ {
		room.Code = codes.NewRoomCode()
		err = s.repo.CreateRoom(ctx, room)
		if err == nil {
			break
		}
		if !dberr.IsUniqueViolation(err) || attempt >= codes.MaxPick {
			return nil, err
		}
	}

	player, err := s.newPlayer(room.ID, 1, cleanName(in.Name, "Host"))
	if err != nil {
		_ = s.repo.DeleteRoom(ctx, room.ID)
		return nil, err
	}
	if err := s.repo.CreatePlayer(ctx, player); err != nil {
		_ = s.repo.DeleteRoom(ctx, room.ID)
		return nil, err
	}
	return &CreateResult{Room: room, Player: player}, nil
}

// PreviewRoom returns public metadata for a code, including the adult flag so
// the join screen can ask for one confirmation before opening a socket.
func (s *Service) PreviewRoom(ctx context.Context, code string) (*Preview, error) {
	room, err := s.repo.GetRoomByCode(ctx, strings.TrimSpace(code))
	if err != nil {
		return nil, gameErr(404, "room not found")
	}
	decks, err := s.selectedDecks(room)
	if err != nil {
		return nil, err
	}
	players, err := s.repo.ListPlayers(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	return &Preview{
		Code:        room.Code,
		Status:      room.Status,
		Phase:       room.Phase,
		PlayerCount: len(players),
		MinPlayers:  MinPlayers,
		MaxPlayers:  MaxPlayers,
		TargetScore: room.TargetScore,
		Decks:       deckInfos(decks),
		Adult:       content.Adult(decks),
	}, nil
}

// AddPlayer claims the next free seat in a lobby. The host is already seat 1.
func (s *Service) AddPlayer(ctx context.Context, roomID, name string, adultConfirmed bool) (*models.Player, error) {
	var out *models.Player
	err := s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, err := repo.GetRoomForUpdate(ctx, roomID)
		if err != nil {
			return gameErr(404, "room not found")
		}
		if room.Status != "lobby" {
			return gameErr(409, "this room has already started")
		}
		decks, err := s.selectedDecks(room)
		if err != nil {
			return err
		}
		if content.Adult(decks) && !adultConfirmed {
			return gameErr(403, "adult confirmation is required for this room")
		}
		players, err := repo.ListPlayers(ctx, room.ID)
		if err != nil {
			return err
		}
		if len(players) >= MaxPlayers {
			return gameErr(409, "room is full")
		}
		seat := nextFreeSeat(players)
		if seat == 0 {
			return gameErr(409, "room is full")
		}
		player, err := s.newPlayer(room.ID, seat, cleanName(name, fmt.Sprintf("Player %d", seat)))
		if err != nil {
			return err
		}
		if err := repo.CreatePlayer(ctx, player); err != nil {
			return err
		}
		out = player
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemovePlayer handles the host-only lobby cleanup path.
func (s *Service) RemovePlayer(ctx context.Context, roomID, token string, seat int) (int, error) {
	removed := 0
	err := s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if player.Seat != room.HostSeat {
			return gameErr(403, "only the host can remove players")
		}
		if room.Status != "lobby" {
			return gameErr(409, "players can only be removed in the lobby")
		}
		if seat == room.HostSeat || seat == player.Seat {
			return gameErr(400, "the host cannot remove themselves")
		}
		if _, err := repo.GetPlayerBySeat(ctx, room.ID, seat); err != nil {
			return gameErr(404, "player not found")
		}
		if err := repo.DeletePlayer(ctx, room.ID, seat); err != nil {
			return err
		}
		removed = seat
		return nil
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// Rematch resets a finished room to its lobby while keeping players, tokens,
// decks, and target score.
func (s *Service) Rematch(ctx context.Context, roomID, token string) error {
	return s.withRoomTx(ctx, roomID, func(repo repository.Repository) error {
		room, player, err := s.authRoomPlayer(ctx, repo, roomID, token)
		if err != nil {
			return err
		}
		if player.Seat != room.HostSeat {
			return gameErr(403, "only the host can start a rematch")
		}
		if room.Status != "finished" {
			return gameErr(409, "the game has not finished")
		}
		if err := repo.DeleteRounds(ctx, room.ID); err != nil {
			return err
		}
		if err := repo.ResetPlayers(ctx, room.ID); err != nil {
			return err
		}
		room.Status = "lobby"
		room.Phase = "lobby"
		room.JudgeSeat = 0
		room.RoundNo = 0
		room.Prompt = "{}"
		room.PromptDraw = "[]"
		room.AnswerDraw = "[]"
		room.AnswerDiscard = "[]"
		return repo.UpdateRoom(ctx, room)
	})
}

func (s *Service) newPlayer(roomID string, seat int, name string) (*models.Player, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	token, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	return &models.Player{
		ID:        id.String(),
		RoomID:    roomID,
		Seat:      seat,
		Token:     token.String(),
		Name:      name,
		Score:     0,
		Hand:      "[]",
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Service) selectedDecks(room *models.Room) ([]content.Deck, error) {
	var ids []string
	if err := json.Unmarshal([]byte(room.DeckIDs), &ids); err != nil {
		return nil, fmt.Errorf("parsing room decks: %w", err)
	}
	decks, err := s.decks.Resolve(ids)
	if err != nil {
		return nil, fmt.Errorf("resolving room decks: %w", err)
	}
	return decks, nil
}

func (s *Service) authRoomPlayer(ctx context.Context, repo repository.Repository, roomID, token string) (*models.Room, *models.Player, error) {
	if strings.TrimSpace(token) == "" {
		return nil, nil, gameErr(401, "player token is required")
	}
	room, err := repo.GetRoomForUpdate(ctx, roomID)
	if err != nil {
		return nil, nil, gameErr(404, "room not found")
	}
	player, err := repo.GetPlayerByToken(ctx, room.ID, token)
	if err != nil {
		return nil, nil, gameErr(403, "invalid player token")
	}
	return room, player, nil
}

func (s *Service) PlayerByToken(ctx context.Context, roomID, token string) (*models.Player, error) {
	player, err := s.repo.GetPlayerByToken(ctx, roomID, token)
	if err != nil {
		return nil, gameErr(403, "invalid player token")
	}
	return player, nil
}

func (s *Service) RoomByCode(ctx context.Context, code string) (*models.Room, error) {
	room, err := s.repo.GetRoomByCode(ctx, strings.TrimSpace(code))
	if err != nil {
		return nil, gameErr(404, "room not found")
	}
	return room, nil
}

func (s *Service) RoomByID(ctx context.Context, id string) (*models.Room, error) {
	room, err := s.repo.GetRoom(ctx, id)
	if err != nil {
		return nil, gameErr(404, "room not found")
	}
	return room, nil
}

func (s *Service) Players(ctx context.Context, roomID string) ([]models.Player, error) {
	return s.repo.ListPlayers(ctx, roomID)
}

func cleanName(name, fallback string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = fallback
	}
	runes := []rune(name)
	if len(runes) > 24 {
		name = string(runes[:24])
	}
	return name
}

func nextFreeSeat(players []models.Player) int {
	claimed := make(map[int]bool, len(players))
	for _, player := range players {
		claimed[player.Seat] = true
	}
	for seat := 1; seat <= MaxPlayers; seat++ {
		if !claimed[seat] {
			return seat
		}
	}
	return 0
}

func deckInfos(decks []content.Deck) []content.DeckInfo {
	out := make([]content.DeckInfo, 0, len(decks))
	for _, deck := range decks {
		out = append(out, content.DeckInfo{
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

// IsGameError lets the handler distinguish a rule rejection from a 500.
func IsGameError(err error) (*GameError, bool) {
	var gameErr *GameError
	if errors.As(err, &gameErr) {
		return gameErr, true
	}
	return nil, false
}
