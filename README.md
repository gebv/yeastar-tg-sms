# Yeastar TG SMS Gateway

A Go library, CLI tool, and Telegram bot for receiving and managing SMS messages via the Yeastar TG series GSM gateway.

Supports two interfaces:
- **AMI** (Asterisk Manager Interface) — real-time SMS reception via TCP on port 5038
- **Web UI** — batch SMS history retrieval via HTTP on port 80

## Features

- **Real-time SMS** via AMI `ReceivedSMS` events with multipart assembly
- **SMS history** via Web UI with pagination and decoding (modified Base64 +2 shift)
- **Send SMS** via AMI or Web UI
- **SIM card discovery** via AMI (`gsm show spans`, `gsm show span N`)
- **Telegram bot** — forward incoming SMS to Telegram with a "last 5" button
- **SQLite storage** — persistent SMS database for querying recent messages
- **AMI auto-reconnect** — exponential backoff on connection loss
- **Transparent decoding** — SMS content is automatically decoded from Yeastar's obfuscated format
- **Automatic session management** — handles Yeastar's quirky authentication (MD5 → Base64 → +2 shift, OsVer cookie extraction)
- **Graceful handling** of Yeastar's malformed HTTP headers

## Installation

```bash
go install github.com/gebv/yeastar-tg-sms/cmd/yeastar-tg-sms@latest    # CLI tool
go install github.com/gebv/yeastar-tg-sms/cmd/yeastar-tg-bot@latest     # Telegram bot
```

Or build from source:

```bash
git clone https://github.com/gebv/yeastar-tg-sms.git
cd yeastar-tg-sms
make build         # CLI tool at ./bin/yeastar-tg-sms
make build-bot     # Telegram bot at ./bin/yeastar-tg-bot
```

---

## Telegram Bot

The `yeastar-tg-bot` daemon connects to the Yeastar gateway via both AMI (real-time) and Web UI (history), stores messages in SQLite, and forwards incoming SMS to a Telegram chat.

### Features

- **Real-time notifications** — incoming SMS are forwarded to Telegram within seconds
- **"📋 Последние 5 СМС" button** — always-visible reply-keyboard button to query the last 5 stored messages
- **/last5 command** — same as the button, accessible via keyboard
- **/start command** — shows welcome message with the button
- **Initial history load** — on startup, fetches the 25 most recent SMS from the Web UI so the "last 5" button works immediately
- **AMI auto-reconnect** — exponential backoff (5 s → 60 s) when the AMI connection drops
- **Deduplication** — `INSERT OR IGNORE` on the SMS primary key prevents duplicates when both AMI and Web UI deliver the same message
- **`.env` file support** — load configuration from a `.env` file next to the binary

### Configuration

