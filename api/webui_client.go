package api

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// WebUISession represents an authenticated session with the Yeastar web UI.
// Create a session by calling WebUIClient.Login().
// The session maintains cookies and handles pagination automatically.
type WebUISession struct {
	client     *WebUIClient
	httpClient *http.Client
}

// newLenientTransport creates an http.Transport that fixes malformed HTTP headers
// returned by the Yeastar Boa web server before they reach Go's strict MIME parser.
//
// The Yeastar gateway returns headers like "<Content-Type:text/html>" (with a leading
// '<' and no space after the colon), which Go's textproto reader rejects with
// "malformed MIME header line". This transport wraps the TCP connection to sanitize
// the response data on-the-fly.
func newLenientTransport() *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 30 * time.Second}
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &headerFixingConn{Conn: conn}, nil
		},
	}
}

// headerFixingConn wraps a net.Conn and fixes malformed HTTP headers in the
// response data stream. Specifically, it fixes the Yeastar Boa server's
// "<Content-Type:text/html>" header by replacing "<Content-Type:" with
// "Content-Type: " in the response bytes.
//
// This works by buffering the response and applying fixes before the data
// reaches Go's HTTP parser. The fix is applied to the first 4KB of the
// response (which always contains the headers).
type headerFixingConn struct {
	net.Conn
	reader io.Reader // buffered reader that applies fixes
}

func (c *headerFixingConn) Read(b []byte) (int, error) {
	if c.reader == nil {
		// Read the initial chunk (headers + possibly some body)
		// and fix malformed headers
		buf := make([]byte, 4096)
		n, err := c.Conn.Read(buf)
		if err != nil && n == 0 {
			return 0, err
		}

		data := buf[:n]
		fixed := fixMalformedHeaders(data)

		c.reader = io.MultiReader(bytes.NewReader(fixed), c.Conn)
	}

	return c.reader.Read(b)
}

// fixMalformedHeaders fixes known malformed HTTP headers in the response data.
// The Yeastar Boa server returns:
//
//	<Content-Type:text/html>
//
// which should be:
//
//	Content-Type: text/html
func fixMalformedHeaders(data []byte) []byte {
	// Fix: "<Content-Type:text/html>" → "Content-Type: text/html"
	// Pattern: "<" followed by a header name, then ":" without space
	s := string(data)

	// Replace leading "<" before known header names
	// This handles: <Content-Type:  → Content-Type:
	s = strings.ReplaceAll(s, "<Content-Type:", "Content-Type: ")
	s = strings.ReplaceAll(s, "<Content-type:", "Content-type: ")

	// Fix any other headers with the same pattern
	// Generic: find lines starting with "<" followed by a header
	// and replace the "<Header:" pattern with "Header: "
	re := regexp.MustCompile(`<([A-Za-z][A-Za-z0-9-]*):`)
	s = re.ReplaceAllString(s, "$1: ")

	return []byte(s)
}



