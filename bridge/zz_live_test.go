package main

import ("context";"fmt";"testing")

func TestLivePrompt(t *testing.T) {
	cases := []struct{ g, m string; want bool }{
		{"Comp Programming Society General", "Hi everyone, I'm giving away my free camera, Canon EOS 2000D, barely used. First to DM me gets it!", true},
		{"Comp Programming Society General", "Please donate to help families in Yemen who are starving. Every pound counts, DM me for the link 🙏", true},
		{"Comp Programming Society General", "I'm giving away my late husband's piano for free to anyone who can pick it up, text me", true},
		{"RAG UCL Society", "Our charity bake sale for Yemen relief is on Thursday in the Main Quad, come along!", false},
		{"Comp Programming Society General", "Anyone selling a camera? Need one for a project", false},
		{"Comp Programming Society General", "https://ucluster-too-qualifier.vercel.app/", false},
		{"UCL CS 26/27", "Highkey will ditch the lecture if they're giving away free 400GB", false},
		{"Comp Programming Society General", "Selling 2 Taylor Swift tickets, DM me", true},
		{"General", "Hi everyone \nI just upgraded my gear, so I’m giving away my old canon camera for free.\nIt’s still in great condition and works perfectly ideal for anyone getting into photography.\nIf you’re interested in picking it up, send me a dm on iMessage: +1 (319) 693-8672", true},
		{"UKIEPC 2026 team finding", "alsalam ealaykum warahmat allah wabarakata\n\nhttps://gofund.me/a1704c635\n\nDear brothers and sisters, here is a family in dire need of assistance, facing extremely difficult circumstances. They don't even have a single room to sleep in. We pray that God will reward everyone who contributes.", true},
		{"Competitive Programming Society", "👆🏻👆🏻👆🏻\nPlease, we kindly ask you not to hold back from helping us, even if it is just £5 or 10£. It may seem like a small amount to you, but to us, it represents the hope we are holding on to.💔", true},
		{"UKIEPC 2026 team finding", "Looking for 2 teammates for UKIEPC, we're both second years, decent at graphs. DM if interested", false},
		{"AI Society 26/27", "Anyone have a spare ticket for the AI Soc ball? will pay", false},
		{"UCL CS 26/27", "does anyone want my old algorithms textbook? free, I'm in the lab till 5", false},
		{"Competitive Programming Society", "We're hiring! My startup needs a part-time React dev, £25/h, apply at careers.acme.io", false},
		{"General", "[image]", false},
		{"General", "I got ticket for Liverpool vs Manchester United premier league match on Sunday 22 November 2026. (Kick off 16:30) @ Anfield", true},
		{"UCL CS 26/27", "I got tickets for the CS ball finally, who else is going?", false},
		{"General", "got my ticket for the bubble tea crawl on friday, so excited", false},
		{"UCL CS 26/27", "I have a spare monitor in the lab if anyone needs one for the hackathon", false},
		{"Competitive Programming Society", "Stock market enthusiasts and beginners are welcome to join and learn：）\nhttps://chat.whatsapp.com/CH5bEXDpkKm2UUMRQKvQNj", true},
		{"UCL CS 26/27", "Join our forex signals channel, members made 30% last month https://t.me/fxsignals", true},
		{"Competitive Programming Society", "Made a group for people practising for UKIEPC, join here https://chat.whatsapp.com/AbCdEfGh123", false},
		{"AI Society 26/27", "We started an ML paper reading group, everyone welcome: https://chat.whatsapp.com/XyZ987reading", false},
		{"UCL CS 26/27", "UCL Investment Society are doing a beginners intro to trading talk on Thursday 6pm in Roberts G06, sign up on the SU site", false},
	}
	bad := 0
	for _, c := range cases {
		got, err := classifySpam(context.Background(), "claude-haiku-5-5", c.g, c.m)
		if err != nil { t.Fatal(err) }
		mark := "ok  "; if got != c.want { mark = "MISS"; bad++ }
		fmt.Printf("%s spam=%v  %.70s\n", mark, got, c.m)
	}
	if bad > 0 { t.Fatalf("%d wrong", bad) }
}
