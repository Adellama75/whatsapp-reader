package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
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