// md5HashHex computes the MD5 hash of a string and returns the lowercase hex encoding.
func md5HashHex(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

// baseURL returns the base URL for the gateway (e.g., "http://GATEWAY_IP").
func (c *WebUIClient) baseURL() string {
	addr := c.Addr
	if !strings.HasPrefix(addr, "http") {
		addr = "http://" + addr
	}
	return strings.TrimRight(addr, "/")
}

// Login authenticates with the Yeastar web UI and returns an authenticated session.
//
// The authentication process follows the same flow as the Yeastar web interface:
//  1. Compute MD5 hash of the password (hex-encoded)
//  2. Base64 encode the hex MD5 hash, then shift each character by +2
//     (Yeastar's custom encoding from /js/base64.js)
//  3. Set authentication cookies (loginname, password, defaultpwd, language, OsVer)
//  4. POST to the login endpoint (/cgi/WebCGI?1000) with username and encoded secret
//
// The returned session maintains cookies (including any session cookies set by the
// server) and can be used for subsequent requests.
func (c *WebUIClient) Login() (*WebUISession, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create cookie jar: %w", err)
	}

	httpClient := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		Transport: newLenientTransport(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}

	session := &WebUISession{
		client:     c,
		httpClient: httpClient,
	}

	// Step 1: Compute the shifted Base64 MD5 password
	// The Yeastar web UI authenticates using: base64(md5(password)) with +2 char shift
	md5Password := md5HashHex(c.Password)
	shiftedPassword := EncodeYeastarBase64Shift(md5Password)

	// Step 2: Set initial authentication cookies on the base URL
	// These are the cookies that the browser's JavaScript sets before submitting the login form.
	// Key insight from curl analysis:
	//   - OsVer can be a placeholder ("xxxx") for the login request itself,
	//     but MUST be set to the real firmware version for subsequent requests.
	//   - defaultpwd is empty for non-default-password users.
	//   - The login response contains the real OsVer in the MyPBX_COMM hidden input
	//     (as "&OSVer:91.3.0.23;") and we must update the cookie before making
	//     any further requests.
	baseURL, _ := url.Parse(c.baseURL())
	cookies := []*http.Cookie{
		{Name: "language", Value: "en"},
		{Name: "loginname", Value: c.Username},
		{Name: "Series", Value: ""},
		{Name: "Product", Value: c.Product},
		{Name: "current", Value: c.Username},
		{Name: "password", Value: url.QueryEscape(shiftedPassword)},
		{Name: "OsVer", Value: "xxxx"}, // placeholder; updated after login
		{Name: "defaultpwd", Value: ""}, // empty for non-default passwords
		{Name: "curUrl", Value: ""},
		{Name: "TabIndex", Value: "0"},
		{Name: "TabIndexwithback", Value: "0"},
	}
	jar.SetCookies(baseURL, cookies)

	// Step 3: POST to the login endpoint
	// The browser submits a form with username and the shifted Base64 MD5 password
	loginURL := c.baseURL() + "/cgi/WebCGI?1000"
	formData := url.Values{
		"username": {c.Username},
		"secret":   {shiftedPassword},
	}

	req, err := http.NewRequest("POST", loginURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", c.baseURL())
	req.Header.Set("Referer", c.baseURL()+"/")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("login failed with HTTP status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read login response: %w", err)
	}

	bodyStr := string(body)

	// Check for login error indicators in the response
	if strings.Contains(bodyStr, "login_error") {
		return nil, fmt.Errorf("login failed: invalid credentials (server returned login_error)")
	}

	// A successful login should return the main page with MyPBX_COMM or at least
	// not contain onLogout
	if strings.Contains(bodyStr, "onLogout") && !strings.Contains(bodyStr, "MyPBX_COMM") {
		return nil, fmt.Errorf("login failed: session not established (server returned onLogout)")
	}

	// Step 4: Extract the real OsVer from the login response's MyPBX_COMM value.
	// The Yeastar web UI JS reads &OSVer: from MyPBX_COMM and updates the OsVer cookie.
	// Without the correct OsVer, subsequent requests (e.g., ?15200) return onLogout.
	mypbxCommValue, err := extractMyPBXComm(bodyStr)
	if err == nil && mypbxCommValue != "" {
		osVer := extractSection(mypbxCommValue, "OSVer:")
		if osVer != "" {
			// Update the OsVer cookie with the real firmware version
			jar.SetCookies(baseURL, []*http.Cookie{
				{Name: "OsVer", Value: osVer},
			})
		}
	}

	return session, nil
}

// myPBXCommRegex extracts the MyPBX_COMM hidden input value from HTML.
// Handles both id="MyPBX_COMM" value="..." and value="..." id="MyPBX_COMM" orderings.
var myPBXCommRegex = regexp.MustCompile(`(?i)<input\s+[^>]*id\s*=\s*"MyPBX_COMM"[^>]*value\s*=\s*"([^"]*)"`)

// myPBXCommRegexAlt handles the case where value comes before id.
var myPBXCommRegexAlt = regexp.MustCompile(`(?i)<input\s+[^>]*value\s*=\s*"([^"]*)"[^>]*id\s*=\s*"MyPBX_COMM"`)

// onLogoutPattern matches session expiration indicators in responses.
var onLogoutPattern = regexp.MustCompile(`(?i)onLogout|login_error`)

// extractMyPBXComm extracts and HTML-unescapes the MyPBX_COMM value from HTML.
func extractMyPBXComm(htmlContent string) (string, error) {
	// Try the primary regex (id before value)
	matches := myPBXCommRegex.FindStringSubmatch(htmlContent)
	if matches == nil || len(matches) < 2 {
		// Try the alternate regex (value before id)
		matches = myPBXCommRegexAlt.FindStringSubmatch(htmlContent)
	}
	if matches == nil || len(matches) < 2 {
		return "", fmt.Errorf("MyPBX_COMM not found in HTML response")
	}

	// HTML-unescape the value (convert &amp; → &, &lt; → <, etc.)
	value := html.UnescapeString(matches[1])
	return value, nil
}

// GetSMSInbox fetches the SMS inbox (received messages) for the given page.
// Page is 1-indexed (page 1 is the first page).
// Returns the parsed MyPBXComm data including SMS records and pagination info.
func (s *WebUISession) GetSMSInbox(startIndex, viewCount int) (*MyPBXComm, error) {
	return s.fetchSMSPage(15200, startIndex, viewCount)
}

// GetSMSSentBox fetches the SMS sent box for the given page.
// Page is 1-indexed.
// Returns the parsed MyPBXComm data including SMS records and pagination info.
func (s *WebUISession) GetSMSSentBox(startIndex, viewCount int) (*MyPBXComm, error) {
	return s.fetchSMSPage(15100, startIndex, viewCount)
}

