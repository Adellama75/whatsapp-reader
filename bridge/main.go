// whatsapp-reader-bridge: a linked WhatsApp device that archives allowlisted group
// chats into data/live.db for the whatsapp-reader CLI.
//
// It speaks the WhatsApp Web multi-device protocol through whatsmeow, so it
// sees messages the moment they arrive (and gets a backlog via history sync
// when first paired). Nothing is sent to WhatsApp except what the protocol
// needs to stay connected; it never posts, reads receipts are not sent.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	"golang.org/x/term"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Exit codes systemd must not retry on (RestartPreventExitStatus in the unit).
const exitNeedsPairing = 3

type bridge struct {
	cli     *whatsmeow.Client
	store   *Store
	allow   *Allowlist
	dataDir string // attachments of save_media groups go to <dataDir>/attachments
}

// plugins run once the bridge is connected, each in its own goroutine until shutdown. Site-specific
// additions register here from an init() in a bridge/local_*.go file, which .gitignore keeps out of
// the repo; a plain checkout has none.
var plugins []func(ctx context.Context, b *bridge)

func main() {
	dataDir := flag.String("data", "data", "directory for session.db and live.db")
	configPath := flag.String("config", "config.toml", "whatsapp-reader config (only [[groups]] and [bridge] are read)")
	pair := flag.Bool("pair", false, "show a QR code to link this device, then exit")
	phone := flag.String("phone", "", "with -pair: link by 8-character code instead of QR, for this phone number (international format, e.g. 447700900123)")
	qrStyle := flag.String("qr", "half", "QR rendering: half (compact) or full (one terminal row per module; try this if the phone won't scan)")
	flag.Parse()
	log.SetFlags(log.Ltime)

	if err := run(*dataDir, *configPath, *pair, *phone, *qrStyle); err != nil {
		log.Fatal(err)
	}
}

func run(dataDir, configPath string, pair bool, phone, qrStyle string) error {
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

	store.DeviceProps.Os = proto.String("whatsapp-reader")
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

	b := &bridge{cli: whatsmeow.NewClient(device, waLog.Stdout("whatsmeow", "WARN", false)), store: st, allow: allow, dataDir: dataDir}
	connected := make(chan struct{}, 1)
	b.cli.AddEventHandler(func(evt any) { b.handle(ctx, evt, connected) })

	if b.cli.Store.ID == nil {
		if !pair {
			_ = st.SetStatus(ctx, "state", "unpaired")
			log.Print("not linked to a phone yet: run `whatsapp-reader link`")
			os.Exit(exitNeedsPairing)
		}
		if err := b.pair(ctx, phone, qrStyle); err != nil {
			return err
		}
		select { // pairing reconnects; wait for the session to come up so the phone sees the device
		case <-connected:
		case <-time.After(30 * time.Second):
		case <-ctx.Done():
		}
		b.cli.Disconnect()
		fmt.Println("Linked. Start the bridge to begin archiving: systemctl --user start whatsapp-reader-bridge")
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
	for _, p := range plugins {
		go p(ctx, b)
	}

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

// pair links this device to the phone. By default it shows QR codes (a fresh
// one every ~20 s until one is scanned). With a phone number it instead asks
// WhatsApp for an 8-character code to type in under "Link with phone number".
func (b *bridge) pair(ctx context.Context, phone, qrStyle string) error {
	qr, err := b.cli.GetQRChannel(ctx)
	if err != nil {
		return err
	}
	if err := b.cli.Connect(); err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	codeShown := false
	for item := range qr {
		switch item.Event {
		case "code":
			if phone != "" {
				if codeShown {
					continue // the link code stays valid while the login socket lives; nothing to redraw
				}
				// Browser-style name is required: the server rejects anything else. The phone shows
				// it only until pairing completes; afterwards the device is listed under DeviceProps.Os.
				code, err := b.cli.PairPhone(ctx, phone, true, whatsmeow.PairClientChrome, "Chrome (Linux)")
				if err != nil {
					return fmt.Errorf("requesting a link code: %w", err)
				}
				codeShown = true
				fmt.Printf("\nOn the phone: WhatsApp > ⋮ > Linked devices > Link a device > Link with phone number instead,\nthen enter this code within about two minutes:\n\n    %s\n\n", code)
				continue
			}
			showQR(item.Code, qrStyle)
			fmt.Printf("(valid for %s; a fresh one appears when it expires)\n", item.Timeout.Round(time.Second))
		case whatsmeow.QRChannelSuccess.Event:
			fmt.Println("Paired.")
			return nil
		default:
			return fmt.Errorf("pairing failed: %s %v", item.Event, item.Error)
		}
	}
	return fmt.Errorf("pairing cancelled")
}

// showQR draws the code with explicit black-on-white ANSI colours so that it
// scans the same in light and dark terminals. WhatsApp's scanner, unlike a
// camera app, rejects inverted codes and codes with gaps between rows.
func showQR(code, style string) {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Print("\033[2J\033[H") // clear, so only the current (unexpired) code is on screen
	}
	fmt.Println("On the phone: WhatsApp > ⋮ > Linked devices > Link a device, then scan:")
	fmt.Println()
	var buf bytes.Buffer
	if style == "full" {
		qrterminal.Generate(code, qrterminal.L, &buf) // colour per cell; largest but most robust
	} else {
		qrterminal.GenerateHalfBlock(code, qrterminal.L, &buf)
	}
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		fmt.Printf("\033[97;40m%s\033[0m\n", line) // white foreground on black background, whatever the theme
	}
	fmt.Println()
}

