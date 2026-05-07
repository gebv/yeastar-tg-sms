package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// sanitizeAMI removes sensitive fields from AMI log lines.
// It keeps the event/action type but redacts Content, Sender, Secret,
// Username, Smsc, and Command fields.
func sanitizeAMI(line string) string {
	if idx := strings.Index(line, ": "); idx >= 0 {
		key := strings.ToLower(line[:idx])
		switch key {
		case "content", "sender", "secret", "username", "smsc", "command":
			return line[:idx+2] + "[REDACTED]"
		}
	}
	return line
}

// sanitizeAMIMessage sanitizes a multi-line AMI message (slice of lines).
func sanitizeAMIMessage(msg []string) string {
	var b strings.Builder
	for i, line := range msg {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(sanitizeAMI(line))
	}
	return b.String()
}

// sanitizeAMIMultiLine sanitizes a multi-line AMI command string (joined by \r\n).
func sanitizeAMIMultiLine(s string) string {
	lines := strings.Split(s, "\r\n")
	for i, line := range lines {
		lines[i] = sanitizeAMI(line)
	}
	return strings.Join(lines, "\r\n")
}

// EventType represents the type of AMI event.
type EventType string

const (
	EventReceivedSMS   EventType = "ReceivedSMS"
	EventUpdateSMSSend EventType = "UpdateSMSSend"
)

// SMSReceived represents a parsed incoming SMS event from AMI.
// AMI fires "Event: ReceivedSMS" with the following fields:
//
//	Event: ReceivedSMS
//	Privilege: all,smscommand
//	ID: 244
//	GsmSpan: 2
//	Sender: +00000000000
//	Recvtime: 2025-03-06 06:12:09
//	Index: 1
//	Total: 2
//	Smsc: +00000000000
//	Content: %EF%BB%BF%D0%95%D1%89%D1%91+%D0%BE%D1%82%D0%B2%D0%B5%D1%82+
//	--END SMS EVENT--
type SMSReceived struct {
	ID       string // Message ID (shared for multipart)
	GsmSpan  string // GSM port number
	Sender   string // Sender phone number
	RecvTime string // Reception timestamp (e.g. "2025-03-06 06:12:09")
	Smsc     string // SMS center number
	Index    int    // Part index (1-based, for multipart messages)
	Total    int    // Total number of parts
	Content  string // Raw URL-encoded content (use DecodeSMSContent to get plaintext)
}

// DecodeSMSContent decodes the URL-encoded UTF-8 SMS content from AMI ReceivedSMS events.
// The content is percent-encoded UTF-8, where '+' represents a space.
// It may start with a UTF-8 BOM (\xEF\xBB\xBF) which is stripped.
func (s *SMSReceived) DecodeSMSContent() (string, error) {
	decoded, err := url.QueryUnescape(s.Content)
	if err != nil {
		return "", fmt.Errorf("failed to URL-decode SMS content: %w", err)
	}
	// Strip UTF-8 BOM if present
	decoded = strings.TrimPrefix(decoded, "\ufeff")
	return decoded, nil
}

// IsComplete returns true if this is a single-part message or the last part of a multipart message.
func (s *SMSReceived) IsComplete() bool {
	return s.Total <= 1 || s.Index == s.Total
}

// SMSSendStatus represents the delivery status update for a sent SMS.
// AMI fires "Event: UpdateSMSSend" with fields:
//
//	Event: UpdateSMSSend
//	Privilege: all,smscommand
//	ID: (null)
//	Smsc: +00000000000
//	Status: 1
//	--END SMS EVENT--
//
// Status: 1 = delivered, 0 = failed
type SMSSendStatus struct {
	ID     string // Message ID (may be "(null)")
	Smsc   string // SMS center number
	Status string // "1" = delivered, "0" = failed
}

// IsDelivered returns true if the SMS was successfully delivered.
func (s *SMSSendStatus) IsDelivered() bool {
	return s.Status == "1"
}

