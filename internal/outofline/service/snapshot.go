package service

import (
	"context"
	"sort"

	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/models"
)

// PlayerView is the public scoreboard row. It deliberately contains no hand
// or token.
type PlayerView struct {
	Seat    int    `json:"seat"`
	Name    string `json:"name"`
	Score   int    `json:"score"`
	IsJudge bool   `json:"is_judge"`
}

// SelfView is the private slice of state for one socket. The token is included
// only here so a reconnect after a refresh has a credential.
type SelfView struct {
	Token     string         `json:"token"`
	Seat      int            `json:"seat"`
	Name      string         `json:"name"`
	Hand      []content.Card `json:"hand"`
	IsHost    bool           `json:"is_host"`
	IsJudge   bool           `json:"is_judge"`
	Submitted bool           `json:"submitted"`
}

// SubmissionView is anonymous during judging; seat and name are populated only
// after the judge has picked a winner.
type SubmissionView struct {
	ID     string         `json:"id"`
	Cards  []content.Card `json:"cards"`
	Seat   int            `json:"seat,omitempty"`
	Name   string         `json:"name,omitempty"`
	Winner bool           `json:"winner,omitempty"`
}

type WinnerView struct {
	Seat  int            `json:"seat"`
	Name  string         `json:"name"`
	Cards []content.Card `json:"cards"`
}

