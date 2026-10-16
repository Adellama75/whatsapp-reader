package main

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// textOf reduces a WhatsApp message to the one line a transcript shows.
//
// Media is reduced to a placeholder plus its caption, so a digest can still see
// that "Member-ab12 sent a poster" without the file ever being downloaded.
// Returns "" for anything that isn't content (receipts, key distribution, ...).
func textOf(m *waE2E.Message) (kind, text string) {
	switch {
	case m == nil:
		return "", ""
	case m.GetConversation() != "":
		return "text", m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		return "text", m.GetExtendedTextMessage().GetText()
	case m.GetImageMessage() != nil:
		return "image", placeholder("image", m.GetImageMessage().GetCaption())
	case m.GetVideoMessage() != nil:
		return "video", placeholder("video", m.GetVideoMessage().GetCaption())
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		return "document", placeholder("file "+d.GetFileName(), d.GetCaption())
	case m.GetAudioMessage() != nil:
		return "audio", "[voice note]"
	case m.GetStickerMessage() != nil:
		return "sticker", "[sticker]"
	case m.GetLocationMessage() != nil:
		l := m.GetLocationMessage()
		return "location", placeholder("location", strings.TrimSpace(l.GetName()+" "+l.GetAddress()))
	case m.GetContactMessage() != nil:
		return "contact", "[contact card]"
	case m.GetEventMessage() != nil:
		e := m.GetEventMessage()
		return "event", placeholder("event: "+e.GetName(), e.GetDescription())
	}
	if p := pollOf(m); p != nil {
		opts := make([]string, 0, len(p.GetOptions()))
		for _, o := range p.GetOptions() {
			opts = append(opts, o.GetOptionName())
		}
		return "poll", fmt.Sprintf("[poll] %s: %s", p.GetName(), strings.Join(opts, " / "))
	}
	return "", ""
}

func pollOf(m *waE2E.Message) *waE2E.PollCreationMessage {
	for _, p := range []*waE2E.PollCreationMessage{
		m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6(),
	} {
		if p != nil {
			return p
		}
	}
	if v4 := m.GetPollCreationMessageV4(); v4 != nil {
		return pollOf(v4.GetMessage())
	}
	return nil
}

func placeholder(kind, caption string) string {
	if caption == "" {
		return "[" + kind + "]"
	}
	return "[" + kind + "] " + caption
}

// replyTo is the ID of the message this one quotes, if any.
func replyTo(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	for _, ci := range []*waE2E.ContextInfo{
		m.GetExtendedTextMessage().GetContextInfo(),
		m.GetImageMessage().GetContextInfo(),
		m.GetVideoMessage().GetContextInfo(),
		m.GetDocumentMessage().GetContextInfo(),
		m.GetAudioMessage().GetContextInfo(),
		m.GetStickerMessage().GetContextInfo(),
	} {
		if ci.GetStanzaID() != "" {
			return ci.GetStanzaID()
		}
	}
	return ""
}
