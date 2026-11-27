-- Active: 1780750071330@@postgres@5432
-- 004_outofline_schema.sql — Out of Line's schema inside the shared games
-- database.
--
-- Every table is namespaced `outofline_*`, following the Millionaire pattern,
-- so a query from one game cannot accidentally read another game's data. The
-- embedded deck content is NOT stored here: prompts and answers are compiled
-- into the API binary. These tables hold only room, seat, hand, round, and
-- submission state.
--
-- Idempotent for re-apply (DROP + CREATE), matching the existing fresh-database
-- migration path.

DROP TABLE IF EXISTS outofline_submissions;
DROP TABLE IF EXISTS outofline_rounds;
DROP TABLE IF EXISTS outofline_players;
DROP TABLE IF EXISTS outofline_rooms;

CREATE TABLE outofline_rooms (
    id             uuid PRIMARY KEY,
    code           text NOT NULL,
    status         text NOT NULL DEFAULT 'lobby'
                       CHECK (status IN ('lobby', 'active', 'finished')),
    phase          text NOT NULL DEFAULT 'lobby'
                       CHECK (phase IN ('lobby', 'submitting', 'judging', 'reveal', 'finished')),
    host_seat      int  NOT NULL CHECK (host_seat BETWEEN 1 AND 12),
    -- Selected deck ids, e.g. ["base","group-chat"]. The base deck is
    -- required by the service, not by this column, so content can evolve.
    deck_ids       jsonb NOT NULL DEFAULT '[]'::jsonb,
    target_score   int  NOT NULL DEFAULT 5 CHECK (target_score BETWEEN 3 AND 7),
    judge_seat     int  NOT NULL DEFAULT 0 CHECK (judge_seat BETWEEN 0 AND 12),
    round_no       int  NOT NULL DEFAULT 0 CHECK (round_no >= 0),
    -- Current prompt (content.Prompt shape). '{}' means no active prompt.
    prompt         jsonb NOT NULL DEFAULT '{}'::jsonb,
    -- Remaining prompt ids and answer draw/discard piles. Hands live on
    -- players; these piles never include a card currently in a hand.
    prompt_draw    jsonb NOT NULL DEFAULT '[]'::jsonb,
    answer_draw    jsonb NOT NULL DEFAULT '[]'::jsonb,
    answer_discard jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_outofline_rooms_code_unique
    ON outofline_rooms (code);

CREATE TABLE outofline_players (
    id         uuid PRIMARY KEY,
    room_id    uuid NOT NULL REFERENCES outofline_rooms(id) ON DELETE CASCADE,
    seat       int  NOT NULL CHECK (seat BETWEEN 1 AND 12),
    -- The only credential. A browser refresh/reconnect uses it; it is never
    -- included in another seat's room_state.
    token      uuid NOT NULL,
    name       text NOT NULL,
    score      int  NOT NULL DEFAULT 0 CHECK (score >= 0),
    -- The player's seven-card hand, as an array of {id,text}.
    hand       jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (room_id, seat),
    UNIQUE (token)
);
CREATE INDEX idx_outofline_players_room ON outofline_players (room_id);

CREATE TABLE outofline_rounds (
    id                   uuid PRIMARY KEY,
    room_id              uuid NOT NULL REFERENCES outofline_rooms(id) ON DELETE CASCADE,
    round_no             int  NOT NULL CHECK (round_no >= 1),
    prompt               jsonb NOT NULL,
    judge_seat           int  NOT NULL CHECK (judge_seat BETWEEN 1 AND 12),
    -- Empty string until the judge (or a forced host recovery) picks.
    winner_submission_id text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (room_id, round_no)
);
CREATE INDEX idx_outofline_rounds_room ON outofline_rounds (room_id);

CREATE TABLE outofline_submissions (
    id            uuid PRIMARY KEY,
    round_id      uuid NOT NULL REFERENCES outofline_rounds(id) ON DELETE CASCADE,
    seat          int  NOT NULL CHECK (seat BETWEEN 1 AND 12),
    cards         jsonb NOT NULL,
    -- Random ordering keeps the judge's anonymous grid from leaking seats
    -- through submission order.
    display_order int  NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (round_id, seat)
);
CREATE INDEX idx_outofline_submissions_round ON outofline_submissions (round_id);
