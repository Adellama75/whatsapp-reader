# Notes for Claude sessions

- Agent behaviour (batching, prompt, posting rules, chat style, follow/coordinator, the Claude run) lives in
  **chat-agent** (`../chat-agent`), shared with discord-reader. Change it there, not here; this repo only has
  the WhatsApp transport and `src/whatsapp_reader/chat.py`, the adapter. Run both readers' tests after a
  chat-agent change.
- The bridge only listens. Extensions are plugins (entry-point group `whatsapp_reader.plugins`, bridge
  `local_*.go`) and live outside this repo.
