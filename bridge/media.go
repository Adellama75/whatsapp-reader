package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// maxMedia caps what save_media groups save on arrival; larger files stay a placeholder.
const maxMedia = 10 << 20

// maxDownload caps what `whatsapp-reader download` fetches on request.
const maxDownload = 100 << 20

// askTimeout is how long a download request waits for the phone to resend a message's keys.
const askTimeout = time.Minute

// mediaOf keeps just the attachment of a message: URL, direct path, media key, hashes, size, mimetype
// and file name, what whatsmeow needs to download it later. The caption, thumbnail and quoted message
// are dropped. nil if the message carries no image, document, video or voice note.
func mediaOf(m *waE2E.Message) []byte {
	var keep waE2E.Message
	switch {
	case m.GetImageMessage() != nil:
		img := proto.Clone(m.GetImageMessage()).(*waE2E.ImageMessage)
		img.Caption, img.JPEGThumbnail, img.ContextInfo = nil, nil, nil
		keep.ImageMessage = img
	case m.GetDocumentMessage() != nil:
		d := proto.Clone(m.GetDocumentMessage()).(*waE2E.DocumentMessage)
		d.Caption, d.JPEGThumbnail, d.ContextInfo = nil, nil, nil
		keep.DocumentMessage = d
	case m.GetVideoMessage() != nil:
		v := proto.Clone(m.GetVideoMessage()).(*waE2E.VideoMessage)
		v.Caption, v.JPEGThumbnail, v.ContextInfo = nil, nil, nil
		keep.VideoMessage = v
	case m.GetAudioMessage() != nil:
		a := proto.Clone(m.GetAudioMessage()).(*waE2E.AudioMessage)
		a.Waveform, a.ContextInfo = nil, nil
		keep.AudioMessage = a
	default:
		return nil
	}
	data, err := proto.Marshal(&keep)
	if err != nil {
		return nil
	}
	return data
}

// attachment unpacks what mediaOf kept: the downloadable part, a file name and the size.
func attachment(media []byte) (whatsmeow.DownloadableMessage, string, uint64, error) {
	var m waE2E.Message
	if err := proto.Unmarshal(media, &m); err != nil {
		return nil, "", 0, err
	}
	switch {
	case m.ImageMessage != nil:
		return m.ImageMessage, "image.jpg", m.ImageMessage.GetFileLength(), nil
	case m.DocumentMessage != nil:
		return m.DocumentMessage, m.DocumentMessage.GetFileName(), m.DocumentMessage.GetFileLength(), nil
	case m.VideoMessage != nil:
		return m.VideoMessage, "video.mp4", m.VideoMessage.GetFileLength(), nil
	case m.AudioMessage != nil:
		return m.AudioMessage, "audio.ogg", m.AudioMessage.GetFileLength(), nil
	}
	return nil, "", 0, errors.New("no attachment")
}

// saveMedia downloads an attachment into <data>/attachments, records the path, and returns it.
func (b *bridge) saveMedia(ctx context.Context, chat, id string, media []byte, limit uint64) (string, error) {
	dl, name, size, err := attachment(media)
	if err != nil {
		return "", err
	}
	if size > limit {
		return "", fmt.Errorf("%d MB is over the %d MB limit", size>>20, limit>>20)
	}
	data, err := b.cli.Download(ctx, dl)
	if err != nil {
		return "", fmt.Errorf("downloading: %w", err)
	}
	dir := filepath.Join(b.dataDir, "attachments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, id+"_"+safeName(name))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return path, b.store.SetFile(ctx, chat, id, path)
}

// serveDownloads fetches attachments on request from the CLI (`whatsapp-reader download`): a file
// <data>/requests/<message id>.want holding the group JID. On success the request goes and the
// message's file column is set; on failure <id>.err says why.
func (b *bridge) serveDownloads(ctx context.Context) {
	dir := filepath.Join(b.dataDir, "requests")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Print(err)
		return
	}
	asked := map[string]time.Time{} // requests waiting for the phone, since when
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		reqs, _ := filepath.Glob(filepath.Join(dir, "*.want"))
		for _, req := range reqs {
			if err := b.serveDownload(ctx, req, asked); err != nil {
				log.Printf("download request %s: %v", filepath.Base(req), err)
				_ = os.WriteFile(strings.TrimSuffix(req, ".want")+".err", []byte(err.Error()+"\n"), 0o600)
				_ = os.Remove(req)
				delete(asked, req)
			}
		}
	}
}

// serveDownload handles one request; nil means done, or still waiting for the phone.
//
// Messages stored before the bridge kept attachment keys have none. For those it asks the phone,
// once, to resend the message (a peer message to this account's own phone, not to the chat); the
// copy comes back as an ordinary message event, PutMessage adds its keys, and the next poll downloads.
func (b *bridge) serveDownload(ctx context.Context, req string, asked map[string]time.Time) error {
	raw, err := os.ReadFile(req)
	if err != nil {
		return err
	}
	chat, id := strings.TrimSpace(string(raw)), strings.TrimSuffix(filepath.Base(req), ".want")
	if !b.allow.Allows(chat) {
		return fmt.Errorf("%s is not an allowlisted group", chat)
	}
	media, sender, fromMe, err := b.store.MediaOf(ctx, chat, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("no message %s in %s", id, chat)
	} else if err != nil {
		return err
	}
	if media == nil {
		if since, ok := asked[req]; ok {
			if time.Since(since) > askTimeout {
				return errors.New("no attachment keys stored, and the phone did not resend the message")
			}
			return nil
		}
		asked[req] = time.Now()
		chatJID, _ := types.ParseJID(chat)
		_, err := b.cli.SendPeerMessage(ctx, b.cli.BuildUnavailableMessageRequest(chatJID, b.senderJID(ctx, sender, fromMe), id))
		if err == nil {
			log.Printf("asked the phone to resend %s in %s for its attachment keys", id, chat)
		}
		return err
	}
	path, err := b.saveMedia(ctx, chat, id, media, maxDownload)
	if err != nil {
		return err
	}
	delete(asked, req)
	_ = os.Remove(req)
	log.Printf("downloaded %s in %s to %s", id, chat, path)
	return nil
}

// senderJID turns a stored sender (see canonical: a LID when known, else a phone number) back into a JID.
func (b *bridge) senderJID(ctx context.Context, sender string, fromMe bool) types.JID {
	if fromMe && b.cli.Store.ID != nil {
		return b.cli.Store.ID.ToNonAD()
	}
	lid := types.NewJID(sender, types.HiddenUserServer)
	if pn, err := b.cli.Store.LIDs.GetPNForLID(ctx, lid); err == nil && !pn.IsEmpty() {
		return lid
	}
	return types.NewJID(sender, types.DefaultUserServer)
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
