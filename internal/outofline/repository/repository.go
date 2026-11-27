package repository

import (
	"context"
	"time"

	"boggle-api/internal/outofline/models"

	"github.com/vingarcia/ksql"
)

// Repository is the persistence surface for Out of Line. Every mutating
// operation can run on a transaction provider (see NewRepository) so the
// service can keep room state and hands consistent.
type Repository interface {
	CreateRoom(ctx context.Context, room *models.Room) error
	GetRoom(ctx context.Context, id string) (*models.Room, error)
	GetRoomByCode(ctx context.Context, code string) (*models.Room, error)
	GetRoomForUpdate(ctx context.Context, id string) (*models.Room, error)
	UpdateRoom(ctx context.Context, room *models.Room) error
	DeleteRoom(ctx context.Context, id string) error

	CreatePlayer(ctx context.Context, player *models.Player) error
	GetPlayerByToken(ctx context.Context, roomID, token string) (*models.Player, error)
	GetPlayerBySeat(ctx context.Context, roomID string, seat int) (*models.Player, error)
	ListPlayers(ctx context.Context, roomID string) ([]models.Player, error)
	UpdatePlayerHand(ctx context.Context, playerID, hand string) error
	SetPlayerScore(ctx context.Context, playerID string, score int) error
	ResetPlayers(ctx context.Context, roomID string) error
	DeletePlayer(ctx context.Context, roomID string, seat int) error

	CreateRound(ctx context.Context, round *models.Round) error
	GetRound(ctx context.Context, roomID string, roundNo int) (*models.Round, error)
	UpdateRoundWinner(ctx context.Context, roundID, submissionID string) error
	DeleteRounds(ctx context.Context, roomID string) error

	CreateSubmission(ctx context.Context, submission *models.Submission) error
	GetSubmission(ctx context.Context, id string) (*models.Submission, error)
	GetSubmissionBySeat(ctx context.Context, roundID string, seat int) (*models.Submission, error)
	ListSubmissions(ctx context.Context, roundID string) ([]models.Submission, error)
}

type repo struct {
	db ksql.Provider
}

var (
	RoomsTable       = ksql.NewTable("outofline_rooms")
	PlayersTable     = ksql.NewTable("outofline_players")
	RoundsTable      = ksql.NewTable("outofline_rounds")
	SubmissionsTable = ksql.NewTable("outofline_submissions")
)

func NewRepository(db ksql.Provider) Repository {
	return &repo{db: db}
}

func (r *repo) CreateRoom(ctx context.Context, room *models.Room) error {
	return r.db.Insert(ctx, RoomsTable, room)
}

func (r *repo) GetRoom(ctx context.Context, id string) (*models.Room, error) {
	var room models.Room
	err := r.db.QueryOne(ctx, &room, "SELECT * FROM outofline_rooms WHERE id = $1", id)
	return &room, err
}

func (r *repo) GetRoomByCode(ctx context.Context, code string) (*models.Room, error) {
	var room models.Room
	err := r.db.QueryOne(ctx, &room, "SELECT * FROM outofline_rooms WHERE code = $1", code)
	return &room, err
}

func (r *repo) GetRoomForUpdate(ctx context.Context, id string) (*models.Room, error) {
	var room models.Room
	err := r.db.QueryOne(ctx, &room, "SELECT * FROM outofline_rooms WHERE id = $1 FOR UPDATE", id)
	return &room, err
}

func (r *repo) UpdateRoom(ctx context.Context, room *models.Room) error {
	now := time.Now().UTC()
	_, err := r.db.Exec(ctx, `
UPDATE outofline_rooms
SET status = $1,
    phase = $2,
    host_seat = $3,
    deck_ids = $4::jsonb,
    target_score = $5,
    judge_seat = $6,
    round_no = $7,
    prompt = $8::jsonb,
    prompt_draw = $9::jsonb,
    answer_draw = $10::jsonb,
    answer_discard = $11::jsonb,
    updated_at = $12
WHERE id = $13`,
		room.Status, room.Phase, room.HostSeat, room.DeckIDs, room.TargetScore,
		room.JudgeSeat, room.RoundNo, room.Prompt, room.PromptDraw,
		room.AnswerDraw, room.AnswerDiscard, now, room.ID)
	room.UpdatedAt = now
	return err
}

