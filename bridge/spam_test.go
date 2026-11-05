package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func TestSpamRule(t *testing.T) {
	for text, want := range map[string]string{
		"Selling 2 Olivia Rodrigo tickets for the O2, can't make it anymore, DM me":             "ticket resale",
		"I have 3 tickets for Sabrina Carpenter on Friday, message me if interested":            "ticket resale",
		"spare tickets for wireless, wa.me/447700900123":                                        "ticket resale",
		"Made £4000 last week with crypto trading, ask me how":                                  "money",
		"Giving away my MacBook Pro 2023, first to text me gets it":                             "giveaway",
		"Winter Ball tickets are on sale now! https://fixr.co/event/123 - DM me with questions": "",
		"Who's going to the social tonight?":                                                    "",
		"Reminder: AGM tickets are free, sign up on the SU site":                                "",
		"": "",
	} {
		if got := spamRule(text); got != want {
			t.Errorf("spamRule(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestBuildDeleteForMe(t *testing.T) {
	chat, _ := types.ParseJID("120363000000000000@g.us")
	sender, _ := types.ParseJID("12345:7@lid")
	p := buildDeleteForMe(chat, sender, "ABC", time.Unix(1700000000, 0))
	idx := p.Mutations[0].Index
	if len(idx) != 5 || idx[0] != "deleteMessageForMe" || idx[1] != chat.String() || idx[2] != "ABC" || idx[3] != "0" || idx[4] != "12345@lid" {
		t.Fatalf("index %v", idx)
	}
	if a := p.Mutations[0].Value.GetDeleteMessageForMeAction(); a.GetMessageTimestamp() != 1700000000 {
		t.Fatalf("action %v", a)
	}
}

func TestAdminRevoke(t *testing.T) {
	admin, _ := types.ParseJID("111@lid")
	adminPN, _ := types.ParseJID("447700900111@s.whatsapp.net")
	revoke := func(participant string, fromMe bool) *events.Message {
		return &events.Message{
			Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: admin, SenderAlt: adminPN}},
			Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
				Type: waE2E.ProtocolMessage_REVOKE.Enum(),
				Key:  &waCommon.MessageKey{ID: proto.String("ORIG"), Participant: proto.String(participant), FromMe: proto.Bool(fromMe)},
			}},
		}
	}
	if id, author, ok := adminRevoke(revoke("222:3@lid", false)); !ok || id != "ORIG" || author.User != "222" {
		t.Fatalf("admin delete: %v %v %v", id, author, ok)
	}
	for name, evt := range map[string]*events.Message{
		"own delete":      revoke("111@lid", false),
		"own delete (PN)": revoke("447700900111@s.whatsapp.net", false),
		"our message":     revoke("222@lid", true),
		"no participant":  revoke("", false),
		"not a revoke":    {Info: types.MessageInfo{MessageSource: types.MessageSource{Sender: admin}}, Message: &waE2E.Message{Conversation: proto.String("hi")}},
	} {
		if _, _, ok := adminRevoke(evt); ok {
			t.Errorf("%s: counted as an admin delete", name)
		}
	}
}

func TestRecentMessages(t *testing.T) {
	var r recentMessages
	chat, _ := types.ParseJID("120363000000000000@g.us")
	sender, _ := types.ParseJID("222@lid")
	at := time.Unix(1700000000, 0)
	r.put(chat, "ORIG", sender, at)
	if s, a, ok := r.get(chat, "ORIG"); !ok || s != sender || !a.Equal(at) {
		t.Fatalf("got %v %v %v", s, a, ok)
	}
	if _, _, ok := r.get(chat, "OTHER"); ok {
		t.Fatal("unknown message found")
	}
}