type StandingView struct {
	Seat  int    `json:"seat"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

// Snapshot is the personalized room_state payload. The same room produces a
// different snapshot for each seat: only the judge sees anonymous submissions
// during judging, and only the owner sees their hand.
type Snapshot struct {
	Event          string             `json:"event"`
	RoomID         string             `json:"room_id"`
	Code           string             `json:"code"`
	Status         string             `json:"status"`
	Phase          string             `json:"phase"`
	HostSeat       int                `json:"host_seat"`
	JudgeSeat      int                `json:"judge_seat"`
	RoundNo        int                `json:"round_no"`
	TargetScore    int                `json:"target_score"`
	MinPlayers     int                `json:"min_players"`
	MaxPlayers     int                `json:"max_players"`
	Adult          bool               `json:"adult"`
	Decks          []content.DeckInfo `json:"decks"`
	Players        []PlayerView       `json:"players"`
	Prompt         *content.Prompt    `json:"prompt,omitempty"`
	SubmittedCount int                `json:"submitted_count"`
	Needed         int                `json:"needed"`
	Self           SelfView           `json:"self"`
	Submissions    []SubmissionView   `json:"submissions,omitempty"`
	Winner         *WinnerView        `json:"winner,omitempty"`
	Standings      []StandingView     `json:"standings,omitempty"`
	CanStart       bool               `json:"can_start"`
	CanNext        bool               `json:"can_next"`
	CanForce       bool               `json:"can_force"`
	CanRematch     bool               `json:"can_rematch"`
	CanRemove      bool               `json:"can_remove"`
}

func (s *Service) Snapshot(ctx context.Context, roomID string, seat int) (*Snapshot, error) {
	room, err := s.repo.GetRoom(ctx, roomID)
	if err != nil {
		return nil, gameErr(404, "room not found")
	}
	players, err := s.repo.ListPlayers(ctx, room.ID)
	if err != nil {
		return nil, err
	}
	var self *models.Player
	for i := range players {
		if players[i].Seat == seat {
			self = &players[i]
			break
		}
	}
	if self == nil {
		return nil, gameErr(404, "player not found")
	}
	decks, err := s.selectedDecks(room)
	if err != nil {
		return nil, err
	}

	var prompt *content.Prompt
	if room.Prompt != "" && room.Prompt != "{}" {
		parsed, err := parsePrompt(room.Prompt)
		if err != nil {
			return nil, err
		}
		prompt = &parsed
	}

	var round *models.Round
	submissions := []models.Submission{}
	if room.RoundNo > 0 {
		round, err = s.repo.GetRound(ctx, room.ID, room.RoundNo)
		if err != nil {
			return nil, err
		}
		submissions, err = s.repo.ListSubmissions(ctx, round.ID)
		if err != nil {
			return nil, err
		}
	}

	hand, err := parseCards(self.Hand)
	if err != nil {
		return nil, err
	}
	isHost := self.Seat == room.HostSeat
	isJudge := self.Seat == room.JudgeSeat
	submitted := false
	for _, submission := range submissions {
		if submission.Seat == self.Seat {
			submitted = true
			break
		}
	}

	publicPlayers := make([]PlayerView, 0, len(players))
	for _, player := range players {
		publicPlayers = append(publicPlayers, PlayerView{
			Seat:    player.Seat,
			Name:    player.Name,
			Score:   player.Score,
			IsJudge: player.Seat == room.JudgeSeat,
		})
	}

	snapshot := &Snapshot{
		Event:          "room_state",
		RoomID:         room.ID,
		Code:           room.Code,
		Status:         room.Status,
		Phase:          room.Phase,
		HostSeat:       room.HostSeat,
		JudgeSeat:      room.JudgeSeat,
		RoundNo:        room.RoundNo,
		TargetScore:    room.TargetScore,
		MinPlayers:     MinPlayers,
		MaxPlayers:     MaxPlayers,
		Adult:          content.Adult(decks),
		Decks:          deckInfos(decks),
		Players:        publicPlayers,
		Prompt:         prompt,
		SubmittedCount: len(submissions),
		Self: SelfView{
			Token:     self.Token,
			Seat:      self.Seat,
			Name:      self.Name,
			Hand:      hand,
			IsHost:    isHost,
			IsJudge:   isJudge,
			Submitted: submitted,
		},
		CanStart:   isHost && room.Status == "lobby" && len(players) >= MinPlayers,
		CanNext:    (isHost || isJudge) && room.Status == "active" && room.Phase == "reveal",
		CanForce:   isHost && room.Status == "active" && (room.Phase == "submitting" || room.Phase == "judging" || room.Phase == "reveal"),
		CanRematch: isHost && room.Status == "finished",
		CanRemove:  isHost && room.Status == "lobby",
	}
	if room.Status == "active" && room.RoundNo > 0 {
		snapshot.Needed = len(players) - 1
	}

	if round != nil && (room.Phase == "judging" || room.Phase == "reveal" || room.Phase == "finished") {
		if room.Phase == "judging" && !isJudge {
			// Non-judges see only the count while judging.
		} else {
			bySeat := make(map[int]models.Player, len(players))
			for _, player := range players {
				bySeat[player.Seat] = player
			}
			views := make([]SubmissionView, 0, len(submissions))
			for _, submission := range submissions {
				cards, err := parseCards(submission.Cards)
				if err != nil {
					return nil, err
				}
				view := SubmissionView{
					ID:     submission.ID,
					Cards:  cards,
					Winner: submission.ID == round.WinnerSubmissionID,
				}
				if room.Phase != "judging" {
					view.Seat = submission.Seat
					if player, ok := bySeat[submission.Seat]; ok {
						view.Name = player.Name
					}
				}
				views = append(views, view)
			}
			snapshot.Submissions = views
		}
	}

	if round != nil && round.WinnerSubmissionID != "" && (room.Phase == "reveal" || room.Phase == "finished") {
		for _, submission := range submissions {
			if submission.ID != round.WinnerSubmissionID {
				continue
			}
			cards, err := parseCards(submission.Cards)
			if err != nil {
				return nil, err
			}
			winner := &WinnerView{Seat: submission.Seat, Cards: cards}
			for _, player := range players {
				if player.Seat == submission.Seat {
					winner.Name = player.Name
					break
				}
			}
			snapshot.Winner = winner
			break
		}
	}

	if room.Status == "finished" {
		standings := make([]StandingView, 0, len(players))
		for _, player := range players {
			standings = append(standings, StandingView{Seat: player.Seat, Name: player.Name, Score: player.Score})
		}
		sort.SliceStable(standings, func(i, j int) bool {
			if standings[i].Score != standings[j].Score {
				return standings[i].Score > standings[j].Score
			}
			return standings[i].Seat < standings[j].Seat
		})
		snapshot.Standings = standings
	}

	return snapshot, nil
}
