package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waAdv"
	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

// device makes a whatsmeow client for a synthetic account, with its own session store.
func device(t *testing.T, ctx context.Context, user, lid string) *whatsmeow.Client {
	t.Helper()
	c, err := sqlstore.New(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "s.db")+"?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := c.NewDevice()
	d.ID = &types.JID{User: user, Device: 1, Server: types.DefaultUserServer}
	d.LID = types.JID{User: lid, Device: 1, Server: types.HiddenUserServer}
	d.Account = &waAdv.ADVSignedDeviceIdentity{ // dummy pairing record; only the session tables matter here
		Details: []byte{}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	if err := d.Save(ctx); err != nil {
		t.Fatal(err)
	}
	return whatsmeow.NewClient(d, nil)
}

func TestPollVotesDecryptMapToOptionsAndLatestWins(t *testing.T) {
	ctx := context.Background()
	group := types.NewJID("120363000000000001", types.GroupServer)
	me := device(t, ctx, "447700900001", "11111111111111")  // the bridge's account, which sent the poll
	pat := device(t, ctx, "447700900002", "57153196953645") // a member who votes
	meLID, patLID := me.Store.GetLID().ToNonAD(), pat.Store.GetLID().ToNonAD()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i)
	}
	for _, c := range []*whatsmeow.Client{me, pat} { // both sides learnt the poll's secret when it was sent
		if err := c.Store.MsgSecrets.PutMessageSecret(ctx, group, meLID, "POLL1", secret); err != nil {
			t.Fatal(err)
		}
	}
	st, err := OpenStore(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	b := &bridge{cli: me, store: st}
	options := []string{"temperature", "stirring", "pH"}
	poll := &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Which: subsystem?")}}
	for _, o := range options {
		poll.PollCreationMessageV3.Options = append(poll.PollCreationMessageV3.Options, &waE2E.PollCreationMessage_Option{OptionName: proto.String(o)})
	}
	kind, text := textOf(poll)
	if err := st.PutMessage(ctx, Msg{ChatJID: group.String(), ID: "POLL1", FromMe: true, At: time.Unix(1_700_000_000, 0),
		Kind: kind, Text: text, PollOptions: pollOptions(poll)}); err != nil {
		t.Fatal(err)
	}

	// vote encrypts a vote the way the voter's phone does and delivers it as the bridge would get it.
	vote := func(voter *whatsmeow.Client, fromMe bool, at int64, picks ...string) {
		t.Helper()
		pollInfo := &types.MessageInfo{ID: "POLL1", MessageSource: types.MessageSource{Chat: group, Sender: meLID, IsFromMe: fromMe, IsGroup: true}}
		pu, err := voter.EncryptPollVote(ctx, pollInfo, &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions(picks)})
		if err != nil {
			t.Fatal(err)
		}
		pu.SenderTimestampMS = proto.Int64(at)
		evt := &events.Message{Info: types.MessageInfo{ID: "V", Timestamp: time.UnixMilli(at), MessageSource: types.MessageSource{
			Chat: group, Sender: voter.Store.GetLID(), IsFromMe: fromMe, IsGroup: true}}, Message: &waE2E.Message{PollUpdateMessage: pu}}
		sender := ""
		if !fromMe {
			sender = patLID.User
		}
		if err := b.pollVote(ctx, group.String(), sender, evt); err != nil {
			t.Fatal(err)
		}
	}
	votes := func() map[string]string {
		rows, err := st.db.Query(`SELECT voter, options FROM poll_votes WHERE chat_jid = ? AND poll_id = 'POLL1'`, group.String())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var v, o string
			rows.Scan(&v, &o)
			out[v] = o
		}
		return out
	}

	vote(pat, false, 1_700_000_100_000, "stirring", "pH")
	vote(me, true, 1_700_000_200_000, "temperature") // from my phone: voter "" (Me), like reactions
	if got := votes(); got[patLID.User] != `["stirring","pH"]` || got[""] != `["temperature"]` || len(got) != 2 {
		t.Fatalf("votes = %v", got)
	}
	vote(pat, false, 1_700_000_300_000, "temperature") // a revote replaces the earlier selection
	vote(pat, false, 1_700_000_150_000, "pH")          // a stale vote delivered late is ignored
	if got := votes()[patLID.User]; got != `["temperature"]` {
		t.Fatalf("after revote %s", got)
	}
	vote(pat, false, 1_700_000_400_000) // nothing selected: withdrawn
	if got := votes()[patLID.User]; got != `[]` {
		t.Fatalf("after withdrawal %s", got)
	}

	// History sync brings votes already decrypted, keyed by the vote message's key.
	b.historyVotes(ctx, group.String(), "POLL1", []*waWeb.PollUpdate{{
		PollUpdateMessageKey: &waCommon.MessageKey{Participant: proto.String("99999999999999@lid")},
		Vote:                 &waE2E.PollVoteMessage{SelectedOptions: whatsmeow.HashPollOptions([]string{"pH", "nope"})},
		SenderTimestampMS:    proto.Int64(1_700_000_500_000),
	}})
	if got := votes()["99999999999999"]; got[:7] != `["pH","` || got[7] != '?' {
		t.Fatalf("history vote = %s (an unknown hash should be kept as ?hex)", got)
	}
}

func TestCandidateOptionsForPollsStoredWithoutTheColumn(t *testing.T) {
	text := "[poll] Q: which one?: a / b: c / d"
	sel := whatsmeow.HashPollOptions([]string{"b: c"})
	if got := optionNames(sel, candidateOptions(nil, text, sel)); len(got) != 1 || got[0] != "b: c" {
		t.Fatalf("got %v", got)
	}
	if got := candidateOptions([]string{"x"}, text, sel); len(got) != 1 || got[0] != "x" {
		t.Fatalf("stored options must win: %v", got)
	}
}