// fetchSMSPage fetches an SMS page from the Yeastar web UI.
// actionCode is the Yeastar CGI action code (15200=inbox, 15100=sent).
// Page is 1-indexed.
func (s *WebUISession) fetchSMSPage(actionCode, startIndex, viewCount int) (*MyPBXComm, error) {
	smsURL := fmt.Sprintf("%s/cgi/WebCGI?%d", s.client.baseURL(), actionCode)

	var req *http.Request
	var err error

	if startIndex <= 1 {
		// First page: simple GET request (no pagination params needed)
		req, err = http.NewRequest("GET", smsURL, nil)
	} else {
		// Subsequent pages: POST with sstartindex and sviewcount
		// The Yeastar web UI pagination uses these form parameters:
		//   sstartindex=N  — 1-based starting record index
		//   sviewcount=N   — number of records per page
		//   begindate=, enddate=, port=, hasread=, fromnumber= — optional filters
		formData := url.Values{
			"sstartindex": {fmt.Sprintf("%d", startIndex)},
			"sviewcount":  {fmt.Sprintf("%d", viewCount)},
			"begindate":   {""},
			"enddate":     {""},
			"port":        {""},
			"hasread":     {""},
			"fromnumber":  {""},
		}
		req, err = http.NewRequest("POST", smsURL, strings.NewReader(formData.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", s.client.baseURL())
		req.Header.Set("Referer", smsURL)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with HTTP status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	bodyStr := string(body)

	// Check for session expiration
	if onLogoutPattern.MatchString(bodyStr) {
		return nil, fmt.Errorf("session expired or access denied (server returned onLogout/login_error)")
	}

	// Extract MyPBX_COMM value from HTML
	mypbxCommValue, err := extractMyPBXComm(bodyStr)
	if err != nil {
		return nil, fmt.Errorf("failed to extract MyPBX_COMM from page: %w", err)
	}

	// Parse the MyPBX_COMM value
	comm, err := ParseMyPBXComm(mypbxCommValue)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MyPBX_COMM: %w", err)
	}

	return comm, nil
}

// GetAllSMSInbox fetches all pages of the SMS inbox by iterating through pagination.
// It starts at page 1 and continues until all pages have been fetched.
func (s *WebUISession) GetAllSMSInbox() ([]WebSMSRecord, error) {
	return s.getAllSMS(15200)
}

// GetAllSMSSentBox fetches all pages of the SMS sent box by iterating through pagination.
func (s *WebUISession) GetAllSMSSentBox() ([]WebSMSRecord, error) {
	return s.getAllSMS(15100)
}

// getAllSMS fetches all pages of SMS data from the specified action code.
func (s *WebUISession) getAllSMS(actionCode int) ([]WebSMSRecord, error) {
	var allRecords []WebSMSRecord

	// First page: GET request (no pagination params)
	comm, err := s.fetchSMSPage(actionCode, 1, 0)
	if err != nil {
		return nil, fmt.Errorf("page 1: %w", err)
	}

	switch actionCode {
	case 15200:
		allRecords = append(allRecords, comm.SMSRecvList...)
	case 15100:
		allRecords = append(allRecords, comm.SMSSendList...)
	}

	// Use the viewCount from the server response (Yeastar defaults to 25)
	viewCount := comm.DisplayViewCount
	if viewCount <= 0 {
		viewCount = 25 // fallback
	}

	// Check if there are more pages
	// Display format: totalRecords,startIndex,viewCount
	totalPages := comm.TotalPages()
	for page := 2; page <= totalPages; page++ {
		time.Sleep(500 * time.Millisecond) // avoid overwhelming the gateway

		startIndex := (page-1)*viewCount + 1
		comm, err := s.fetchSMSPage(actionCode, startIndex, viewCount)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}

		switch actionCode {
		case 15200:
			allRecords = append(allRecords, comm.SMSRecvList...)
		case 15100:
			allRecords = append(allRecords, comm.SMSSendList...)
		}
	}

	return allRecords, nil
}

// SendSMSViaWebUI sends an SMS message via the Yeastar web UI (action code 15000).
// This is an alternative to sending via AMI. The message is sent from the specified
// GSM port to the destination number.
func (s *WebUISession) SendSMSViaWebUI(port int, dst, msg string) error {
	sendURL := fmt.Sprintf("%s/cgi/WebCGI?15000", s.client.baseURL())

	formData := url.Values{
		"port": {fmt.Sprintf("%d", port)},
		"dst":  {dst},
		"msg":  {msg},
	}

	req, err := http.NewRequest("POST", sendURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create send SMS request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", s.client.baseURL()+"/")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send SMS request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send SMS failed with HTTP status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	bodyStr := string(body)

	// Check for session expiration
	if onLogoutPattern.MatchString(bodyStr) {
		return fmt.Errorf("session expired (server returned onLogout)")
	}

	return nil
}
