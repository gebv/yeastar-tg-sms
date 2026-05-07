package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// --- md5HashHex tests ---

func TestMD5HashHex(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"testpass", "179ad45c6ce2cb97cf1029e212046e81"},
		{"", "d41d8cd98f00b204e9800998ecf8427e"},
		{"password", "5f4dcc3b5aa765d61d8327deb882cf99"},
		{"admin", "21232f297a57a5a743894a0e4a801fc3"},
	}
	for _, tt := range tests {
		got := md5HashHex(tt.input)
		if got != tt.expected {
			t.Errorf("md5HashHex(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMD5HashHex_consistency(t *testing.T) {
	// Calling md5HashHex multiple times on the same input should return the same result
	input := "test_password"
	first := md5HashHex(input)
	second := md5HashHex(input)
	if first != second {
		t.Errorf("md5HashHex is not deterministic: %q != %q", first, second)
	}
}

// --- baseURL tests ---

func TestWebUIClient_baseURL(t *testing.T) {
	tests := []struct {
		addr     string
		expected string
	}{
		{"127.0.0.1", "http://127.0.0.1"},
		{"http://127.0.0.1", "http://127.0.0.1"},
		{"https://127.0.0.1", "https://127.0.0.1"},
		{"127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://127.0.0.1:8080", "http://127.0.0.1:8080"},
		{"http://127.0.0.1/", "http://127.0.0.1"},
		{"http://127.0.0.1//", "http://127.0.0.1"},
	}

	for _, tt := range tests {
		client := &WebUIClient{Addr: tt.addr}
		got := client.baseURL()
		if got != tt.expected {
			t.Errorf("baseURL(%q) = %q, want %q", tt.addr, got, tt.expected)
		}
	}
}

// --- extractMyPBXComm tests ---

func TestExtractMyPBXComm_standard(t *testing.T) {
	html := `<html><body><form><input id="MyPBX_COMM" value="&amp;Gsmport:1,2;&amp;SMSRecvList:id1^^1^^Sender1^^2026-01-01^^content1^^No^^C1;&amp;Display:1,25,50;&amp;FilterPlus:0;"></form></body></html>`
	val, err := extractMyPBXComm(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(val, "Gsmport:1,2") {
		t.Errorf("expected Gsmport section in value, got %q", val)
	}
	if !strings.Contains(val, "SMSRecvList:id1") {
		t.Errorf("expected SMSRecvList section in value, got %q", val)
	}
	if !strings.Contains(val, "Display:1,25,50") {
		t.Errorf("expected Display section in value, got %q", val)
	}
}

func TestExtractMyPBXComm_valueBeforeId(t *testing.T) {
	html := `<input value="&amp;Gsmport:1;&amp;FilterPlus:0;" id="MyPBX_COMM">`
	val, err := extractMyPBXComm(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(val, "Gsmport:1") {
		t.Errorf("expected Gsmport section, got %q", val)
	}
}

func TestExtractMyPBXComm_notFound(t *testing.T) {
	html := `<html><body><p>No hidden input here</p></body></html>`
	_, err := extractMyPBXComm(html)
	if err == nil {
		t.Error("expected error when MyPBX_COMM not found, got nil")
	}
}

func TestExtractMyPBXComm_emptyValue(t *testing.T) {
	html := `<input id="MyPBX_COMM" value="">`
	val, err := extractMyPBXComm(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty value, got %q", val)
	}
}

func TestExtractMyPBXComm_htmlEntities(t *testing.T) {
	// Value contains &amp; which should be unescaped to &
	html := `<input id="MyPBX_COMM" value="&amp;Gsmport:1,2;&amp;SMSRecvList:123^^1^^+00000000000^^2026-01-01 00:00:00^^enc^^No^^Contact;&amp;Display:1,25,100;">`
	val, err := extractMyPBXComm(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(val, "&Gsmport:1,2") {
		t.Errorf("expected unescaped & in value, got %q", val)
	}
	if !strings.Contains(val, "&Display:1,25,100") {
		t.Errorf("expected Display section, got %q", val)
	}
}

func TestExtractMyPBXComm_fullPage(t *testing.T) {
	// Simulate a realistic Yeastar HTML page
	// Display format: totalRecords,startIndex,viewCount
	// 141 total, starting at index 1, 25 per page
	html := `<!DOCTYPE html><html><head><title>SMS</title></head><body>
<script src="/js/base64.js"></script>
<form name="form1" method="post" action="/cgi/WebCGI?15200">
<input id="MyPBX_COMM" value="&amp;Gsmport:1,2;&amp;SearchInfos:,,,,;&amp;Display:141,1,25;&amp;SMSRecvList:10395^^2^^+00000000000^^2026-05-05 09:04:58^^99w1abc^^No^^;&amp;FilterPlus:0;">
<input type="submit">
</form></body></html>`
	val, err := extractMyPBXComm(html)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	comm, err := ParseMyPBXComm(val)
	if err != nil {
		t.Fatalf("unexpected error parsing MyPBX_COMM: %v", err)
	}
	if len(comm.SMSRecvList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSRecvList))
	}
	if comm.SMSRecvList[0].Sender != "+00000000000" {
		t.Errorf("expected Sender %q, got %q", "+00000000000", comm.SMSRecvList[0].Sender)
	}
	if comm.DisplayTotal != 141 {
		t.Errorf("expected DisplayTotal 141, got %d", comm.DisplayTotal)
	}
	if comm.DisplayStart != 1 {
		t.Errorf("expected DisplayStart 1, got %d", comm.DisplayStart)
	}
	if comm.DisplayViewCount != 25 {
		t.Errorf("expected DisplayViewCount 25, got %d", comm.DisplayViewCount)
	}
}

// --- Login tests using httptest ---

func TestWebUISession_Login_success(t *testing.T) {
	md5Password := md5HashHex("testpass")
	shiftedPassword := EncodeYeastarBase64Shift(md5Password)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi/WebCGI" && r.URL.Query().Get("1000") == "" && r.Method == "POST" {
			// Login endpoint - simulate successful login
			// Verify form data
			if err := r.ParseForm(); err != nil {
				t.Errorf("failed to parse form: %v", err)
			}
			username := r.FormValue("username")
			secret := r.FormValue("secret")
			if username != "testuser" {
				t.Errorf("expected username 'sms', got %q", username)
			}
			if secret != shiftedPassword {
				t.Errorf("expected secret %q, got %q", shiftedPassword, secret)
			}

			// Set a session cookie
			http.SetCookie(w, &http.Cookie{
				Name:  "sessionid",
				Value: "test-session-123",
			})

			// Return a page with MyPBX_COMM (successful login)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><input id="MyPBX_COMM" value="&amp;Gsmport:1;&amp;Display:1,25,0;"></body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session, err := client.Login()
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if session == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestWebUISession_Login_errorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi/WebCGI" {
			// Simulate login error
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>login_error</body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "wrong_password")
	_, err := client.Login()
	if err == nil {
		t.Fatal("expected login error, got nil")
	}
	if !strings.Contains(err.Error(), "login_error") {
		t.Errorf("expected error containing 'login_error', got %v", err)
	}
}

func TestWebUISession_Login_onLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi/WebCGI" {
			// Simulate session expired response (onLogout without MyPBX_COMM)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>onLogout</body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	_, err := client.Login()
	if err == nil {
		t.Fatal("expected login error for onLogout response, got nil")
	}
	if !strings.Contains(err.Error(), "onLogout") {
		t.Errorf("expected error containing 'onLogout', got %v", err)
	}
}

func TestWebUISession_Login_setsCookies(t *testing.T) {
	md5Password := md5HashHex("testpass")
	shiftedPassword := EncodeYeastarBase64Shift(md5Password)

	var receivedCookies []*http.Cookie

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture cookies sent by the client
		receivedCookies = r.Cookies()

		if r.URL.Path == "/cgi/WebCGI" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><input id="MyPBX_COMM" value="&amp;Gsmport:1;&amp;OSVer:91.3.0.23;"></body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session, err := client.Login()
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	_ = session

	// Verify that authentication cookies were sent
	cookieMap := make(map[string]string)
	for _, c := range receivedCookies {
		cookieMap[c.Name] = c.Value
	}

	if cookieMap["loginname"] != "testuser" {
		t.Errorf("expected loginname cookie 'testuser', got %q", cookieMap["loginname"])
	}

	// Password cookie should be the URL-escaped shifted base64 MD5
	expectedPasswordCookie := url.QueryEscape(shiftedPassword)
	if cookieMap["password"] != expectedPasswordCookie {
		t.Errorf("expected password cookie %q, got %q", expectedPasswordCookie, cookieMap["password"])
	}

	if cookieMap["defaultpwd"] != "" {
		t.Errorf("expected defaultpwd cookie to be empty, got %q", cookieMap["defaultpwd"])
	}

	if cookieMap["language"] != "en" {
		t.Errorf("expected language cookie 'en', got %q", cookieMap["language"])
	}

	if cookieMap["OsVer"] != "xxxx" {
		t.Errorf("expected initial OsVer cookie 'xxxx', got %q", cookieMap["OsVer"])
	}

	if cookieMap["Product"] != "TG100" {
		t.Errorf("expected Product cookie 'TG100', got %q", cookieMap["Product"])
	}

	if cookieMap["current"] != "testuser" {
		t.Errorf("expected current cookie 'testuser', got %q", cookieMap["current"])
	}
}

// TestWebUISession_Login_updatesOsVer verifies that after login, the OsVer cookie
// is updated to the real value extracted from MyPBX_COMM.
func TestWebUISession_Login_updatesOsVer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi/WebCGI" {
			w.Header().Set("Content-Type", "text/html")
			// Login response contains OSVer in MyPBX_COMM
			fmt.Fprint(w, `<html><body><input id="MyPBX_COMM" value="&amp;Info:sms;&amp;OSVer:91.3.0.23;&amp;Product:TG100;"></body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session, err := client.Login()
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	// Now make a request to check that the OsVer cookie was updated
	// The session's cookie jar should have OsVer=91.3.0.23
	baseURL, _ := url.Parse(server.URL)
	cookies := session.httpClient.Jar.Cookies(baseURL)

	osVerFound := false
	for _, c := range cookies {
		if c.Name == "OsVer" && c.Value == "91.3.0.23" {
			osVerFound = true
		}
	}
	if !osVerFound {
		cookieValues := make(map[string]string)
		for _, c := range cookies {
			cookieValues[c.Name] = c.Value
		}
		t.Errorf("expected OsVer cookie to be updated to '91.3.0.23', got cookies: %v", cookieValues)
	}
}

// --- GetSMSInbox tests using httptest ---

func TestWebUISession_GetSMSInbox(t *testing.T) {
	// Display format: totalRecords,startIndex,viewCount
	// 141 total records, starting at index 1, 25 per page
	mypbxCommValue := "&Gsmport:1,2;&Display:141,1,25;&SMSRecvList:10395^^2^^+00000000000^^2026-05-05 09:04:58^^99w1abc^^No^^TestContact;&FilterPlus:0;"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() == "/cgi/WebCGI?15200" {
			w.Header().Set("Content-Type", "text/html")
			htmlValue := strings.ReplaceAll(mypbxCommValue, "&", "&amp;")
			fmt.Fprintf(w, `<html><body><input id="MyPBX_COMM" value="%s"></body></html>`, htmlValue)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	comm, err := session.GetSMSInbox(1, 25)
	if err != nil {
		t.Fatalf("GetSMSInbox failed: %v", err)
	}
	if comm == nil {
		t.Fatal("expected non-nil MyPBXComm")
	}
	if len(comm.SMSRecvList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSRecvList))
	}
	if comm.SMSRecvList[0].ID != "10395" {
		t.Errorf("expected ID %q, got %q", "10395", comm.SMSRecvList[0].ID)
	}
	if comm.SMSRecvList[0].Sender != "+00000000000" {
		t.Errorf("expected Sender %q, got %q", "+00000000000", comm.SMSRecvList[0].Sender)
	}
	if comm.DisplayTotal != 141 {
		t.Errorf("expected DisplayTotal 141, got %d", comm.DisplayTotal)
	}
	if comm.DisplayViewCount != 25 {
		t.Errorf("expected DisplayViewCount 25, got %d", comm.DisplayViewCount)
	}
	if comm.TotalPages() != 6 {
		t.Errorf("expected TotalPages 6, got %d", comm.TotalPages())
	}
}

func TestWebUISession_GetSMSSentBox(t *testing.T) {
	mypbxCommValue := "&Gsmport:1;&Display:1,25,2;&SMSSendList:id1^^1^^+00000000000^^2026-01-01^^enc^^Yes^^;&FilterPlus:0;"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() == "/cgi/WebCGI?15100" {
			w.Header().Set("Content-Type", "text/html")
			htmlValue := strings.ReplaceAll(mypbxCommValue, "&", "&amp;")
			fmt.Fprintf(w, `<html><body><input id="MyPBX_COMM" value="%s"></body></html>`, htmlValue)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	comm, err := session.GetSMSSentBox(1, 25)
	if err != nil {
		t.Fatalf("GetSMSSentBox failed: %v", err)
	}
	if comm == nil {
		t.Fatal("expected non-nil MyPBXComm")
	}
	if len(comm.SMSSendList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSSendList))
	}
	if comm.SMSSendList[0].ID != "id1" {
		t.Errorf("expected ID %q, got %q", "id1", comm.SMSSendList[0].ID)
	}
}

func TestWebUISession_GetSMSInbox_sessionExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>onLogout</body></html>`)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	_, err := session.GetSMSInbox(1, 25)
	if err == nil {
		t.Fatal("expected error for onLogout response, got nil")
	}
	if !strings.Contains(err.Error(), "onLogout") && !strings.Contains(err.Error(), "login_error") {
		t.Errorf("expected error containing 'onLogout' or 'login_error', got %v", err)
	}
}

