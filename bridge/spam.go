package main

import (
	"context"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/appstate"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Spam filter. With [bridge] delete_spam = true, a message that looks like ticket-resale or
// get-rich spam, posted to a community's announcements group or any of its subgroups (or to a
// group listed in [bridge] spam_groups), is
// deleted for me: removed from this account on every device, as if deleted by hand on the phone.
// So is the "deleted by admin" placeholder an admin leaves behind there. Nothing changes for
// other members. It runs on any joined community, allowlisted or not, but
// keeps nothing: the log names only the group, the message id and the rule that matched.

var (
	ticketWord = regexp.MustCompile(`(?i)\btickets?\b`)
	// someone selling privately: asks to be contacted, or has spare tickets of their own
	privateSale = regexp.MustCompile(`(?i)\b(dm|pm|inbox|message|text|whatsapp|hmu)\s*me\b|` +
		`\bi\s*have\s*(\d+|two|three|four|a few|some|spare)\b|\b(i'?m|i am)\s*selling\b|\bselling\s*my\b|` +
		`\bcan'?t\s*(make|go|attend)\b|\bspare\b|wa\.me/|\+?\d[\d \-]{9,}\d`)
	// a society's own ticket post links to the place tickets are sold
	officialSale = regexp.MustCompile(`(?i)fixr\.co|studentsunionucl\.org|ucl\.ac\.uk|eventbrite|dice\.fm|ticketmaster|seetickets|luma\.com|lu\.ma/|meetup\.com`)
	money        = regexp.MustCompile(`(?i)\b(crypto|bitcoin|btc|forex|binary options|investment|trading account)\b`)
	moneyBait    = regexp.MustCompile(`(?i)\b(earn|profit|returns|income|payout)\b|[£$€]\s*\d`)
	// get-rich spam wants you to get in touch; a talk about a fintech company doesn't
	moneyHook = regexp.MustCompile(`(?i)\b(dm|pm|inbox|message|text|whatsapp|hmu)\s*me\b|\bask\s*me\b|\bcontact\s*me\b|` +
		`telegram|t\.me/|wa\.me/|\binterested\s*\?|\+?\d[\d \-]{9,}\d`)
	giveaway = regexp.MustCompile(`(?i)\b(giving away|selling)\s*(my|a|an)?\s*(macbook|iphone|ipad|ps5|playstation|xbox|airpods)\b`)
)

// spamRule names the rule a message's text breaks, or "" if it looks fine.
func spamRule(text string) string {
	switch {
	case strings.TrimSpace(text) == "":
		return ""
	case ticketWord.MatchString(text) && privateSale.MatchString(text) && !officialSale.MatchString(text):
		return "ticket resale"
	case money.MatchString(text) && moneyBait.MatchString(text) && moneyHook.MatchString(text) && !officialSale.MatchString(text):
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
	if !b.allow.SpamGroup(info.Chat.String()) {
		if watched, err := b.store.SpamWatched(ctx, info.Chat.String()); err != nil || !watched {
			return
		}
	}
	id, sender, at, rule := info.ID, info.Sender, info.Timestamp, ""
	if target, author, ok := adminRevoke(evt); ok {
		id, rule = target, "deleted by admin"
		var seen bool
		sender, at, seen = b.recent.get(info.Chat, target)
		how := "as it arrived"
		if !seen {
			how = "guessed"
			// Not seen arriving (sent before the bridge started, or too long ago): use the author
			// from the revoke, as a LID if we know one, since covered groups address by LID.
			sender, at = author, info.Timestamp
			if author.Server == types.DefaultUserServer {
				if lid, err := b.cli.Store.LIDs.GetLIDForPN(ctx, author); err == nil && !lid.IsEmpty() {
					sender = lid
				}
			}
		}
		// Diagnostics while it's unproven: which sender form and time the delete-for-me names.
		log.Printf("admin deleted %s in %s: author %s from revoke, using sender %s at %s (%s)",
			id, info.Chat, author.Server, sender.Server, at.Format("15:04:05"), how)
	} else {
		b.recent.put(info.Chat, info.ID, info.Sender, info.Timestamp)
		_, text := textOf(evt.Message)
		rule = spamRule(text)
	}
	if rule == "" {
		return
	}
	if err := b.cli.SendAppState(ctx, buildDeleteForMe(info.Chat, sender, id, at)); err != nil {
		log.Printf("deleting spam %s in %s: %v", id, info.Chat, err)
		return
	}
	log.Printf("deleted spam %s in %s for me (%s)", id, info.Chat, rule)
}

// recentMessages remembers, in memory only, the sender and time of each message that arrived in a
// covered group in the last two days, so that the delete-for-me of a message an admin later
// deletes names it exactly as the phone has it. No text is kept.
type recentMessages struct {
	mu   sync.Mutex
	seen map[string]recentMessage
}

type recentMessage struct {
	sender types.JID
	at     time.Time
}

const recentFor = 48 * time.Hour

func (r *recentMessages) put(chat types.JID, id types.MessageID, sender types.JID, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]recentMessage{}
	}
	if len(r.seen)%1000 == 999 {
		for k, m := range r.seen {
			if time.Since(m.at) > recentFor {
				delete(r.seen, k)
			}
		}
	}
	r.seen[chat.String()+"/"+id] = recentMessage{sender, at}
}

func (r *recentMessages) get(chat types.JID, id types.MessageID) (types.JID, time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.seen[chat.String()+"/"+id]
	return m.sender, m.at, ok
}

// adminRevoke recognises an admin deleting someone else's message, which leaves a "deleted by
// admin" placeholder, and returns that message's id and author. A sender deleting their own
// message, or anyone deleting ours, doesn't count.
func adminRevoke(evt *events.Message) (types.MessageID, types.JID, bool) {
	pm := evt.Message.GetProtocolMessage()
	if pm.GetType() != waE2E.ProtocolMessage_REVOKE || pm.GetKey().GetFromMe() {
		return "", types.JID{}, false
	}
	author, err := types.ParseJID(pm.GetKey().GetParticipant())
	if err != nil || author.User == "" || author.User == evt.Info.Sender.User || author.User == evt.Info.SenderAlt.User {
		return "", types.JID{}, false
	}
	return pm.GetKey().GetID(), author, true
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
