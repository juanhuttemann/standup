Output demo.gif

Set Shell "bash"
Set FontSize 28
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set Width 1200
Set Height 620
Set Theme "Dracula"
Set Padding 20
Set TypingSpeed 30ms
Set PlaybackSpeed 2.0
Set CursorBlink false
Set WaitTimeout 60s

# Build, scratch store, seed yesterday (last working day) — all off-screen
Hide
Type "go build -o /tmp/standup-demo-bin/standup ./cmd/standup && mkdir -p /tmp/standup-demo-store"
Enter
Wait
Type "echo aW1wb3J0IGpzb24sIGRhdGV0aW1lIGFzIGQKZiA9IG9wZW4oIi90bXAvc3RhbmR1cC1kZW1vLXN0b3JlL3Rhc2tzLmpzb25sIiwgInciKQp0ID0gZC5kYXRldGltZS5ub3coZC50aW1lem9uZS51dGMpLmFzdGltZXpvbmUoKSAtIGQudGltZWRlbHRhKGRheXM9MSkKcm93cyA9IFsKICAgICgic2hpcHBlZCByYXRlLWxpbWl0IG1pZGRsZXdhcmUiLCAiZG9uZSIsIDEwLCAxMiksCiAgICAoInJldmlld2VkIFBSICMzNDIgYXBpIHBhZ2luYXRpb24iLCAiZG9uZSIsIDE2LCA0NSksCiAgICAoIndhaXRpbmcgb24gc3RhZ2luZyBjcmVkZW50aWFscyIsICJibG9ja2VkIiwgMTcsIDUpLApdCmZvciBpLCAodGFzaywgc3RhdHVzLCBoLCBtKSBpbiBlbnVtZXJhdGUocm93cyk6CiAgICB0cyA9IHQucmVwbGFjZShob3VyPWgsIG1pbnV0ZT1tLCBtaWNyb3NlY29uZD0wKS5pc29mb3JtYXQoKQogICAgZi53cml0ZShqc29uLmR1bXBzKHsiaWQiOiBmInN7aX0iLCAidGFzayI6IHRhc2ssICJzdGF0dXMiOiBzdGF0dXMsICJ0aW1lc3RhbXAiOiB0c30pICsgIlxuIikK | base64 -d > /tmp/seed.py && python3 /tmp/seed.py"
Enter
Wait
# Provider settings stay in the invoker's env (never committed):
#   online:  export OPENAI_BASE_URL/OPENAI_MODEL (see README Setup)
#   offline: export STANDUP_OFFLINE=true
Type "export PATH=/tmp/standup-demo-bin:$PATH STANDUP_DATA_FILE=/tmp/standup-demo-store/tasks.jsonl"
Enter
Wait
Type "clear"
Enter
Wait
Show

# Log today's work — the model cleans up the text
Type `standup add "fix flaky checkout e2e test"`
Enter
Wait+Screen /\[todo\] Fix flaky/

# Raw add: no model, instant
Type `standup add --raw "pair with dana on migration review"`
Enter
Wait+Screen /\[todo\] pair with dana/

# The standup report: yesterday, today, blockers
Type "standup generate"
Enter
Wait+Screen /## Blockers/
Sleep 4s