func TestWebUISession_GetSMSInbox_noMyPBXComm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><p>No SMS data here</p></body></html>`)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	_, err := session.GetSMSInbox(1, 25)
	if err == nil {
		t.Fatal("expected error when MyPBX_COMM not found, got nil")
	}
}

func TestWebUISession_GetAllSMSInbox_pagination(t *testing.T) {
	// Display format: totalRecords,startIndex,viewCount
	// Total 5 records, 25 per page (fits in one page)
	// This tests the basic pagination logic without needing multiple pages
	mypbxCommValue := "&Gsmport:1;&Display:5,1,25;&SMSRecvList:id1^^1^^+00000000000^^2026-01-01^^enc1^^No^^Contact1;id2^^1^^+00000000001^^2026-01-02^^enc2^^No^^Contact2;&FilterPlus:0;"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		htmlValue := strings.ReplaceAll(mypbxCommValue, "&", "&amp;")
		fmt.Fprintf(w, `<html><body><input id="MyPBX_COMM" value="%s"></body></html>`, htmlValue)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	records, err := session.GetAllSMSInbox()
	if err != nil {
		t.Fatalf("GetAllSMSInbox failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 SMS records, got %d", len(records))
	}
	if records[0].ID != "id1" {
		t.Errorf("record 0: expected ID %q, got %q", "id1", records[0].ID)
	}
	if records[1].ID != "id2" {
		t.Errorf("record 1: expected ID %q, got %q", "id2", records[1].ID)
	}
	// With 5 total and 25 per page, TotalPages = 1, so no second page request needed
}

// --- SendSMSViaWebUI test ---

func TestWebUISession_SendSMSViaWebUI(t *testing.T) {
	var receivedPort, receivedDst, receivedMsg string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() == "/cgi/WebCGI?15000" && r.Method == "POST" {
			if err := r.ParseForm(); err != nil {
				t.Errorf("failed to parse form: %v", err)
			}
			receivedPort = r.FormValue("port")
			receivedDst = r.FormValue("dst")
			receivedMsg = r.FormValue("msg")

			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body>OK</body></html>`)
			return
		}
		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	err := session.SendSMSViaWebUI(1, "+00000000000", "Test message")
	if err != nil {
		t.Fatalf("SendSMSViaWebUI failed: %v", err)
	}

	if receivedPort != "1" {
		t.Errorf("expected port '1', got %q", receivedPort)
	}
	if receivedDst != "+00000000000" {
		t.Errorf("expected dst '+00000000000', got %q", receivedDst)
	}
	if receivedMsg != "Test message" {
		t.Errorf("expected msg 'Test message', got %q", receivedMsg)
	}
}

