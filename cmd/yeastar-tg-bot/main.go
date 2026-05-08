package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/gebv/yeastar-tg-sms/api"
	"github.com/gebv/yeastar-tg-sms/internal/health"
	"github.com/gebv/yeastar-tg-sms/internal/store"
	"github.com/gebv/yeastar-tg-sms/internal/tgbot"
)

// version is set at build time via -ldflags.
// Default value "dev" is used when building locally (e.g., go build / make).
var version = "dev"

func main() {
	log.Printf("yeastar-tg-bot %s", version)

	// Load .env file if present (silently ignore if missing)
	_ = godotenv.Load()

	cfg := loadConfig()

	// Initialize SQLite store
	s, err := store.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Store init failed: %v", err)
	}
	defer s.Close()

	// Initialize health tracker
	h := health.New(time.Now())

	// Initialize Telegram bot
	bot, err := tgbot.New(cfg.TGBotToken, cfg.TGChatID, s, h)
	if err != nil {
		log.Fatalf("Telegram bot init failed: %v", err)
	}
	log.Printf("Telegram bot started")

	// Web UI session manager (reuses login session, reconnects on expiry)
	webUI := &webUISession{
		client: api.NewWebUIClient(cfg.YeastarAddr, cfg.WebUser, cfg.WebPass),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Signal channel: AMI sends a pulse when a new SMS arrives.
	// The Web UI fetcher reads from this channel and debounces fetches.
	smsSignal := make(chan struct{}, 32)

	// Initial load: fetch recent SMS from Web UI so the "last 5" button
	// works immediately, even before any AMI events arrive.
	go initialLoad(s, bot, h, webUI)

	// AMI listener: connects with auto-reconnect, signals on ReceivedSMS.
	go runAMISignaler(ctx, cfg, h, smsSignal)

	// Web UI fetcher: debounced fetch triggered by AMI signals.
	go runWebUIFetcher(ctx, s, bot, h, webUI, smsSignal)

	// Start Telegram bot (blocking in goroutine)
	go bot.Run()

	log.Println("Yeastar SMS Gateway Bot is running. Press Ctrl+C to stop.")

	// Wait for shutdown signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigChan

	log.Printf("Received signal %v, shutting down...", sig)
	bot.Stop()
	cancel()

	// Give goroutines time to clean up
	time.Sleep(2 * time.Second)
	log.Println("Bye!")
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// Config holds all configuration for the Telegram bot application.
type Config struct {
	YeastarAddr string // Yeastar gateway address (e.g., "192.168.1.1")
	AMIAddr     string // AMI address in host:port format
	AMIUser     string // AMI username
	AMIPass     string // AMI password
	WebUser     string // Web UI username
	WebPass     string // Web UI password
	TGBotToken  string // Telegram bot API token
	TGChatID    int64  // Authorized Telegram chat ID
	DBPath      string // SQLite database file path
}

func loadConfig() *Config {
	chatID, err := strconv.ParseInt(envOrDie("TG_CHAT_ID"), 10, 64)
	if err != nil {
		log.Fatalf("Invalid TG_CHAT_ID: %v", err)
	}

	addr := envOrDie("YEASTAR_ADDR")

	return &Config{
		YeastarAddr: addr,
		AMIAddr:     envOrDefault("YEASTAR_AMI_ADDR", addr+":5038"),
		AMIUser:     envOrDie("YEASTAR_AMI_USER"),
		AMIPass:     envOrDie("YEASTAR_AMI_PASS"),
		WebUser:     envOrDie("YEASTAR_WEB_USER"),
		WebPass:     envOrDie("YEASTAR_WEB_PASS"),
		TGBotToken:  envOrDie("TG_BOT_TOKEN"),
		TGChatID:    chatID,
		DBPath:      envOrDefault("DB_PATH", "sms.db"),
	}
}

func envOrDie(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}

func envOrDefault(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}

// ---------------------------------------------------------------------------
// Initial load: fetch recent SMS from Web UI at startup
// ---------------------------------------------------------------------------

// initialLoad fetches the most recent SMS from the Web UI and stores them.
// This ensures the "last 5" button works immediately, even before any AMI
// events arrive. Only truly new records are forwarded to Telegram.
func initialLoad(s *store.Store, bot *tgbot.Bot, h *health.Tracker, webUI *webUISession) {
	records, err := webUI.fetchRecent()
	if err != nil {
		h.SetWebUIError(err.Error())
		log.Printf("[WebUI] Initial load failed: %v", err)
		return
	}

	newCount := processWebUIRecords(records, s, bot)
	h.SetWebUISync()
	log.Printf("[WebUI] Initial load: %d new of %d fetched", newCount, len(records))
}

// ---------------------------------------------------------------------------
// Web UI session manager
// ---------------------------------------------------------------------------

// webUISession manages a persistent Web UI session, reconnecting on expiry.
type webUISession struct {
	client  *api.WebUIClient
	session *api.WebUISession
	mu      sync.Mutex
}

// fetchRecent logs in (if needed) and fetches the first page of SMS inbox
// (up to 25 most recent messages). It automatically reconnects if the
// session has expired.
func (w *webUISession) fetchRecent() ([]api.WebSMSRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.session == nil {
		sess, err := w.client.Login()
		if err != nil {
			return nil, fmt.Errorf("webui login: %w", err)
		}
		w.session = sess
	}

	comm, err := w.session.GetSMSInbox(1, 25)
	if err != nil {
		// Session likely expired — reconnect and retry
		log.Printf("[WebUI] Session expired, re-logging in: %v", err)
		w.session = nil

		sess, loginErr := w.client.Login()
		if loginErr != nil {
			return nil, fmt.Errorf("webui re-login: %w", loginErr)
		}
		w.session = sess

		comm, err = w.session.GetSMSInbox(1, 25)
		if err != nil {
			return nil, fmt.Errorf("webui fetch after re-login: %w", err)
		}
	}

	return comm.SMSRecvList, nil
}

// ---------------------------------------------------------------------------
// AMI signaler: connects with auto-reconnect, pulses on ReceivedSMS
// ---------------------------------------------------------------------------

// runAMISignaler connects to the Yeastar AMI and sends a signal on smsSignal
// each time a ReceivedSMS event arrives. It does NOT extract SMS content —
// the Web UI is the single source of truth.
// On connection loss it reconnects with exponential backoff (5s → 60s).
func runAMISignaler(ctx context.Context, cfg *Config, h *health.Tracker, smsSignal chan<- struct{}) {
	reconnectDelay := 5 * time.Second
	maxDelay := 60 * time.Second
	firstConnect := true

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("[AMI] Connecting to %s...", cfg.AMIAddr)

		// Use a 30-second timeout for the AMI connection attempt (banner + login).
		amiCtx, amiCancel := context.WithTimeout(context.Background(), 30*time.Second)

		amiCfg := api.Config{
			Addr:     cfg.AMIAddr,
			Username: cfg.AMIUser,
			Password: cfg.AMIPass,
			Handler: api.HandlerFunc{
				SMSReceivedFn: func(sms api.SMSReceived) {
					// Signal only — do NOT extract content.
					// The Web UI fetcher will pull fresh data.
					log.Printf("[AMI] ReceivedSMS event → sending signal to fetcher")
					h.TouchAMILastEvent()
					select {
					case smsSignal <- struct{}{}:
						log.Printf("[AMI] Signal sent to fetcher OK")
					default:
						log.Printf("[AMI] Signal channel full — fetch is already pending")
					}
				},
			},
		}

		client, err := api.New(amiCtx, amiCfg)
		if err != nil {
			amiCancel()
			h.SetAMIError(err.Error())
			log.Printf("[AMI] Connection failed: %v", err)
			log.Printf("[AMI] Retrying in %v...", reconnectDelay)

			select {
			case <-time.After(reconnectDelay):
				reconnectDelay = minDur(reconnectDelay*2, maxDelay)
				continue
			case <-ctx.Done():
				return
			}
		}

		// Connected successfully — reset backoff
		h.SetAMIConnected(true)
		if !firstConnect {
			h.IncAMIReconnects()
		}
		firstConnect = false
		reconnectDelay = 5 * time.Second
		log.Println("[AMI] Connected and logged in")

		// Wait for connection loss or app shutdown
		select {
		case <-client.Done():
			h.SetAMIConnected(false)
			log.Println("[AMI] Connection lost, reconnecting...")
			client.Close()
			amiCancel()
		case <-ctx.Done():
			client.Close()
			amiCancel()
			return
		}

		// Small pause before reconnecting
		select {
		case <-time.After(2 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Web UI fetcher: debounced fetch triggered by AMI signals
// ---------------------------------------------------------------------------

// runWebUIFetcher reads AMI signals and debounces them. After a 30-second
// quiet period it fetches the latest SMS from the Web UI, stores new records,
// and notifies the Telegram bot.
func runWebUIFetcher(ctx context.Context, s *store.Store, bot *tgbot.Bot, h *health.Tracker, webUI *webUISession, smsSignal <-chan struct{}) {
	const debounceDelay = 30 * time.Second

	var debounceTimer <-chan time.Time

	for {
		select {
		case <-smsSignal:
			log.Printf("[Fetcher] AMI signal received, debounce timer started (%v)", debounceDelay)
			// Reset the debounce timer
			debounceTimer = time.After(debounceDelay)
		case <-debounceTimer:
			debounceTimer = nil
			log.Printf("[Fetcher] Debounce timer fired, fetching from Web UI...")
			fetchAndNotify(s, bot, h, webUI)
		case <-ctx.Done():
			return
		}
	}
}

// fetchAndNotify fetches recent SMS from the Web UI and stores + notifies
// any new records.
func fetchAndNotify(s *store.Store, bot *tgbot.Bot, h *health.Tracker, webUI *webUISession) {
	records, err := webUI.fetchRecent()
	if err != nil {
		h.SetWebUIError(err.Error())
		log.Printf("[Fetcher] WebUI fetch failed: %v", err)
		return
	}

	newCount := processWebUIRecords(records, s, bot)
	h.SetWebUISync()
	log.Printf("[Fetcher] Fetch complete: %d total, %d new", len(records), newCount)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// processWebUIRecords decodes, stores, and notifies about Web UI SMS records.
// It returns the count of newly inserted records.
func processWebUIRecords(records []api.WebSMSRecord, s *store.Store, bot *tgbot.Bot) int {
	newCount := 0
	for _, r := range records {
		content, decodeErr := r.DecodedContent()
		if decodeErr != nil {
			log.Printf("[WebUI] Decode SMS %s failed: %v", r.ID, decodeErr)
			continue
		}

		sms := &store.SMS{
			ID:          r.ID,
			Port:        r.Port,
			Sender:      r.Sender,
			DateTime:    r.DateTime,
			Content:     content,
			Read:        r.Read,
			ContactName: r.ContactName,
		}

		inserted, saveErr := s.Save(sms)
		if saveErr != nil {
			log.Printf("[WebUI] Save SMS %s failed: %v", r.ID, saveErr)
			continue
		}
		if inserted {
			newCount++
			bot.NotifySMS(sms)
		}
	}
	return newCount
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
