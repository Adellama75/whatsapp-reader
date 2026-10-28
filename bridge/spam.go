package main

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Spam filter. With [bridge] delete_spam = true, a message that looks like ticket-resale or
// get-rich spam, posted to a community's announcements group or a subgroup named "General", is
// deleted for me: removed from this account on every device, as if deleted by hand on the phone.
// Nothing changes for other members. It runs on any joined community, allowlisted or not, but
// keeps nothing: the log names only the group, the message id and the rule that matched.

var (
	ticketWord = regexp.MustCompile(`(?i)\btickets?\b`)
	// someone selling privately: asks to be contacted, or has spare tickets of their own
	privateSale = regexp.MustCompile(`(?i)\b(dm|pm|inbox|message|text|whatsapp|hmu)\s*me\b|` +
		`\bi\s*have\s*(\d+|two|three|four|a few|some|spare)\b|\b(i'?m|i am)\s*selling\b|\bselling\s*my\b|` +
		`\bcan'?t\s*(make|go|attend)\b|\bspare\b|wa\.me/|\+?\d[\d \-]{9,}\d`)
	// a society's own ticket post links to the place tickets are sold
	officialSale = regexp.MustCompile(`(?i)fixr\.co|studentsunionucl\.org|ucl\.ac\.uk|eventbrite|dice\.fm|ticketmaster|seetickets`)
	money        = regexp.MustCompile(`(?i)\b(crypto|bitcoin|btc|forex|binary options|investment|trading account)\b`)
	moneyBait    = regexp.MustCompile(`(?i)\b(earn|profit|returns|income|payout)\b|[£$€]\s*\d`)
	giveaway     = regexp.MustCompile(`(?i)\b(giving away|selling)\s*(my|a|an)?\s*(macbook|iphone|ipad|ps5|playstation|xbox|airpods)\b`)
)

// spamRule names the rule a message's text breaks, or "" if it looks fine.
func spamRule(text string) string {
	switch {
	case strings.TrimSpace(text) == "":
		return ""
	case ticketWord.MatchString(text) && privateSale.MatchString(text) && !officialSale.MatchString(text):
		return "ticket resale"
	case money.MatchString(text) && moneyBait.MatchString(text):
		return "money"
	case giveaway.MatchString(text):
		return "giveaway"
	}
	return ""
}

// filterSpam deletes a newly arrived spam message for me. History sync backlogs never come
// through here, and nor does anything older than a few minutes (e.g. queued while offline long ago).
func (b *bridge) filterSpam(ctx context.Context, evt *events.Message) {
	info := evt.Info
	if !b.allow.DeletesSpam() || info.IsFromMe || info.Chat.Server != types.GroupServer ||
		time.Since(info.Timestamp) > 10*time.Minute {
		return
	}
	_, text := textOf(evt.Message)
	rule := spamRule(text)
	if rule == "" {
		return
	}
	if watched, err := b.store.SpamWatched(ctx, info.Chat.String()); err != nil || !watched {
		return
	}
	if err := b.cli.SendAppState(ctx, buildDeleteForMe(info.Chat, info.Sender, info.ID, info.Timestamp)); err != nil {
		log.Printf("deleting spam %s in %s: %v", info.ID, info.Chat, err)
		return
	}
	log.Printf("deleted spam %s in %s for me (%s)", info.ID, info.Chat, rule)
}

// buildDeleteForMe is the app state patch the phone sends for "Delete for me" on someone
// else's message in a group (whatsmeow has no builder for it).
func buildDeleteForMe(chat, sender types.JID, id types.MessageID, at time.Time) appstate.PatchInfo {
	return appstate.PatchInfo{
		Type: appstate.WAPatchRegularHigh,
		Mutations: []appstate.MutationInfo{{
			Index:   []string{appstate.IndexDeleteMessageForMe, chat.String(), id, "0", sender.ToNonAD().String()},
			Version: 3,
			Value: &waSyncAction.SyncActionValue{
				DeleteMessageForMeAction: &waSyncAction.DeleteMessageForMeAction{
					DeleteMedia:      new(true),
					MessageTimestamp: new(at.Unix()),
				},
			},
		}},
	}
}
