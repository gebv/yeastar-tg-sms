// Package tgbot provides a Telegram bot that forwards incoming SMS
// from a Yeastar TG GSM gateway and allows querying recent messages.
package tgbot

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"golang.org/x/net/proxy"

	"github.com/gebv/yeastar-tg-sms/internal/health"
	"github.com/gebv/yeastar-tg-sms/internal/store"
)

const (
	// last5BtnText is the label of the reply-keyboard button for last 5 SMS.
	last5BtnText = "📋 Последние 5 СМС"

	// statusBtnText is the label of the reply-keyboard button for bot status.
	statusBtnText = "📊 Статус"

	// maxMsgLen is the Telegram message length limit.
	maxMsgLen = 4096
)

// Bot wraps a Telegram bot API and forwards SMS notifications to a
// dedicated chat. It also provides a "last 5" query and a "status"
// query via reply-keyboard buttons, inline buttons, and commands.
type Bot struct {
	api    *tgbotapi.BotAPI
	chatID int64
	store  *store.Store
	health *health.Tracker
	smsCh  chan *store.SMS
	done   chan struct{}
}

// New creates a Bot. The bot will only process messages from chatID;
// all other chats are silently ignored.
// If proxyURL is non-empty, all Telegram API requests are routed through
// the specified SOCKS5 proxy (e.g. "socks5://user:pass@host:port").
func New(token string, chatID int64, s *store.Store, h *health.Tracker, proxyURL string) (*Bot, error) {
	var httpClient *http.Client

	if proxyURL != "" {
		client, err := newSOCKS5Client(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("configure SOCKS5 proxy: %w", err)
		}
		httpClient = client
		log.Printf("[BOT] Using SOCKS5 proxy: %s", proxyURL)
	}

	var api *tgbotapi.BotAPI
	var err error

	if httpClient != nil {
		api, err = tgbotapi.NewBotAPIWithClient(token, tgbotapi.APIEndpoint, httpClient)
	} else {
		api, err = tgbotapi.NewBotAPI(token)
	}
	if err != nil {
		return nil, fmt.Errorf("create bot api: %w", err)
	}

	api.Debug = false

	return &Bot{
		api:    api,
		chatID: chatID,
		store:  s,
		health: h,
		smsCh:  make(chan *store.SMS, 256),
		done:   make(chan struct{}),
	}, nil
}

// newSOCKS5Client creates an HTTP client that routes all connections through
// the given SOCKS5 proxy URL. Supported formats:
//   - socks5://host:port
//   - socks5://user:pass@host:port
func newSOCKS5Client(proxyURL string) (*http.Client, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}

	if u.Scheme != "socks5" {
		return nil, fmt.Errorf("unsupported proxy scheme %q (only \"socks5\" is supported)", u.Scheme)
	}

	// Extract auth info before creating the dialer (proxy.SOCKS5 reads
	// User from the URL directly, but we handle it ourselves for clarity).
	var auth *proxy.Auth
	if u.User != nil {
		auth = &proxy.Auth{
			User:     u.User.Username(),
			Password: "",
		}
		if p, ok := u.User.Password(); ok {
			auth.Password = p
		}
	}

	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}

	transport := &http.Transport{
		DialContext: dialer.(proxy.ContextDialer).DialContext,
	}

	return &http.Client{Transport: transport}, nil
}

// NotifySMS enqueues an SMS for delivery to the Telegram chat.
// It never blocks: if the channel is full the message is dropped
// and a warning is logged.
func (b *Bot) NotifySMS(sms *store.SMS) {
	select {
	case b.smsCh <- sms:
	default:
		log.Printf("[WARN] SMS notification channel full, dropping message from %s", sms.Sender)
	}
}

// Run starts the bot's update loop. It blocks until Stop is called.
func (b *Bot) Run() {
	log.Printf("[BOT] started as @%s", b.api.Self.UserName)

	go b.dispatchSMS()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30
	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case update, ok := <-updates:
			if !ok {
				return
			}
			b.handleUpdate(update)
		case <-b.done:
			b.api.StopReceivingUpdates()
			return
		}
	}
}

// Stop signals the bot to stop and returns immediately.
func (b *Bot) Stop() {
	close(b.done)
}

// ---------------------------------------------------------------------------
// Update handling
// ---------------------------------------------------------------------------

func (b *Bot) handleUpdate(update tgbotapi.Update) {
	switch {
	case update.Message != nil:
		if update.Message.Chat.ID != b.chatID {
			return
		}
		b.handleMessage(update.Message)
	case update.CallbackQuery != nil:
		b.handleCallback(update.CallbackQuery)
	}
}

func (b *Bot) handleMessage(msg *tgbotapi.Message) {
	switch {
	case msg.IsCommand():
		switch msg.Command() {
		case "start":
			b.sendWelcome(msg.Chat.ID)
		case "last5":
			b.sendLast5(msg.Chat.ID)
		case "status":
			b.sendStatus(msg.Chat.ID)
		}
	case msg.Text == last5BtnText:
		b.sendLast5(msg.Chat.ID)
	case msg.Text == statusBtnText:
		b.sendStatus(msg.Chat.ID)
	}
}

