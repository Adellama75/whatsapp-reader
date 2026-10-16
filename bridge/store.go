package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Store is the local message archive the Python CLI reads (data/live.db).
//
// It holds only allowlisted groups' messages; see Allowlist. The groups table
// is a directory of every joined group (metadata, no content) so that
// `wareader index` can help pick which groups to ask for consent.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS groups (
	jid TEXT PRIMARY KEY, subject TEXT NOT NULL, kind TEXT NOT NULL,
	members INTEGER NOT NULL, parent_jid TEXT, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS messages (
	chat_jid TEXT NOT NULL, id TEXT NOT NULL, sender TEXT NOT NULL, from_me INTEGER NOT NULL,
	ts INTEGER NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, reply_to TEXT,
	PRIMARY KEY (chat_jid, id));
CREATE INDEX IF NOT EXISTS messages_chat_ts ON messages (chat_jid, ts);
CREATE TABLE IF NOT EXISTS reactions (
	chat_jid TEXT NOT NULL, target_id TEXT NOT NULL, sender TEXT NOT NULL,
	emoji TEXT NOT NULL, ts INTEGER NOT NULL,
	PRIMARY KEY (chat_jid, target_id, sender));
CREATE TABLE IF NOT EXISTS people (
	id TEXT PRIMARY KEY, push_name TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS status (key TEXT PRIMARY KEY, value TEXT NOT NULL);
`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite + WAL: one writer, readers (the CLI) use their own connections
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type Group struct {
	JID, Subject, Kind, ParentJID string
	Members                       int
}

// ReplaceGroups makes the directory match the current list of joined groups.
func (s *Store) ReplaceGroups(ctx context.Context, groups []Group) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM groups`); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, g := range groups {
		var parent any
		if g.ParentJID != "" {
			parent = g.ParentJID
		}
		if _, err := tx.Exec(`INSERT INTO groups VALUES (?,?,?,?,?,?)`,
			g.JID, g.Subject, g.Kind, g.Members, parent, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type Msg struct {
	ChatJID, ID, Sender, Kind, Text, ReplyTo string
	FromMe                                   bool
	At                                       time.Time
}

// PutMessage stores a message, or updates its text if it was edited.
func (s *Store) PutMessage(ctx context.Context, m Msg) error {
	var reply any
	if m.ReplyTo != "" {
		reply = m.ReplyTo
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO messages (chat_jid, id, sender, from_me, ts, kind, text, reply_to)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT (chat_jid, id) DO UPDATE SET kind = excluded.kind, text = excluded.text`,
		m.ChatJID, m.ID, m.Sender, m.FromMe, m.At.Unix(), m.Kind, m.Text, reply)
	return err
}

func (s *Store) EditMessage(ctx context.Context, chat, id, kind, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET kind = ?, text = ? WHERE chat_jid = ? AND id = ?`,
		kind, text, chat, id)
	return err
}

// DeleteMessage honours "delete for everyone": the message and its reactions go.
func (s *Store) DeleteMessage(ctx context.Context, chat, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE chat_jid = ? AND id = ?`, chat, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE chat_jid = ? AND target_id = ?`, chat, id)
	return err
}

// PutReaction records a sender's latest reaction to a message; an empty emoji removes it.
func (s *Store) PutReaction(ctx context.Context, chat, target, sender, emoji string, at time.Time) error {
	if emoji == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM reactions WHERE chat_jid = ? AND target_id = ? AND sender = ?`,
			chat, target, sender)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO reactions VALUES (?,?,?,?,?)
		ON CONFLICT (chat_jid, target_id, sender) DO UPDATE SET emoji = excluded.emoji, ts = excluded.ts
		WHERE excluded.ts >= reactions.ts`,
		chat, target, sender, emoji, at.Unix())
	return err
}

func (s *Store) PutPushName(ctx context.Context, id, name string) error {
	if id == "" || name == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO people VALUES (?,?,?)
		ON CONFLICT (id) DO UPDATE SET push_name = excluded.push_name, updated_at = excluded.updated_at`,
		id, name, time.Now().Unix())
	return err
}

func (s *Store) SetStatus(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO status VALUES (?,?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}