All settings are environment variables. Create a `.env` file (see `.env.example`) or export them directly:

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `YEASTAR_ADDR` | ✅ | — | Gateway IP/hostname (e.g. `192.168.1.1`) |
| `YEASTAR_AMI_ADDR` | — | `YEASTAR_ADDR:5038` | AMI address in `host:port` format |
| `YEASTAR_AMI_USER` | ✅ | — | AMI username |
| `YEASTAR_AMI_PASS` | ✅ | — | AMI password |
| `YEASTAR_WEB_USER` | ✅ | — | Web UI username |
| `YEASTAR_WEB_PASS` | ✅ | — | Web UI password |
| `TG_BOT_TOKEN` | ✅ | — | Telegram bot token from [@BotFather](https://t.me/BotFather) |
| `TG_CHAT_ID` | ✅ | — | Authorized Telegram chat ID (messages from other chats are ignored) |
| `DB_PATH` | — | `sms.db` | SQLite database file path |

### Quick Start

1. **Create a Telegram bot** via [@BotFather](https://t.me/BotFather) and copy the token.

2. **Find your chat ID** — send any message to [@userinfobot](https://t.me/userinfobot) and copy the numeric ID.

3. **Create `.env`**:

```bash
cp .env.example .env
# Edit .env with your values
```

4. **Run**:

```bash
./bin/yeastar-tg-bot
```

5. **Open the bot in Telegram**, press **Start**, and you'll see the "📋 Последние 5 СМС" button.

### Architecture

```
┌──────────┐  ReceivedSMS event  ┌─────────┐  NotifySMS()  ┌──────────┐
│  Yeastar │ ─────────────────> │   AMI   │ ────────────> │ Telegram │
│  Gateway │                    │ Client  │               │   Chat   │
└──────────┘                    └────┬────┘               └─────┬────┘
                                     │                          │
                                     │ Save                     │ /last5
                                     ▼                          ▼
                                ┌─────────────────────────────────┐
                                │          SQLite Store           │
                                └─────────────────────────────────┘
                                     ▲
                                     │ Save (initial load)
                                ┌────┴────┐
                                │  Web UI │
                                │ Client  │
                                └─────────┘
```

- **AMI client** — persistent TCP connection with auto-reconnect; receives `ReceivedSMS` events in real time, assembles multipart messages, and pushes them to the Telegram bot and SQLite store.
- **Web UI client** — on startup, fetches the 25 most recent SMS from the gateway's HTTP interface and stores them in SQLite so the "last 5" query works immediately.
- **SQLite store** — single-table database with `INSERT OR IGNORE` deduplication; powers the `/last5` and button queries.
- **Telegram bot** — long-polls for updates; only processes messages from the authorized `TG_CHAT_ID`.

---

## CLI Tool

The `yeastar-tg-sms` CLI fetches SMS history from the Yeastar Web UI and outputs JSON to stdout.

```
yeastar-tg-sms -addr <host> -user <login> -pass <password> [flags]
```

### Required Flags

| Flag | Description |
|------|-------------|
| `-addr` | Yeastar gateway address (e.g., `192.168.1.1`) |
| `-user` | Web UI username |
| `-pass` | Web UI password |

### Optional Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-last` | `false` | Fetch only the most recent page of SMS (25 records). Default: fetch all pages. |
| `-sent` | `false` | Fetch sent messages (outbox) instead of inbox |
| `-port` | `0` | Filter by GSM port (0 = all ports, 1-indexed) |

### Examples

Fetch all SMS from the inbox:

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword
```

Fetch only the most recent page (25 records, fast):

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword -last
```

Fetch sent messages:

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword -sent
```

Filter by GSM port (port 1):

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword -port 1
```

Pipe output to `jq` for filtering:

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword -last | jq '.[] | select(.sender == "MegaFon")'
```

Save to a file:

```bash
yeastar-tg-sms -addr 192.168.1.1 -user admin -pass secretpassword > sms_inbox.json
```

### Output Format

JSON array of objects:

```json
[
  {
    "id": "10395585831777946715",
    "port": 1,
    "sender": "MegaFon",
    "datetime": "2026-05-05 09:04:58",
    "content": "Ваш баланс: -490.12 руб.",
    "content_raw": "99w12LNSuPIKKPEz2...",
    "read": "No",
    "contact_name": "MegaFon"
  }
]
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Unique message ID from the gateway |
| `port` | int | GSM port number (1-indexed) |
| `sender` | string | Sender name/number (may have `@` suffix in raw data, stripped) |
| `datetime` | string | Reception time (`YYYY-MM-DD HH:MM:SS`) |
| `content` | string | Decoded SMS text (human-readable) |
| `content_raw` | string | Raw encoded content (Yeastar modified Base64 +2 shift) |
| `read` | string | `"Yes"` or `"No"` |
| `contact_name` | string | Contact name from the gateway phonebook |

### Error Handling

On error, the tool writes a message to stderr and exits with a non-zero code:

```bash
$ yeastar-tg-sms -addr 192.168.1.1 -user admin -pass wrongpassword
Error: failed to fetch SMS: session expired or access denied (server returned onLogout/login_error)
$ echo $?
1
```

---

## Go API

### Web UI — Fetch SMS History

```go
client := api.NewWebUIClient("192.168.1.1", "admin", "secretpassword")

// Login (handles MD5→Base64→+2 shift auth, OsVer cookie extraction)
session, err := client.Login()
if err != nil {
    log.Fatal(err)
}

// Fetch all SMS from inbox (auto-paginates)
records, err := session.GetAllSMSInbox()
for _, r := range records {
    text, _ := r.DecodedContent()
    fmt.Printf("%s: %s\n", r.Sender, text)
}

// Fetch only the first page (25 most recent)
comm, err := session.GetSMSInbox(1, 25)

// Fetch sent messages
sentRecords, err := session.GetAllSMSSentBox()
```

### AMI — Real-time SMS Reception

```go
cfg := api.Config{
    Addr:     "192.168.1.1:5038",
    Username: "admin",
    Password: "secret",
    Handler: api.HandlerFunc{
        SMSReceivedFn: func(sms api.SMSReceived) {
            if sms.Total <= 1 {
                // Single-part message — process immediately
                text, _ := sms.DecodeSMSContent()
                fmt.Printf("SMS from %s: %s\n", sms.Sender, text)
            }
            // Multipart messages are handled via MultipartCollector
        },
    },
}

client, err := api.New(context.Background(), cfg)
if err != nil {
    log.Fatal(err)
}
defer client.Close()

// Detect connection loss for reconnection
<-client.Done()
log.Println("AMI connection lost")

// Multipart SMS assembly
collector := api.NewMultipartCollector()
handler := api.HandlerFunc{
    SMSReceivedFn: func(sms api.SMSReceived) {
        if assembled := collector.Add(sms); assembled != nil {
            fmt.Printf("Complete SMS from %s: %s\n", assembled.Sender, assembled.Content)
        }
    },
}

// Send SMS
client.SendSMS(0, "+79001234567", "Hello from Yeastar!")
```

### SQLite Store

```go
s, err := store.New("sms.db")
if err != nil {
    log.Fatal(err)
}
defer s.Close()

// Save an SMS (INSERT OR IGNORE — safe to call for duplicates)
s.Save(&store.SMS{
    ID:       "123",
    Port:     1,
    Sender:   "+79001234567",
    DateTime: "2026-05-05 09:04:58",
    Content:  "Hello",
})

// Query the 5 most recent messages
messages, _ := s.GetLastN(5)
for _, m := range messages {
    fmt.Printf("%s: %s\n", m.Sender, m.Content)
}
```

---

## Architecture Details

### Web UI Authentication Flow

The Yeastar web UI uses a custom authentication scheme:

1. **Password encoding**: MD5 → hex → Base64 → shift each character +2 (from `/js/base64.js`)
2. **Cookies**: `loginname`, `password` (URL-encoded shifted Base64), `OsVer`, `defaultpwd`, etc.
3. **OsVer is critical**: The server returns `&OSVer:91.3.0.23;` in the `MyPBX_COMM` hidden input after login. JavaScript extracts this and updates the `OsVer` cookie. Without the correct `OsVer`, subsequent requests return `onLogout`.
4. **Login process**:
   - Set initial cookies (including `OsVer=xxxx` as placeholder)
   - POST to `/cgi/WebCGI?1000` with `username` and `secret` (shifted Base64 MD5)
   - Parse `MyPBX_COMM` from response, extract `OSVer`
   - Update `OsVer` cookie with the real firmware version
5. **SMS requests**: GET/POST to `/cgi/WebCGI?15200` (inbox) or `/cgi/WebCGI?15100` (sent)

### SMS Content Decoding

The Yeastar web UI encodes SMS content using a **modified Base64 with +2 character shift**:

1. Original text → UTF-8 (with BOM `\xEF\xBB\xBF`)
2. → Base64 encode
3. → Shift each character +2

Decoding (implemented in `DecodeYeastarSMSContent`):

1. URL-decode the content
2. Shift each character -2
3. Remove non-Base64 characters
4. Base64 decode
5. UTF-8 decode
6. Strip BOM prefix

AMI events use standard URL-encoding (`url.QueryUnescape`).

### Web UI Pagination

The Yeastar web UI paginates SMS history via POST form parameters:

- **First page**: GET `/cgi/WebCGI?15200`
- **Subsequent pages**: POST `/cgi/WebCGI?15200` with form data `sstartindex=N&sviewcount=25&begindate=&enddate=&port=&hasread=&fromnumber=`

The `MyPBX_COMM` hidden input contains pagination info in the format:

```
&Display:totalRecords,startIndex,viewCount;
```

Example: `Display:141,26,25` means 141 total records, starting at record 26, 25 per page.

### HTTP Header Quirk

The Yeastar Boa web server returns malformed HTTP headers like `<Content-Type:text/html>` (with a leading `<` and no space after the colon). The Go client includes a custom `headerFixingConn` that sanitizes these headers before they reach Go's strict MIME parser.

## Project Structure

```
yeastar-tg-sms/
├── api/
│   ├── client.go              # AMI client (real-time SMS, send SMS, SIM discovery)
│   ├── client_test.go         # AMI client tests (playground, skipped)
│   ├── webui.go               # Web UI types, parsing, SMS decoding
│   ├── webui_client.go        # Web UI HTTP client (Login, GetSMS, pagination)
│   ├── webui_client_test.go   # Web UI client tests
│   └── webui_test.go          # Decoding/parsing tests
├── cmd/
│   ├── yeastar-tg-sms/
│   │   └── main.go            # CLI tool (JSON to stdout)
│   └── yeastar-tg-bot/
│       └── main.go            # Telegram bot daemon
├── internal/
│   ├── store/
│   │   └── store.go           # SQLite storage for SMS messages
│   └── tgbot/
│       └── bot.go             # Telegram bot (notifications, /last5, reply keyboard)
├── .env.example               # Example environment configuration
├── go.mod
└── Makefile
```

## License

MIT