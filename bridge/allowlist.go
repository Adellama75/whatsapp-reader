package main

import (
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Allowlist is the set of group JIDs from config.toml's [[groups]] entries.
//
// Only these groups are ever written to the store. The file is re-read when
// its mtime changes (checked at most every few seconds), so adding a group
// that has given consent takes effect without restarting the bridge.
type Allowlist struct {
	path string
	mu   sync.Mutex
	jids map[string]bool
	mod  time.Time
	seen time.Time
	// HistoryDays is the [bridge] history_days hint sent to the phone at pairing.
	HistoryDays uint32
}

type configFile struct {
	Groups []struct {
		JID string `toml:"jid"`
	} `toml:"groups"`
	Bridge struct {
		HistoryDays uint32 `toml:"history_days"`
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
	for _, g := range c.Groups {
		jids[g.JID] = true
	}
	a.jids, a.mod, a.HistoryDays = jids, st.ModTime(), c.Bridge.HistoryDays
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