func (b *bridge) handle(ctx context.Context, evt any, connected chan struct{}) {
	switch e := evt.(type) {
	case *events.Connected:
		_ = b.store.SetStatus(ctx, "state", "connected")
		_ = b.store.SetStatus(ctx, "since", strconv.FormatInt(time.Now().Unix(), 10))
		// Own messages are stored without a sender, so the CLI learns here which ID is "Me" when
		// someone @-mentions this account.
		if lid := b.cli.Store.GetLID(); !lid.IsEmpty() {
			_ = b.store.SetStatus(ctx, "me_lid", lid.User)
		}
		if id := b.cli.Store.ID; id != nil {
			_ = b.store.SetStatus(ctx, "me_pn", id.User)
		}
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
		log.Printf("logged out by the phone (%v); run `whatsapp-reader link` to pair again", e.Reason)
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
	case m.GetSecretEncryptedMessage() != nil:
		// Newer clients send edits encrypted with the original message's secret.
		enc := m.GetSecretEncryptedMessage()
		if enc.GetSecretEncType() != waE2E.SecretEncryptedMessage_MESSAGE_EDIT {
			log.Printf("secret message type %v for %s in %s ignored", enc.GetSecretEncType(), enc.GetTargetMessageKey().GetID(), chat)
			break
		}
		dec, derr := b.cli.DecryptSecretEncryptedMessage(ctx, evt)
		if derr != nil {
			log.Printf("decrypting edit of %s in %s: %v", enc.GetTargetMessageKey().GetID(), chat, derr)
			break
		}
		err = b.applyEdit(ctx, chat, enc.GetTargetMessageKey().GetID(), dec)
	case m.GetProtocolMessage() != nil:
		pm := m.GetProtocolMessage()
		switch pm.GetType() {
		case waE2E.ProtocolMessage_REVOKE:
			err = b.store.DeleteMessage(ctx, chat, pm.GetKey().GetID())
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			err = b.applyEdit(ctx, chat, pm.GetKey().GetID(), pm.GetEditedMessage())
		default:
			log.Printf("protocol message type %v for %s in %s ignored (edit attr %q)", pm.GetType(), pm.GetKey().GetID(), chat, info.Edit)
		}
	default:
		kind, text := textOf(m)
		if text == "" {
			if f := setFields(m); f != "senderKeyDistributionMessage,messageContextInfo" && f != "senderKeyDistributionMessage" {
				log.Printf("message %s in %s has no text we understand (fields %s); not stored", info.ID, chat, f)
			}
			return
		}
		if info.Edit == types.EditAttributeMessageEdit || evt.IsEdit {
			// An edit that didn't come as a ProtocolMessage: the node's edit attribute marks it and
			// info.ID is the original message. Log it so we learn which shape WhatsApp uses.
			log.Printf("edit-flagged plain message %s in %s (from_me=%v, unwrapped=%v): updating text", info.ID, chat, info.IsFromMe, evt.IsEdit)
			_, err = b.store.EditMessage(ctx, chat, info.ID, kind, text)
			break
		}
		err = b.store.PutMessage(ctx, Msg{
			ChatJID: chat, ID: info.ID, Sender: sender, FromMe: info.IsFromMe,
			At: info.Timestamp, Kind: kind, Text: text, ReplyTo: replyTo(m),
		})
		if err == nil && b.allow.SavesMedia(chat) && time.Since(info.Timestamp) < 48*time.Hour {
			go b.saveMedia(ctx, chat, info.ID, m) // not for history sync backlogs
		}
	}
	if err != nil {
		log.Printf("storing %s in %s: %v", info.ID, chat, err)
	}
}

