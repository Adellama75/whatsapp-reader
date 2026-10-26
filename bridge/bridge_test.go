package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
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
	if n, err := s.EditMessage(ctx, chat, "A", "text", "edited"); err != nil || n != 1 {
		t.Fatal("edit:", n, err)
	}
	if n, err := s.EditMessage(ctx, chat, "nope", "text", "x"); err != nil || n != 0 {
		t.Fatal("edit of unknown id should match 0 rows:", n, err)
	}
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

func TestApplyEditAcceptsBareAndWrappedContent(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	chat := "120363000000000001@g.us"
	if err := st.PutMessage(ctx, Msg{ChatJID: chat, ID: "A", FromMe: true, Kind: "text", Text: "edit v1", At: time.Unix(1_700_000_000, 0)}); err != nil {
		t.Fatal(err)
	}
	b := &bridge{store: st}
	text := func() string {
		var s string
		if err := st.db.QueryRowContext(ctx, "SELECT text FROM messages WHERE id='A'").Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	// A decrypted secret edit carries the new content directly...
	if err := b.applyEdit(ctx, chat, "A", &waE2E.Message{Conversation: proto.String("edit v2")}); err != nil || text() != "edit v2" {
		t.Fatal("bare:", err, text())
	}
	// ...or wrapped in a MESSAGE_EDIT ProtocolMessage, like the older format.
	wrapped := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
		EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("edit v3")}},
	}}
	if err := b.applyEdit(ctx, chat, "A", wrapped); err != nil || text() != "edit v3" {
		t.Fatal("wrapped:", err, text())
	}
	if got := setFields(wrapped); got != "protocolMessage" {
		t.Fatal("setFields:", got)
	}
}

func TestSaveMediaIsPerGroupAndFileNamesAreSafe(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := OpenStore(filepath.Join(dir, "live.db"))
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("[[groups]]\njid = \"1@g.us\"\nlabel = \"A\"\nsave_media = true\n[[groups]]\njid = \"2@g.us\"\nlabel = \"B\"\n"), 0o600)
	allow, _ := LoadAllowlist(cfg)
	if !allow.SavesMedia("1@g.us") || allow.SavesMedia("2@g.us") {
		t.Fatal("save_media not read per group")
	}
	st.PutMessage(ctx, Msg{ChatJID: "1@g.us", ID: "Q1", Sender: "111", At: time.Now(), Kind: "image", Text: "[image]"})
	if err := st.SetFile(ctx, "1@g.us", "Q1", "/x/Q1_a.png"); err != nil {
		t.Fatal(err)
	}
	var file string
	st.db.QueryRow(`SELECT file FROM messages WHERE id = 'Q1'`).Scan(&file)
	if file != "/x/Q1_a.png" {
		t.Fatalf("file = %q", file)
	}
	if safeName("../../etc/passwd") != "passwd" || safeName("my report (v2).pdf") != "my_report__v2_.pdf" {
		t.Fatal("safeName", safeName("../../etc/passwd"), safeName("my report (v2).pdf"))
	}
}

