package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// pollOptions lists a poll's option names, for storing alongside it (votes name options by hash).
func pollOptions(m *waE2E.Message) []string {
	p := pollOf(m)
	if p == nil {
		return nil
	}
	opts := make([]string, 0, len(p.GetOptions()))
	for _, o := range p.GetOptions() {
		opts = append(opts, o.GetOptionName())
	}
	return opts
}

// optionNames maps the SHA-256 hashes a vote carries back to option names. A hash that matches
// none of the options (a poll we never stored) is kept as "?" plus its first 8 hex digits.
func optionNames(selected [][]byte, options []string) []string {
	names := make([]string, 0, len(selected))
	for _, h := range selected {
		name := "?" + hex.EncodeToString(h)[:min(8, 2*len(h))]
		for _, o := range options {
			if sum := sha256.Sum256([]byte(o)); bytes.Equal(sum[:], h) {
				name = o
				break
			}
		}
		names = append(names, name)
	}
	return names
}

// candidateOptions is the option list of a stored poll: the poll_options column, or for polls stored
// before it existed, the options parsed back from the "[poll] name: a / b" text. The question may
// itself contain ": ", so every split is tried and the one whose options match the vote wins.
func candidateOptions(stored []string, text string, selected [][]byte) []string {
	if len(stored) > 0 {
		return stored
	}
	body := strings.TrimPrefix(text, "[poll] ")
	best := []string(nil)
	for i := strings.Index(body, ": "); i >= 0; {
		opts := strings.Split(body[i+2:], " / ")
		matched := 0
		for _, n := range optionNames(selected, opts) {
			if !strings.HasPrefix(n, "?") {
				matched++
			}
		}
		if best == nil || matched == len(selected) {
			best = opts
			if matched == len(selected) {
				break
			}
		}
		j := strings.Index(body[i+2:], ": ")
		if j < 0 {
			break
		}
		i += 2 + j
	}
	return best
}

// pollVote records a decrypted vote (a PollUpdateMessage) on a poll in an archived chat.
func (b *bridge) pollVote(ctx context.Context, chat, voter string, evt *events.Message) error {
	pu := evt.Message.GetPollUpdateMessage()
	pollID := pu.GetPollCreationMessageKey().GetID()
	vote, err := b.cli.DecryptPollVote(ctx, evt)
	if err != nil {
		return fmt.Errorf("vote on poll %s: %w", pollID, err)
	}
	at := evt.Info.Timestamp
	if ms := pu.GetSenderTimestampMS(); ms > 0 {
		at = time.UnixMilli(ms)
	}
	return b.storeVote(ctx, chat, pollID, voter, vote.GetSelectedOptions(), at)
}

// historyVotes records the votes a history sync carries with a poll: already decrypted, keyed by
// the vote message's key (whose sender is the voter).
func (b *bridge) historyVotes(ctx context.Context, chat string, pollID string, updates []*waWeb.PollUpdate) {
	for _, u := range updates {
		voter := b.voterOf(ctx, u.GetPollUpdateMessageKey())
		at := time.UnixMilli(u.GetSenderTimestampMS())
		if err := b.storeVote(ctx, chat, pollID, voter, u.GetVote().GetSelectedOptions(), at); err != nil {
			log.Printf("storing history vote on %s in %s: %v", pollID, chat, err)
		}
	}
}

func (b *bridge) voterOf(ctx context.Context, key *waCommon.MessageKey) string {
	if key.GetFromMe() {
		return ""
	}
	id := key.GetParticipant()
	if id == "" {
		id = key.GetRemoteJID() // a direct chat: the other person
	}
	j, err := types.ParseJID(id)
	if err != nil {
		return id
	}
	return b.canonical(ctx, j, types.EmptyJID)
}

func (b *bridge) storeVote(ctx context.Context, chat, pollID, voter string, selected [][]byte, at time.Time) error {
	stored, text, err := b.store.Poll(ctx, chat, pollID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	names := optionNames(selected, candidateOptions(stored, text, selected))
	return b.store.PutPollVote(ctx, chat, pollID, voter, names, at)
}

// Poll returns a stored poll's option list (nil for polls stored before poll_options existed) and text.
func (s *Store) Poll(ctx context.Context, chat, id string) ([]string, string, error) {
	var raw sql.NullString
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT poll_options, text FROM messages WHERE chat_jid = ? AND id = ?`, chat, id).
		Scan(&raw, &text)
	if err != nil {
		return nil, "", err
	}
	var opts []string
	if raw.Valid {
		_ = json.Unmarshal([]byte(raw.String), &opts)
	}
	return opts, text, nil
}

// PutPollVote records a voter's current selection on a poll; a later vote replaces an earlier one
// and an empty selection (the vote withdrawn) is kept as [], so a stale vote arriving late can't
// bring it back.
func (s *Store) PutPollVote(ctx context.Context, chat, poll, voter string, options []string, at time.Time) error {
	if options == nil {
		options = []string{}
	}
	js, err := json.Marshal(options)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO poll_votes (chat_jid, poll_id, voter, options, ts) VALUES (?,?,?,?,?)
		ON CONFLICT (chat_jid, poll_id, voter) DO UPDATE SET options = excluded.options, ts = excluded.ts
		WHERE excluded.ts >= poll_votes.ts`,
		chat, poll, voter, string(js), at.UnixMilli())
	return err
}