// applyEdit replaces the text of message id with the edited content, which arrives either as the
// new message itself or wrapped in a MESSAGE_EDIT ProtocolMessage.
func (b *bridge) applyEdit(ctx context.Context, chat, id string, edited *waE2E.Message) error {
	if pm := edited.GetProtocolMessage(); pm.GetType() == waE2E.ProtocolMessage_MESSAGE_EDIT {
		edited = pm.GetEditedMessage()
	}
	kind, text := textOf(edited)
	if text == "" {
		log.Printf("edit of %s in %s carries no text (fields %s); ignored", id, chat, setFields(edited))
		return nil
	}
	n, err := b.store.EditMessage(ctx, chat, id, kind, text)
	if err == nil && n == 0 {
		log.Printf("edit of %s in %s: no such message stored", id, chat)
	}
	return err
}

// maxMedia caps what saveMedia downloads; larger files stay a placeholder.
const maxMedia = 10 << 20

// saveMedia downloads an image or document into <data>/attachments so the agent can Read it.
// Video, voice notes and stickers are skipped.
func (b *bridge) saveMedia(ctx context.Context, chat, id string, m *waE2E.Message) {
	var dl whatsmeow.DownloadableMessage
	name := "image.jpg"
	switch {
	case m.GetImageMessage() != nil:
		img := m.GetImageMessage()
		if img.GetFileLength() > maxMedia {
			return
		}
		dl = img
	case m.GetDocumentMessage() != nil:
		d := m.GetDocumentMessage()
		if d.GetFileLength() > maxMedia {
			return
		}
		dl, name = d, d.GetFileName()
	default:
		return
	}
	data, err := b.cli.Download(ctx, dl)
	if err != nil {
		log.Printf("downloading %s in %s: %v", id, chat, err)
		return
	}
	dir := filepath.Join(b.dataDir, "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Print(err)
		return
	}
	path := filepath.Join(dir, id+"_"+safeName(name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Print(err)
		return
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if err := b.store.SetFile(ctx, chat, id, path); err != nil {
		log.Printf("recording file of %s: %v", id, err)
	}
}

func safeName(name string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '.' || r == '-' || r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return r
		}
		return '_'
	}, filepath.Base(name))
	if len(clean) > 80 {
		clean = clean[len(clean)-80:]
	}
	if clean == "" || clean == "." || clean == ".." {
		return "file"
	}
	return clean
}

// setFields names the populated top-level fields of a message, for logging what we skipped.
func setFields(m *waE2E.Message) string {
	if m == nil {
		return "none"
	}
	var names []string
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		names = append(names, string(fd.Name()))
		return true
	})
	return strings.Join(names, ",")
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