func (b *Bot) handleCallback(cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || cb.Message.Chat.ID != b.chatID {
		return
	}

	switch cb.Data {
	case "last5":
		b.sendLast5(cb.Message.Chat.ID)
	case "status":
		b.sendStatus(cb.Message.Chat.ID)
	}

	callback := tgbotapi.NewCallback(cb.ID, "")
	if _, err := b.api.Request(callback); err != nil {
		log.Printf("[ERROR] callback answer: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Keyboard helpers
// ---------------------------------------------------------------------------

// mainReplyKeyboard returns the persistent reply keyboard with the two main buttons.
func mainReplyKeyboard() tgbotapi.ReplyKeyboardMarkup {
	return tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(last5BtnText),
			tgbotapi.NewKeyboardButton(statusBtnText),
		),
	)
}

// last5InlineKeyboard returns an inline keyboard with refresh and status buttons.
func last5InlineKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Обновить", "last5"),
			tgbotapi.NewInlineKeyboardButtonData("📊 Статус", "status"),
		),
	)
}

// ---------------------------------------------------------------------------
// Outgoing messages
// ---------------------------------------------------------------------------

func (b *Bot) sendWelcome(chatID int64) {
	text := "🤖 *Yeastar SMS Gateway*\n\n" +
		"Я пересылаю входящие СМС с GSM\\-шлюза Yeastar в этот чат\\.\n\n" +
		"*Команды:*\n" +
		"/last5 — показать последние 5 СМС\n" +
		"/status — статус бота\n\n" +
		"Или нажмите кнопки внизу экрана ⬇️"

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.ReplyMarkup = mainReplyKeyboard()

	if _, err := b.api.Send(msg); err != nil {
		log.Printf("[ERROR] send welcome: %v", err)
	}
}

func (b *Bot) sendLast5(chatID int64) {
	messages, err := b.store.GetLastN(5)
	if err != nil {
		b.sendTextMD(chatID, fmt.Sprintf("❌ Ошибка: `%s`", escMDV2(err.Error())))
		return
	}

	if len(messages) == 0 {
		b.sendTextMD(chatID, "📭 Нет сообщений")
		return
	}

	var sb strings.Builder
	sb.WriteString("📋 *Последние СМС:*\n\n")

	for i, sms := range messages {
		sb.WriteString(fmt.Sprintf("%d\\. *%s* — %s\n", i+1, escMDV2(sms.Sender), escMDV2(sms.DateTime)))
		sb.WriteString("||" + escMDV2(sms.Content) + "||")
		sb.WriteString("\n\n")
	}

	text := sb.String()
	if len(text) > maxMsgLen {
		text = text[:maxMsgLen-3] + "…"
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.ReplyMarkup = last5InlineKeyboard()

	if _, err := b.api.Send(msg); err != nil {
		log.Printf("[ERROR] send last5: %v", err)
	}
}

func (b *Bot) sendStatus(chatID int64) {
	snap := b.health.Snapshot()

	// Count stored SMS
	smsCount, err := b.store.Count()
	if err != nil {
		smsCount = -1
	}

	var sb strings.Builder
	sb.WriteString(snap.FormatStatus())

	if smsCount >= 0 {
		sb.WriteString(fmt.Sprintf("\n💾 СМС в базе: %d\n", smsCount))
	}

	text := sb.String()
	if len(text) > maxMsgLen {
		text = text[:maxMsgLen-3] + "…"
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Обновить", "status"),
		),
	)

	if _, err := b.api.Send(msg); err != nil {
		log.Printf("[ERROR] send status: %v", err)
	}
}

func (b *Bot) sendSMSNotification(sms *store.SMS) {
	text := fmt.Sprintf(
		"📨 *Входящее СМС*\n"+
			"📱 От: *%s*\n"+
			"🔌 Порт: %d\n"+
			"🕐 %s\n\n"+
			"||%s||",
		escMDV2(sms.Sender),
		sms.Port,
		escMDV2(sms.DateTime),
		escMDV2(sms.Content),
	)

	if len(text) > maxMsgLen {
		text = text[:maxMsgLen-3] + "…"
	}

	msg := tgbotapi.NewMessage(b.chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2

	if _, err := b.api.Send(msg); err != nil {
		log.Printf("[ERROR] send SMS notification: %v", err)
	} else {
		b.health.IncSMSSent()
	}
}

func (b *Bot) sendTextMD(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdownV2
	if _, err := b.api.Send(msg); err != nil {
		log.Printf("[ERROR] send text: %v", err)
	}
}

// dispatchSMS reads from the SMS channel and sends notifications.
// It exits when the channel is closed.
func (b *Bot) dispatchSMS() {
	for sms := range b.smsCh {
		b.sendSMSNotification(sms)
	}
}

// ---------------------------------------------------------------------------
// MarkdownV2 escaping
// ---------------------------------------------------------------------------

// escMDV2 escapes all characters that Telegram MarkdownV2 requires to be
// escaped: _ * [ ] ( ) ~ ` > # + - = | { } . !
func escMDV2(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) + len(s)/10) // ~10% overhead estimate
	for _, c := range s {
		switch c {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			sb.WriteByte('\\')
			sb.WriteRune(c)
		default:
			sb.WriteRune(c)
		}
	}
	return sb.String()
}
