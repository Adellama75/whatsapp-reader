package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Bug 18: an edit that changes the text stamps edited_at (follow re-shows it); a re-delivery with
// the same text does not.
func TestEditedAt(t *testing.T) {
	ctx := context.Background()
	st, err := OpenStore(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	edited := func() (n int64) {
		var v *int64
		st.db.QueryRow(`SELECT edited_at FROM messages WHERE id = 'X'`).Scan(&v)
		if v != nil {
			n = *v
		}
		return
	}
	m := Msg{ChatJID: "g@g.us", ID: "X", Sender: "1", Kind: "text", Text: "2,3,5", At: time.Unix(1, 0)}
	st.PutMessage(ctx, m)
	st.PutMessage(ctx, m) // delivered again, unchanged
	if edited() != 0 {
		t.Fatal("a re-delivery counted as an edit")
	}
	if _, err := st.EditMessage(ctx, "g@g.us", "X", "text", "2,3,6"); err != nil || edited() == 0 {
		t.Fatalf("an edit left edited_at unset (%v)", err)
	}
	m.Text = "2,3,7"
	st.db.Exec(`UPDATE messages SET edited_at = 0 WHERE id = 'X'`)
	if st.PutMessage(ctx, m); edited() == 0 {
		t.Fatal("a changed text via PutMessage left edited_at unset")
	}
}