func TestMediaKeysAreKeptForLaterDownload(t *testing.T) {
	ctx := context.Background()
	doc := &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		FileName: proto.String("plan.pdf"), Mimetype: proto.String("application/pdf"), FileLength: proto.Uint64(1234),
		DirectPath: proto.String("/v/t62/x"), MediaKey: []byte("k"), FileSHA256: []byte("h"), FileEncSHA256: []byte("e"),
		Caption: proto.String("see p2"), JPEGThumbnail: []byte("thumb"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q")},
	}}
	media := mediaOf(doc)
	dl, name, size, err := attachment(media)
	if err != nil || name != "plan.pdf" || size != 1234 {
		t.Fatal(name, size, err)
	}
	d := dl.(*waE2E.DocumentMessage)
	if d.GetDirectPath() != "/v/t62/x" || string(d.GetMediaKey()) != "k" || d.Caption != nil || d.JPEGThumbnail != nil || d.ContextInfo != nil {
		t.Fatal("kept the wrong fields:", d)
	}
	if mediaOf(&waE2E.Message{Conversation: proto.String("hi")}) != nil {
		t.Fatal("text has no attachment")
	}

	dir := t.TempDir()
	st, _ := OpenStore(filepath.Join(dir, "live.db"))
	msg := Msg{ChatJID: "1@g.us", ID: "D1", Sender: "111", At: time.Now(), Kind: "document", Text: "[file plan.pdf]"}
	st.PutMessage(ctx, msg) // as stored before the bridge kept keys
	if m, _, _, err := st.MediaOf(ctx, "1@g.us", "D1"); err != nil || m != nil {
		t.Fatal("old message has keys?", m, err)
	}
	msg.Media = media
	st.PutMessage(ctx, msg) // the phone resent it
	msg.Media = nil
	st.PutMessage(ctx, msg) // a later copy without keys keeps them
	if m, sender, _, _ := st.MediaOf(ctx, "1@g.us", "D1"); !bytes.Equal(m, media) || sender != "111" {
		t.Fatal("keys not kept")
	}

	// Requests for groups off the allowlist, or unknown messages, fail without touching WhatsApp.
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("[[groups]]\njid = \"1@g.us\"\nlabel = \"A\"\n"), 0o600)
	allow, _ := LoadAllowlist(cfg)
	b := &bridge{store: st, allow: allow, dataDir: dir}
	for chat, want := range map[string]string{"2@g.us": "not an allowlisted group", "1@g.us": "no message"} {
		req := filepath.Join(dir, "NOPE.want")
		os.WriteFile(req, []byte(chat+"\n"), 0o600)
		if err := b.serveDownload(ctx, req, map[string]time.Time{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: %v", chat, err)
		}
	}
}

func TestDirectChatsAreStoredOnlyWhenListedInEitherForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("[[groups]]\njid = \"1@g.us\"\nlabel = \"G\"\n"+
		"[[groups]]\njid = \"447700900001@s.whatsapp.net\"\nlabel = \"Pat\"\n"+
		"[[groups]]\njid = \"57153196953645@lid\"\nlabel = \"Sam\"\n"), 0o600)
	a, err := LoadAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	pn := func(u string) types.JID { return types.NewJID(u, types.DefaultUserServer) }
	lid := func(u string) types.JID { return types.NewJID(u, types.HiddenUserServer) }
	pairs := map[types.JID]types.JID{ // the session's LID map, both ways
		lid("11111111111111"): pn("447700900001"), pn("447700900001"): lid("11111111111111"),
		pn("447700900002"): lid("57153196953645"), lid("57153196953645"): pn("447700900002"),
	}
	lookup := func(j types.JID) types.JID { return pairs[j] }
	device := types.JID{User: "447700900001", Device: 3, Server: types.DefaultUserServer}
	cases := []struct {
		chat, alt types.JID
		want      string
	}{
		{pn("447700900001"), types.EmptyJID, "447700900001@s.whatsapp.net"},        // listed form
		{lid("11111111111111"), pn("447700900001"), "447700900001@s.whatsapp.net"}, // alt JID on the message
		{lid("11111111111111"), types.EmptyJID, "447700900001@s.whatsapp.net"},     // LID map (history sync)
		{pn("447700900002"), types.EmptyJID, "57153196953645@lid"},                 // listed by LID, arrives by number
		{device, types.EmptyJID, "447700900001@s.whatsapp.net"},                    // device suffix dropped
		{pn("447700900009"), types.EmptyJID, ""},                                   // not listed: never stored
		{lid("99999999999999"), pn("447700900009"), ""},
		{types.NewJID("1", types.GroupServer), types.EmptyJID, ""},              // groups don't go this way
		{types.NewJID("status", types.BroadcastServer), pn("447700900001"), ""}, // nor broadcasts
	}
	for _, c := range cases {
		got, ok := directKey(c.chat, c.alt, a.Allows, lookup)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("directKey(%s, %s) = %q,%v want %q", c.chat, c.alt, got, ok, c.want)
		}
	}
	b := &bridge{allow: a}
	if key, ok := b.chatKey(context.Background(), types.NewJID("1", types.GroupServer), types.EmptyJID); !ok || key != "1@g.us" {
		t.Errorf("group: %q %v", key, ok)
	}
	if _, ok := b.chatKey(context.Background(), types.NewJID("2", types.GroupServer), pn("447700900001")); ok {
		t.Error("an unlisted group must not match through an alt JID")
	}
}
