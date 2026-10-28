package main

import (
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Allowlist is the set of chat JIDs from config.toml's [[groups]] entries: group JIDs, and
// direct-message chats listed explicitly by <pn>@s.whatsapp.net or <lid>@lid.
//
// Only these chats are ever written to the store. The file is re-read when
// its mtime changes (checked at most every few seconds), so adding a group
// that has given consent takes effect without restarting the bridge.
type Allowlist struct {
	path string
	mu   sync.Mutex
	jids map[string]bool
	// media is the set of groups with save_media = true: their images and documents are downloaded.
	media map[string]bool
	mod   time.Time
	seen  time.Time
	// HistoryDays is the [bridge] history_days hint sent to the phone at pairing.
	HistoryDays uint32
	deleteSpam  bool // [bridge] delete_spam: see spam.go
}

type configFile struct {
	Groups []struct {
		JID       string `toml:"jid"`
		SaveMedia bool   `toml:"save_media"`
	} `toml:"groups"`
	Bridge struct {
		HistoryDays uint32 `toml:"history_days"`
		DeleteSpam  bool   `toml:"delete_spam"`
	} `toml:"bridge"`
}

func LoadAllowlist(path string) (*Allowlist, error) {
	a := &Allowlist{path: path, jids: map[string]bool{}}
	return a, a.reload()
}

func (a *Allowlist) reload() error {
	st, err := os.Stat(a.path)
	if err != nil {
		return err
	}
	a.seen = time.Now()
	if st.ModTime().Equal(a.mod) {
		return nil
	}
	var c configFile
	if _, err := toml.DecodeFile(a.path, &c); err != nil {
		return err
	}
	jids := make(map[string]bool, len(c.Groups))
	media := map[string]bool{}
	for _, g := range c.Groups {
		jids[g.JID] = true
		if g.SaveMedia {
			media[g.JID] = true
		}
	}
	a.media = media
	a.jids, a.mod, a.HistoryDays, a.deleteSpam = jids, st.ModTime(), c.Bridge.HistoryDays, c.Bridge.DeleteSpam
	return nil
}

func (a *Allowlist) Allows(jid string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.seen) > 5*time.Second {
		_ = a.reload() // keep the last good list if the file is mid-edit
	}
	return a.jids[jid]
}

// SavesMedia reports whether an allowlisted chat opted into saving attachments.
func (a *Allowlist) SavesMedia(jid string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.media[jid]
}

// DeletesSpam reports whether [bridge] delete_spam is on, re-reading config.toml like Allows.
func (a *Allowlist) DeletesSpam() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.seen) > 5*time.Second {
		_ = a.reload()
	}
	return a.deleteSpam
}