func (r *repo) DeleteRoom(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM outofline_rooms WHERE id = $1", id)
	return err
}

func (r *repo) CreatePlayer(ctx context.Context, player *models.Player) error {
	return r.db.Insert(ctx, PlayersTable, player)
}

func (r *repo) GetPlayerByToken(ctx context.Context, roomID, token string) (*models.Player, error) {
	var player models.Player
	err := r.db.QueryOne(ctx, &player,
		"SELECT * FROM outofline_players WHERE room_id = $1 AND token = $2", roomID, token)
	return &player, err
}

func (r *repo) GetPlayerBySeat(ctx context.Context, roomID string, seat int) (*models.Player, error) {
	var player models.Player
	err := r.db.QueryOne(ctx, &player,
		"SELECT * FROM outofline_players WHERE room_id = $1 AND seat = $2", roomID, seat)
	return &player, err
}

func (r *repo) ListPlayers(ctx context.Context, roomID string) ([]models.Player, error) {
	var players []models.Player
	err := r.db.Query(ctx, &players,
		"SELECT * FROM outofline_players WHERE room_id = $1 ORDER BY seat", roomID)
	return players, err
}

func (r *repo) UpdatePlayerHand(ctx context.Context, playerID, hand string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE outofline_players SET hand = $1::jsonb WHERE id = $2", hand, playerID)
	return err
}

func (r *repo) SetPlayerScore(ctx context.Context, playerID string, score int) error {
	_, err := r.db.Exec(ctx,
		"UPDATE outofline_players SET score = $1 WHERE id = $2", score, playerID)
	return err
}

func (r *repo) ResetPlayers(ctx context.Context, roomID string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE outofline_players SET score = 0, hand = '[]'::jsonb WHERE room_id = $1", roomID)
	return err
}

func (r *repo) DeletePlayer(ctx context.Context, roomID string, seat int) error {
	_, err := r.db.Exec(ctx,
		"DELETE FROM outofline_players WHERE room_id = $1 AND seat = $2", roomID, seat)
	return err
}

func (r *repo) CreateRound(ctx context.Context, round *models.Round) error {
	return r.db.Insert(ctx, RoundsTable, round)
}

func (r *repo) GetRound(ctx context.Context, roomID string, roundNo int) (*models.Round, error) {
	var round models.Round
	err := r.db.QueryOne(ctx, &round,
		"SELECT * FROM outofline_rounds WHERE room_id = $1 AND round_no = $2", roomID, roundNo)
	return &round, err
}

func (r *repo) UpdateRoundWinner(ctx context.Context, roundID, submissionID string) error {
	_, err := r.db.Exec(ctx,
		"UPDATE outofline_rounds SET winner_submission_id = $1 WHERE id = $2", submissionID, roundID)
	return err
}

func (r *repo) DeleteRounds(ctx context.Context, roomID string) error {
	_, err := r.db.Exec(ctx, "DELETE FROM outofline_rounds WHERE room_id = $1", roomID)
	return err
}

func (r *repo) CreateSubmission(ctx context.Context, submission *models.Submission) error {
	return r.db.Insert(ctx, SubmissionsTable, submission)
}

func (r *repo) GetSubmission(ctx context.Context, id string) (*models.Submission, error) {
	var submission models.Submission
	err := r.db.QueryOne(ctx, &submission,
		"SELECT * FROM outofline_submissions WHERE id = $1", id)
	return &submission, err
}

func (r *repo) GetSubmissionBySeat(ctx context.Context, roundID string, seat int) (*models.Submission, error) {
	var submission models.Submission
	err := r.db.QueryOne(ctx, &submission,
		"SELECT * FROM outofline_submissions WHERE round_id = $1 AND seat = $2", roundID, seat)
	return &submission, err
}

func (r *repo) ListSubmissions(ctx context.Context, roundID string) ([]models.Submission, error) {
	var submissions []models.Submission
	err := r.db.Query(ctx, &submissions,
		"SELECT * FROM outofline_submissions WHERE round_id = $1 ORDER BY display_order, created_at", roundID)
	return submissions, err
}