// Handler is the interface for processing AMI events.
type Handler interface {
	OnSMSReceived(sms SMSReceived)
	OnSMSSendStatus(status SMSSendStatus)
}

// HandlerFunc is a convenience type for implementing Handler with functions.
type HandlerFunc struct {
	SMSReceivedFn    func(sms SMSReceived)
	SMSSendStatusFn  func(status SMSSendStatus)
}

func (h HandlerFunc) OnSMSReceived(sms SMSReceived) {
	if h.SMSReceivedFn != nil {
		h.SMSReceivedFn(sms)
	}
}

func (h HandlerFunc) OnSMSSendStatus(status SMSSendStatus) {
	if h.SMSSendStatusFn != nil {
		h.SMSSendStatusFn(status)
	}
}

// Client is an AMI (Asterisk Manager Interface) client for the Yeastar TG gateway.
// It connects via TCP to the AMI port (default 5038) and communicates using
// the AMI text protocol with \r\n line separators.
type Client struct {
	conn      net.Conn
	cmdChan   chan string
	incoming  chan []string // raw messages from the reader goroutine
	responses chan []string // non-event responses forwarded by the dispatcher
	handler   Handler
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	done      chan struct{} // closed when the reader goroutine exits (connection lost)
}

// Config holds the configuration for a Yeastar AMI client.
type Config struct {
	Addr     string        // Address in "host:port" format (e.g. "GATEWAY_IP:5038")
	Username string        // AMI username
	Password string        // AMI password/secret
	Handler  Handler       // Event handler for incoming events
}

// New creates a new AMI client and connects to the Yeastar gateway.
// It logs in using the provided credentials and starts processing events.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Handler == nil {
		cfg.Handler = HandlerFunc{} // no-op handler
	}

	ctx, cancel := context.WithCancel(ctx)

	var dial net.Dialer
	conn, err := dial.DialContext(ctx, "tcp", cfg.Addr)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	c := &Client{
		conn:      conn,
		cmdChan:   make(chan string, 64),
		incoming:  make(chan []string, 256),
		responses: make(chan []string, 32),
		handler:   cfg.Handler,
		cancel:    cancel,
		done:      make(chan struct{}),
	}

	// Start reader and writer first. The event dispatcher is NOT started
	// yet — we need to read the banner and login response synchronously
	// from c.incoming before the dispatcher starts consuming that channel.
	c.wg.Add(2)
	go c.runReader()
	go c.runWriter()

	// Read the Asterisk Call Manager banner (reads directly from c.incoming)
	if err := c.readBanner(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("failed to read AMI banner: %w", err)
	}

	// Login (sends command, then reads response from c.incoming)
	if err := c.Login(ctx, cfg.Username, cfg.Password); err != nil {
		c.Close()
		return nil, err
	}

	// Login succeeded — now start the event dispatcher. From this point on,
	// all messages from c.incoming are routed by the dispatcher: events go
	// to the handler, responses go to c.responses.
	c.wg.Add(1)
	go c.runEventDispatcher(ctx)

	return c, nil
}