func TestWebUISession_SendSMSViaWebUI_sessionExpired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>onLogout</body></html>`)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session := &WebUISession{
		client:     client,
		httpClient: server.Client(),
	}

	err := session.SendSMSViaWebUI(1, "+00000000000", "Test")
	if err == nil {
		t.Fatal("expected error for onLogout response, got nil")
	}
	if !strings.Contains(err.Error(), "onLogout") {
		t.Errorf("expected error containing 'onLogout', got %v", err)
	}
}

// --- Integration: Login + GetSMSInbox ---

func TestWebUISession_LoginThenGetSMSInbox(t *testing.T) {
	md5Password := md5HashHex("testpass")
	shiftedPassword := EncodeYeastarBase64Shift(md5Password)

	mypbxCommValue := "&Gsmport:1;&Display:1,25,5;&SMSRecvList:id1^^1^^+00000000000^^2026-01-01 00:00:00^^enc1^^No^^C1;&FilterPlus:0;"

	loginCalled := false
	smsCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Login endpoint
		if r.URL.String() == "/cgi/WebCGI?1000" && r.Method == "POST" {
			loginCalled = true
			// Verify form data
			if err := r.ParseForm(); err != nil {
				t.Errorf("failed to parse login form: %v", err)
			}
			if r.FormValue("username") != "testuser" {
				t.Errorf("expected username 'testuser', got %q", r.FormValue("username"))
			}
			if r.FormValue("secret") != shiftedPassword {
				t.Errorf("expected secret %q, got %q", shiftedPassword, r.FormValue("secret"))
			}

			// Set session cookie
			http.SetCookie(w, &http.Cookie{
				Name:  "sessionid",
				Value: "test-session-abc",
			})

			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><input id="MyPBX_COMM" value="&amp;Gsmport:1;"></body></html>`)
			return
		}

		// SMS inbox endpoint
		if r.URL.String() == "/cgi/WebCGI?15200" {
			smsCalled = true
			w.Header().Set("Content-Type", "text/html")
			htmlValue := strings.ReplaceAll(mypbxCommValue, "&", "&amp;")
			fmt.Fprintf(w, `<html><body><input id="MyPBX_COMM" value="%s"></body></html>`, htmlValue)
			return
		}

		http.Error(w, "not found", 404)
	}))
	defer server.Close()

	client := NewWebUIClient(server.URL, "testuser", "testpass")
	session, err := client.Login()
	if err != nil {
		t.Fatalf("Login failed: %v", err)
	}

	comm, err := session.GetSMSInbox(1, 25)
	if err != nil {
		t.Fatalf("GetSMSInbox failed: %v", err)
	}

	if !loginCalled {
		t.Error("login endpoint was not called")
	}
	if !smsCalled {
		t.Error("SMS inbox endpoint was not called")
	}
	if len(comm.SMSRecvList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSRecvList))
	}
	if comm.SMSRecvList[0].Sender != "+00000000000" {
		t.Errorf("expected Sender %q, got %q", "+00000000000", comm.SMSRecvList[0].Sender)
	}
}

