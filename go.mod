module go.mau.fi/mautrix-discord

go 1.25.0

toolchain go1.26.0

require (
	github.com/bwmarrin/discordgo v0.27.0
	github.com/gabriel-vasile/mimetype v1.4.9
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510
	github.com/gorilla/mux v1.8.0
	github.com/gorilla/websocket v1.5.0
	github.com/lib/pq v1.12.3
	github.com/mattn/go-sqlite3 v1.14.44
	github.com/rs/zerolog v1.35.1
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/stretchr/testify v1.11.1
	github.com/tidwall/sjson v1.2.5
	github.com/yuin/goldmark v1.8.2
	go.mau.fi/util v0.9.9
	go.mau.fi/zeroconfig v0.2.0
	golang.org/x/exp v0.0.0-20260508232706-74f9aab9d74a
	golang.org/x/sync v0.20.0
	gopkg.in/yaml.v3 v3.0.1
	maunium.net/go/mauflag v1.0.0
	maunium.net/go/maulogger/v2 v2.4.1
	maunium.net/go/mautrix v0.28.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/coreos/go-systemd/v22 v22.7.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/petermattis/goid v0.0.0-20260330135022-df67b199bc81 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rs/xid v1.6.0 // indirect
	github.com/tidwall/gjson v1.19.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/bwmarrin/discordgo => github.com/beeper/discordgo v0.0.0-20260215125047-ccf8cbaa0a9f

replace maunium.net/go/mautrix => github.com/thesuperRL/mautrix-go v0.0.0-20260628213958-cfd090ee7c51
