package models

import "time"

// Room is one Out of Line lobby/game. JSON columns are kept as text here so
// ksql can pass them through to Postgres jsonb columns, the same pattern the
// Boggle module uses for its board cells.
type Room struct {
	ID            string    `ksql:"id" json:"id"`
	Code          string    `ksql:"code" json:"code"`
	Status        string    `ksql:"status" json:"status"`
	Phase         string    `ksql:"phase" json:"phase"`
	HostSeat      int       `ksql:"host_seat" json:"host_seat"`
	DeckIDs       string    `ksql:"deck_ids" json:"-"`
	TargetScore   int       `ksql:"target_score" json:"target_score"`
	JudgeSeat     int       `ksql:"judge_seat" json:"judge_seat"`
	RoundNo       int       `ksql:"round_no" json:"round_no"`
	Prompt        string    `ksql:"prompt" json:"-"`
	PromptDraw    string    `ksql:"prompt_draw" json:"-"`
	AnswerDraw    string    `ksql:"answer_draw" json:"-"`
	AnswerDiscard string    `ksql:"answer_discard" json:"-"`
	CreatedAt     time.Time `ksql:"created_at" json:"created_at"`
	UpdatedAt     time.Time `ksql:"updated_at" json:"updated_at"`
}

// Player is one seat in a room. The token is the only credential: there are
// no accounts, and a token is never included in another player's snapshot.
type Player struct {
	ID        string    `ksql:"id" json:"id"`
	RoomID    string    `ksql:"room_id" json:"room_id"`
	Seat      int       `ksql:"seat" json:"seat"`
	Token     string    `ksql:"token" json:"-"`
	Name      string    `ksql:"name" json:"name"`
	Score     int       `ksql:"score" json:"score"`
	Hand      string    `ksql:"hand" json:"-"`
	CreatedAt time.Time `ksql:"created_at" json:"created_at"`
}

// Round is the historical record of one prompt/judge cycle.
type Round struct {
	ID                 string    `ksql:"id" json:"id"`
	RoomID             string    `ksql:"room_id" json:"room_id"`
	RoundNo            int       `ksql:"round_no" json:"round_no"`
	Prompt             string    `ksql:"prompt" json:"-"`
	JudgeSeat          int       `ksql:"judge_seat" json:"judge_seat"`
	WinnerSubmissionID string    `ksql:"winner_submission_id" json:"winner_submission_id"`
	CreatedAt          time.Time `ksql:"created_at" json:"created_at"`
}

// Submission is one player's anonymous entry for a round.
type Submission struct {
	ID           string    `ksql:"id" json:"id"`
	RoundID      string    `ksql:"round_id" json:"round_id"`
	Seat         int       `ksql:"seat" json:"seat"`
	Cards        string    `ksql:"cards" json:"-"`
	DisplayOrder int       `ksql:"display_order" json:"display_order"`
	CreatedAt    time.Time `ksql:"created_at" json:"created_at"`
}