// --- DecodedContent on WebSMSRecord ---

func TestWebSMSRecord_DecodedContent(t *testing.T) {
	// "−15% до 16.03" encoded
	record := WebSMSRecord{
		ID:      "1",
		Port:    1,
		Sender:  "+00000000000",
		Content: "99w16qkUOVWnKPE22N6iOV%5BwOFO%3F",
	}
	decoded, err := record.DecodedContent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "−15% до 16.03"
	if decoded != expected {
		t.Errorf("expected %q, got %q", expected, decoded)
	}
}

func TestWebSMSRecord_DecodedContent_empty(t *testing.T) {
	record := WebSMSRecord{
		ID:      "1",
		Port:    1,
		Content: "",
	}
	decoded, err := record.DecodedContent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded != "" {
		t.Errorf("expected empty string, got %q", decoded)
	}
}

// --- WebUIClient factory ---

func TestNewWebUIClient(t *testing.T) {
	client := NewWebUIClient("127.0.0.1", "testuser", "testpass")
	if client.Addr != "127.0.0.1" {
		t.Errorf("expected Addr %q, got %q", "127.0.0.1", client.Addr)
	}
	if client.Username != "testuser" {
		t.Errorf("expected Username %q, got %q", "testuser", client.Username)
	}
	if client.Password != "testpass" {
		t.Errorf("expected Password %q, got %q", "testpass", client.Password)
	}
}

// --- End-to-end encoding pipeline test ---

func TestEncodingPipeline_loginPassword(t *testing.T) {
	// Test the full pipeline: password → md5 → base64 → shift +2
	password := "testpass"

	// Step 1: MD5 hex hash
	md5Hash := md5HashHex(password)
	expectedMD5 := "179ad45c6ce2cb97cf1029e212046e81"
	if md5Hash != expectedMD5 {
		t.Errorf("md5HashHex(%q) = %q, want %q", password, md5Hash, expectedMD5)
	}

	// Step 2: Base64 encode, shift +2
	shifted := EncodeYeastarBase64Shift(md5Hash)

	// Step 3: Shift -2, Base64 decode should give us back the MD5 hex hash
	roundTrip, err := DecodeYeastarBase64Shift(shifted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if roundTrip != md5Hash {
		t.Errorf("round-trip failed: expected %q, got %q", md5Hash, roundTrip)
	}

	// Step 4: Verify cookie string contains the shifted password
	cookie := BuildCookieString("testuser", shifted)
	if !strings.Contains(cookie, "loginname=testuser") {
		t.Error("cookie should contain loginname=testuser")
	}
	if !strings.Contains(cookie, "password=") {
		t.Error("cookie should contain password=")
	}
}
