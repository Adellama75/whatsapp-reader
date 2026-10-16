// wareader-bridge: a linked WhatsApp device that archives allowlisted group
// chats into data/live.db for the wareader CLI.
//
// It speaks the WhatsApp Web multi-device protocol through whatsmeow, so it
// sees messages the moment they arrive (and gets a backlog via history sync
// when first paired). Nothing is sent to WhatsApp except what the protocol
// needs to stay connected; it never posts, reads receipts are not sent.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// Exit codes systemd must not retry on (RestartPreventExitStatus in the unit).
const exitNeedsPairing = 3

type bridge struct {
	cli   *whatsmeow.Client
	store *Store
	allow *Allowlist
}

func main() {
	dataDir := flag.String("data", "data", "directory for session.db and live.db")
	configPath := flag.String("config", "config.toml", "wareader config (only [[groups]] and [bridge] are read)")
	pair := flag.Bool("pair", false, "show a QR code to link this device, then exit")
	flag.Parse()
	log.SetFlags(log.Ltime)

	if err := run(*dataDir, *configPath, *pair); err != nil {
		log.Fatal(err)
	}
}

func run(dataDir, configPath string, pair bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	allow, err := LoadAllowlist(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	st, err := OpenStore(filepath.Join(dataDir, "live.db"))
	if err != nil {
		return fmt.Errorf("live.db: %w", err)
	}
	defer st.Close()

	store.DeviceProps.Os = proto.String("wareader")
	if allow.HistoryDays > 0 {
		// Ask the phone for a longer backlog than the default at pairing time.
		store.DeviceProps.RequireFullSync = proto.Bool(true)
		store.DeviceProps.HistorySyncConfig.FullSyncDaysLimit = proto.Uint32(allow.HistoryDays)
	}
	session := filepath.Join(dataDir, "session.db")
	container, err := sqlstore.New(ctx, "sqlite", "file:"+session+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)", waLog.Noop)
	if err != nil {
		return fmt.Errorf("session.db: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}

	b := &bridge{cli: whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false)), store: st, allow: allow}
	connected := make(chan struct{}, 1)
	b.cli.AddEventHandler(func(evt any) { b.handle(ctx, evt, connected) })

	if b.cli.Store.ID == nil {
		if !pair {
			_ = st.SetStatus(ctx, "state", "unpaired")
			log.Print("not linked to a phone yet: run `wareader link`")
			os.Exit(exitNeedsPairing)
		}
		if err := b.pairWithQR(ctx); err != nil {
			return err
		}
		select { // pairing reconnects; wait for the session to come up so the phone sees the device
		case <-connected:
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
		}
		b.cli.Disconnect()
		fmt.Println("Linked. Start the bridge to begin archiving: systemctl --user start wareader-bridge")
		return nil
	}
	if pair {
		fmt.Println("Already linked as", b.cli.Store.ID.ToNonAD(), "- delete data/session.db to link again.")
		return nil
	}

	if err := b.cli.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	log.Print("connected as ", b.cli.Store.ID.ToNonAD())

	refresh := time.NewTicker(time.Hour)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = st.SetStatus(context.Background(), "state", "stopped")
			b.cli.Disconnect()
			return nil
		case <-refresh.C:
			b.refreshGroups(ctx)
		}
	}
}

func (b *bridge) pairWithQR(ctx context.Context) error {
	qr, err := b.cli.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := b.cli.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	for item := range qr {
		switch item.Event {
		case "code":
			fmt.Println("\nOn the phone: WhatsApp > ⋮ > Linked devices > Link a device, then scan:")
			qrterminal.GenerateHalfBlock(item.Code, qrterminal.L, os.Stdout)
			fmt.Printf("(this code is valid for %s; a fresh one follows if it expires)\n", item.Timeout.Round(time.Second))
		case whatsmeow.QRChannelSuccess.Event:
			fmt.Println("Paired.")
			return nil
		default:
			return fmt.Errorf("pairing failed: %s %v", item.Event, item.Error)
		}
	}
	return fmt.Errorf("pairing cancelled")
}

func (b *bridge) handle(ctx context.Context, evt any, connected chan struct{}) {
	switch e := evt.(type) {
	case *events.Connected:
		_ = b.store.SetStatus(ctx, "state", "connected")
		_ = b.store.SetStatus(ctx, "since", strconv.FormatInt(time.Now().Unix(), 10))
		select {
		case connected <- struct{}{}:
		default:
		}
		go b.refreshGroups(ctx)
	case *events.Disconnected:
		_ = b.store.SetStatus(ctx, "state", "disconnected")
	case *events.LoggedOut:
		// The phone unlinked us. systemd must not restart into a loop: see exitNeedsPairing.
		_ = b.store.SetStatus(ctx, "state", "unpaired")
		log.Printf("logged out by the phone (%v); run `wareader link` to pair again", e.Reason)
		os.Exit(exitNeedsPairing)
	case *events.OfflineSyncCompleted:
		log.Printf("caught up: %d messages queued while offline", e.Count)
	case *events.HistorySync:
		b.historySync(ctx, e)
	case *events.Message:
		b.message(ctx, e)
	}
}

