package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

func TestTextOfReducesMediaToPlaceholders(t *testing.T) {
	cases := []struct {
		msg        *waE2E.Message
		kind, text string
	}{
		{&waE2E.Message{Conversation: proto.String("hi")}, "text", "hi"},
		{&waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("link")}}, "text", "link"},
		{&waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}, "image", "[image]"},
		{&waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("poster")}}, "image", "[image] poster"},
		{&waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("When?"),
			Options: []*waE2E.PollCreationMessage_Option{{OptionName: proto.String("Mon")}, {OptionName: proto.String("Tue")}}}},
			"poll", "[poll] When?: Mon / Tue"},
		{&waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{}}, "", ""},
		{nil, "", ""},
	}
	for _, c := range cases {
		if kind, text := textOf(c.msg); kind != c.kind || text != c.text {
			t.Errorf("textOf(%v) = %q,%q want %q,%q", c.msg, kind, text, c.kind, c.text)
		}
	}
}

func TestStoreEditDeleteAndReactions(t *testing.T) {
	ctx := context.Background()
	s, err := OpenStore(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Unix(1_700_000_000, 0)
	chat := "120363000000000001@g.us"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.PutMessage(ctx, Msg{ChatJID: chat, ID: "A", Sender: "111", Kind: "text", Text: "first", At: at}))
	must(s.PutMessage(ctx, Msg{ChatJID: chat, ID: "A", Sender: "111", Kind: "text", Text: "first", At: at})) // replay is idempotent
	must(s.EditMessage(ctx, chat, "A", "text", "edited"))
	must(s.PutReaction(ctx, chat, "A", "222", "❤️", at))
	must(s.PutReaction(ctx, chat, "A", "222", "👏", at.Add(time.Minute))) // latest wins
	must(s.PutReaction(ctx, chat, "A", "222", "🙈", at))                  // stale update ignored
	must(s.PutReaction(ctx, chat, "A", "", "👍", at))                     // from me

	var text string
	var n int
	must(s.db.QueryRow(`SELECT text, (SELECT COUNT(*) FROM messages) FROM messages WHERE id = 'A'`).Scan(&text, &n))
	if text != "edited" || n != 1 {
		t.Fatalf("got text=%q count=%d", text, n)
	}
	var emoji string
	must(s.db.QueryRow(`SELECT emoji FROM reactions WHERE target_id = 'A' AND sender = '222'`).Scan(&emoji))
	if emoji != "👏" {
		t.Fatalf("reaction = %q, want 👏", emoji)
	}
	must(s.PutReaction(ctx, chat, "A", "222", "", at.Add(2*time.Minute))) // removed
	must(s.db.QueryRow(`SELECT COUNT(*) FROM reactions WHERE target_id = 'A'`).Scan(&n))
	if n != 1 {
		t.Fatalf("after removal %d reactions, want 1 (mine)", n)
	}
	must(s.DeleteMessage(ctx, chat, "A"))
	must(s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM messages) + (SELECT COUNT(*) FROM reactions)`).Scan(&n))
	if n != 0 {
		t.Fatalf("delete-for-everyone left %d rows", n)
	}
}

func TestAllowlistReloadsOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write := func(body string) {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("[[groups]]\njid = \"1@g.us\"\nlabel = \"A\"\n")
	a, err := LoadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Allows("1@g.us") || a.Allows("2@g.us") {
		t.Fatal("initial allowlist wrong")
	}
	write("[bridge]\nhistory_days = 30\n[[groups]]\njid = \"2@g.us\"\nlabel = \"B\"\n")
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(path, future, future) // make sure the mtime differs even within the same second
	a.seen = time.Time{}             // force the periodic check
	if a.Allows("1@g.us") || !a.Allows("2@g.us") || a.HistoryDays != 30 {
		t.Fatal("allowlist did not reload")
	}
}