// readBanner reads the initial "Asterisk Call Manager/1.1" banner from the AMI connection.
func (c *Client) readBanner(ctx context.Context) error {
	// The banner is sent as a single line followed by \r\n
	// NOTE: This is called BEFORE the event dispatcher starts,
	// so we read directly from c.incoming.
	log.Printf("[AMI] readBanner: waiting for message from c.incoming...")
	select {
	case msg := <-c.incoming:
		log.Printf("[AMI] readBanner: got message: %v", msg)
		if len(msg) == 0 {
			return fmt.Errorf("empty banner received")
		}
		if !strings.HasPrefix(msg[0], "Asterisk Call Manager") {
			return fmt.Errorf("unexpected banner: %s", msg[0])
		}
		log.Printf("[AMI] Banner OK: %s", msg[0])
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Login authenticates with the AMI using the provided credentials.
// Action: Login\r\nUsername: <user>\r\nSecret: <pass>\r\n\r\n
func (c *Client) Login(ctx context.Context, username, password string) error {
	cmd := fmt.Sprintf("Action: Login\r\nUsername: %s\r\nSecret: %s\r\n\r\n", username, password)
	c.SendCommand(cmd)

	// Wait for login response.
	// NOTE: This is called BEFORE the event dispatcher starts,
	// so we read directly from c.incoming.
	select {
	case msg := <-c.incoming:
		isSuccess := parseLoginResponse(msg) == nil
		log.Printf("[AMI] Login response: success=%v", isSuccess)
		return parseLoginResponse(msg)
	case <-ctx.Done():
		return fmt.Errorf("login cancelled: %w", ctx.Err())
	}
}

func parseLoginResponse(msg []string) error {
	for _, line := range msg {
		if strings.HasPrefix(line, "Response: ") {
			resp := strings.TrimPrefix(line, "Response: ")
			if resp == "Success" {
				return nil
			}
			// Find the message
			for _, l := range msg {
				if strings.HasPrefix(l, "Message: ") {
					return fmt.Errorf("login failed: %s", strings.TrimPrefix(l, "Message: "))
				}
			}
			return fmt.Errorf("login failed with response: %s", resp)
		}
	}
	return fmt.Errorf("invalid login response")
}

// DiscoverSIMCards lists all GSM spans (SIM cards) on the gateway.
func (c *Client) DiscoverSIMCards() {
	c.SendCommand("Action: SMSCommand\r\ncommand: gsm show spans\r\n\r\n")
}

// SIMInfo requests detailed information about a specific GSM span.
// Port is 0-indexed (port 0 = GSM span 1).
func (c *Client) SIMInfo(port int) {
	c.SendCommand(fmt.Sprintf("Action: SMSCommand\r\ncommand: gsm show span %d\r\n\r\n", port+1))
}

// SendSMS sends an SMS message via the specified GSM port.
// Port is 0-indexed (port 0 = GSM span 1, which the gateway calls "span 1").
// The message is URL-encoded before sending (the gateway expects percent-encoded content).
func (c *Client) SendSMS(port int, dst, msg string) {
	span := port + 1
	encoded := url.QueryEscape(msg)
	c.SendCommand(fmt.Sprintf("Action: SMSCommand\r\ncommand: gsm send sms %d %s \"%s\"\r\n\r\n", span, dst, encoded))
}

// SendCommand sends a raw AMI command string.
// Commands must end with \r\n\r\n.
func (c *Client) SendCommand(cmd string) {
	select {
	case c.cmdChan <- cmd:
	default:
		log.Printf("[WARN] command channel full, dropping command")
	}
}

// Close shuts down the client, closing the connection and waiting for goroutines.
func (c *Client) Close() {
	c.cancel()
	close(c.cmdChan)
	c.conn.Close()
	c.wg.Wait()
}

// Done returns a channel that is closed when the reader goroutine exits,
// typically due to a lost connection. Use this to detect connection loss
// and trigger reconnection logic.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// runReader reads lines from the AMI connection and assembles them into messages.
// AMI messages are separated by blank lines (\r\n\r\n).
// Lines within a message are separated by \r\n.
func (c *Client) runReader() {
	defer c.wg.Done()
	defer close(c.done) // signal connection lost

	scanner := bufio.NewScanner(c.conn)
	scanner.Split(func(data []byte, eof bool) (advance int, token []byte, err error) {
		if i := bytes.Index(data, []byte{'\r', '\n'}); i >= 0 {
			return i + 2, data[0:i], nil
		}
		if eof && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})

	var buffer []string
	for scanner.Scan() {
		line := scanner.Text()
		log.Printf("[AMI] << %s", sanitizeAMI(line))

		// Special case: the AMI banner ("Asterisk Call Manager/X.Y") is a
		// single-line message that may NOT be followed by a blank line.
		// Send it immediately so readBanner() doesn't hang.
		if len(buffer) == 0 && strings.HasPrefix(line, "Asterisk Call Manager") {
			log.Printf("[READER] detected AMI banner, sending immediately: %s", line)
			select {
			case c.incoming <- []string{line}:
			default:
				log.Printf("[WARN] incoming channel full, dropping banner")
			}
			continue
		}

		if line == "" {
			if len(buffer) > 0 {
				select {
				case c.incoming <- buffer:
				default:
					log.Printf("[WARN] incoming channel full, dropping message")
				}
				buffer = nil
			}
		} else {
			buffer = append(buffer, line)
		}
	}

	// Flush remaining buffer
	if len(buffer) > 0 {
		select {
		case c.incoming <- buffer:
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("[ERROR] scanner error: %v", err)
	}
}

// runWriter sends commands from the command channel to the AMI connection.
func (c *Client) runWriter() {
	defer c.wg.Done()

	for cmd := range c.cmdChan {
		log.Printf("[AMI] >> %s", sanitizeAMIMultiLine(cmd))
		_, err := c.conn.Write([]byte(cmd))
		if err != nil {
			log.Printf("[ERROR] write error: %v", err)
			return
		}
	}
}

// runEventDispatcher reads incoming AMI messages and dispatches them to the handler.
// Non-event messages (responses) are forwarded to the responses channel for
// synchronous reads by Login, readBanner, etc.
func (c *Client) runEventDispatcher(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case msg := <-c.incoming:
			if isAMIMessageEvent(msg) {
				c.dispatchEvent(msg)
			} else {
				// It's a response (like login result), forward it to the responses channel
				log.Printf("[DISPATCHER] forwarding non-event message to responses: %s", sanitizeAMIMessage(msg))
				select {
				case c.responses <- msg:
				case <-ctx.Done():
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// isAMIMessageEvent returns true if the message is an AMI event (starts with "Event: ").
func isAMIMessageEvent(msg []string) bool {
	for _, line := range msg {
		if strings.HasPrefix(line, "Event: ") {
			eventName := strings.TrimPrefix(line, "Event: ")
			// "Newstate", "Newchannel", etc. are events
			// But some events like "Follows" in "Response: Follows" are not
			// Check if there's also a "Response:" header - if so, it's a response, not a standalone event
			for _, l := range msg {
				if strings.HasPrefix(l, "Response: ") {
					return false
				}
			}
			// Only dispatch events we care about
			switch EventType(eventName) {
			case EventReceivedSMS, EventUpdateSMSSend:
				return true
			default:
				return false
			}
		}
	}
	return false
}

// dispatchEvent parses an AMI event message and calls the appropriate handler method.
func (c *Client) dispatchEvent(msg []string) {
	fields := parseAMIFields(msg)

	eventName := fields["Event"]
	switch EventType(eventName) {
	case EventReceivedSMS:
		sms := parseReceivedSMS(fields)
		c.handler.OnSMSReceived(sms)
	case EventUpdateSMSSend:
		status := parseSMSSendStatus(fields)
		c.handler.OnSMSSendStatus(status)
	}
}

// parseAMIFields parses key-value pairs from AMI message lines.
// Each line is in "Key: Value" format.
func parseAMIFields(msg []string) map[string]string {
	fields := make(map[string]string, len(msg))
	for _, line := range msg {
		if idx := strings.Index(line, ": "); idx >= 0 {
			key := line[:idx]
			val := line[idx+2:]
			fields[key] = val
		}
	}
	return fields
}

// parseReceivedSMS creates an SMSReceived from parsed AMI fields.
func parseReceivedSMS(fields map[string]string) SMSReceived {
	sms := SMSReceived{
		ID:       fields["ID"],
		GsmSpan:  fields["GsmSpan"],
		Sender:   fields["Sender"],
		RecvTime: fields["Recvtime"],
		Smsc:     fields["Smsc"],
		Content:  fields["Content"],
	}
	// Parse numeric fields
	if v, err := parseInt(fields["Index"]); err == nil {
		sms.Index = v
	}
	if v, err := parseInt(fields["Total"]); err == nil {
		sms.Total = v
	}
	// Default Total to 1 if not set (single-part message)
	if sms.Total == 0 {
		sms.Total = 1
	}
	// Default Index to 1 if not set
	if sms.Index == 0 {
		sms.Index = 1
	}
	return sms
}

// parseSMSSendStatus creates an SMSSendStatus from parsed AMI fields.
func parseSMSSendStatus(fields map[string]string) SMSSendStatus {
	return SMSSendStatus{
		ID:     fields["ID"],
		Smsc:   fields["Smsc"],
		Status: fields["Status"],
	}
}

func parseInt(s string) (int, error) {
	var v int
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		v = v*10 + int(c-'0')
	}
	if v == 0 && s != "0" {
		return 0, fmt.Errorf("invalid integer: %q", s)
	}
	return v, nil
}

// MultipartCollector collects SMS parts and assembles complete multipart messages.
// Yeastar sends multipart SMS as separate ReceivedSMS events with the same ID
// but different Index/Total values. Use this to reassemble them.
type MultipartCollector struct {
	mu    sync.Mutex
	parts map[string][]SMSReceived // key = message ID
}

// NewMultipartCollector creates a new MultipartCollector.
func NewMultipartCollector() *MultipartCollector {
	return &MultipartCollector{
		parts: make(map[string][]SMSReceived),
	}
}

// Add adds an SMS part and returns the complete assembled message if all parts are received.
// For single-part messages, it returns the decoded content immediately.
func (mc *MultipartCollector) Add(sms SMSReceived) *AssembledSMS {
	if sms.Total <= 1 {
		content, err := sms.DecodeSMSContent()
		if err != nil {
			log.Printf("[ERROR] decoding single SMS: %v", err)
			return nil
		}
		return &AssembledSMS{
			ID:       sms.ID,
			GsmSpan:  sms.GsmSpan,
			Sender:   sms.Sender,
			RecvTime: sms.RecvTime,
			Smsc:     sms.Smsc,
			Content:  content,
		}
	}

	mc.mu.Lock()
	defer mc.mu.Unlock()

	id := sms.ID
	mc.parts[id] = append(mc.parts[id], sms)

	// Check if all parts are collected
	if len(mc.parts[id]) >= sms.Total {
		parts := mc.parts[id]
		delete(mc.parts, id)

		return mc.assemble(parts)
	}

	return nil
}

// assemble combines SMS parts into a single AssembledSMS.
func (mc *MultipartCollector) assemble(parts []SMSReceived) *AssembledSMS {
	if len(parts) == 0 {
		return nil
	}

	// Sort by Index
	sorted := make([]SMSReceived, len(parts))
	copy(sorted, parts)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].Index > sorted[j].Index {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Decode and concatenate content
	var content strings.Builder
	for _, part := range sorted {
		decoded, err := part.DecodeSMSContent()
		if err != nil {
			log.Printf("[ERROR] decoding SMS part %d/%d: %v", part.Index, part.Total, err)
			continue
		}
		content.WriteString(decoded)
	}

	// Use the first part's metadata
	first := sorted[0]
	return &AssembledSMS{
		ID:       first.ID,
		GsmSpan:  first.GsmSpan,
		Sender:   first.Sender,
		RecvTime: first.RecvTime,
		Smsc:     first.Smsc,
		Content:  content.String(),
	}
}

// AssembledSMS represents a fully assembled SMS message (single or multipart).
type AssembledSMS struct {
	ID       string
	GsmSpan  string
	Sender   string
	RecvTime string
	Smsc     string
	Content  string
}

// ParseTime attempts to parse the RecvTime field (format: "2006-01-02 15:04:05").
func (a *AssembledSMS) ParseTime() (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05", a.RecvTime)
}