func (b *bridge) refreshGroups(ctx context.Context) {
	infos, err := b.cli.GetJoinedGroups(ctx)
	if err != nil {
		log.Print("listing groups: ", err)
		return
	}
	groups := make([]Group, 0, len(infos))
	for _, g := range infos {
		groups = append(groups, Group{
			JID: g.JID.String(), Subject: g.Name, Kind: groupKind(g),
			ParentJID: jidOrEmpty(g.LinkedParentJID), Members: max(g.ParticipantCount, len(g.Participants)),
		})
	}
	if err := b.store.ReplaceGroups(ctx, groups); err != nil {
		log.Print("saving groups: ", err)
	}
}

func groupKind(g *types.GroupInfo) string {
	switch {
	case g.IsParent:
		return "community"
	case g.IsDefaultSubGroup:
		return "announcements"
	case !g.LinkedParentJID.IsEmpty():
		return "subgroup"
	default:
		return "group"
	}
}

func jidOrEmpty(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.String()
}

// historySync replays the backlog the phone sends after pairing through the
// same path as live messages, so the store never sees two formats.
func (b *bridge) historySync(ctx context.Context, e *events.HistorySync) {
	for _, pn := range e.Data.GetPushnames() {
		if j, err := types.ParseJID(pn.GetID()); err == nil {
			_ = b.store.PutPushName(ctx, b.canonical(ctx, j, types.EmptyJID), pn.GetPushname())
		}
	}
	kept, skipped := 0, 0
	for _, conv := range e.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil || chat.Server != types.GroupServer || !b.allow.Allows(chat.String()) {
			skipped += len(conv.GetMessages())
			continue
		}
		for _, hm := range conv.GetMessages() {
			evt, err := b.cli.ParseWebMessage(chat, hm.GetMessage())
			if err != nil {
				continue
			}
			b.message(ctx, evt)
			kept++
		}
	}
	log.Printf("history sync (%s): stored %d messages from allowlisted groups, ignored %d", e.Data.GetSyncType(), kept, skipped)
}

func (b *bridge) message(ctx context.Context, evt *events.Message) {
	info := evt.Info
	if info.Chat.Server != types.GroupServer {
		return // direct messages are never stored
	}
	chat := info.Chat.String()
	if !b.allow.Allows(chat) {
		return
	}
	sender := ""
	if !info.IsFromMe {
		sender = b.canonical(ctx, info.Sender, info.SenderAlt)
		if info.PushName != "" {
			_ = b.store.PutPushName(ctx, sender, info.PushName)
		}
	}
	m := evt.Message
	var err error
	switch {
	case m.GetReactionMessage() != nil:
		r := m.GetReactionMessage()
		err = b.store.PutReaction(ctx, chat, r.GetKey().GetID(), sender, r.GetText(), info.Timestamp)
	case m.GetProtocolMessage() != nil:
		pm := m.GetProtocolMessage()
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			err = b.store.DeleteMessage(ctx, chat, pm.GetKey().GetID())
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			if kind, text := textOf(pm.GetEditedMessage()); text != "" {
				err = b.store.EditMessage(ctx, chat, pm.GetKey().GetID(), kind, text)
			}
		}
	default:
		kind, text := textOf(m)
		if text == "" {
			return
		}
		err = b.store.PutMessage(ctx, Msg{
			ChatJID: chat, ID: info.ID, Sender: sender, FromMe: info.IsFromMe,
			At: info.Timestamp, Kind: kind, Text: text, ReplyTo: replyTo(m),
		})
	}
	if err != nil {
		log.Printf("storing %s in %s: %v", info.ID, chat, err)
	}
}

// canonical picks one stable identifier per person. WhatsApp now addresses
// most group members by LID (a random per-account ID) and sometimes by phone
// number; the same person must get the same pseudonym either way, so prefer
// the LID whenever it is known.
func (b *bridge) canonical(ctx context.Context, sender, alt types.JID) string {
	sender, alt = sender.ToNonAD(), alt.ToNonAD()
	switch {
	case sender.Server == types.HiddenUserServer:
		return sender.User
	case alt.Server == types.HiddenUserServer:
		return alt.User
	}
	if lid, err := b.cli.Store.LIDs.GetLIDForPN(ctx, sender); err == nil && !lid.IsEmpty() {
		return lid.User
	}
	return sender.User
}
