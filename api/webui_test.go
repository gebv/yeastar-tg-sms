package api

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestDecodeYeastarSMSContent_short verifies the modified Base64 +2 shift decoding
// with a short encoded message.
func TestDecodeYeastarSMSContent_short(t *testing.T) {
	// "−15% до 16.03"
	encoded := "99w16qkUOVWnKPE22N6iOV%5BwOFO%3F"
	decoded, err := DecodeYeastarSMSContent(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "−15% до 16.03"
	if decoded != expected {
		t.Errorf("expected %q, got %q", expected, decoded)
	}
}

// TestDecodeYeastarSMSContent_medium verifies decoding of a medium-length message.
func TestDecodeYeastarSMSContent_medium(t *testing.T) {
	// "Курьер у дома, заказ скоро будет у вас"
	encoded := "99w12LtTi%3BIC2%5B%7CSvfICKPIFKPE22N9SxPEyNEFSv%3BEy2NtSuPE5KPID2NtSxvIC2N6i2NJTi%3BE22NZTikFTi%7BFSuvEy2%5BG%3F"
	decoded, err := DecodeYeastarSMSContent(encoded)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "Курьер у дома, заказ скоро будет у вас"
	if decoded != expected {
		t.Errorf("expected %q, got %q", expected, decoded)
	}
}

// TestDecodeYeastarSMSContent_empty verifies empty input returns empty string.
func TestDecodeYeastarSMSContent_empty(t *testing.T) {
	decoded, err := DecodeYeastarSMSContent("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded != "" {
		t.Errorf("expected empty string, got %q", decoded)
	}
}

// TestDecodeYeastarSMSContent_bomPrefix verifies that the shifted BOM prefix
// "99w1" (which decodes to "77u/" = base64 of \xEF\xBB\xBF) is properly
// stripped after decoding.
func TestDecodeYeastarSMSContent_bomPrefix(t *testing.T) {
	// Build a message with BOM: "77u/" shifted +2 = "99w1", then append shifted base64
	// Original text: "тест" in UTF-8 with BOM
	utf8WithBOM := "\ufeffтест"
	b64 := base64.StdEncoding.EncodeToString([]byte(utf8WithBOM))
	shifted := shiftString(b64, 2)
	decoded, err := DecodeYeastarSMSContent(shifted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded != "тест" {
		t.Errorf("expected %q, got %q", "тест", decoded)
	}
}

// TestParseSMSRecvList verifies parsing of the ^^ separated SMS records.
func TestParseSMSRecvList(t *testing.T) {
	input := "12345^^1^^SenderName^^2026-05-05 09:04:58^^99w1testcontent^^No^^ContactName"
	records, err := ParseSMSRecvList(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	if r.ID != "12345" {
		t.Errorf("expected ID %q, got %q", "12345", r.ID)
	}
	if r.Port != 1 {
		t.Errorf("expected Port 1, got %d", r.Port)
	}
	if r.Sender != "SenderName" {
		t.Errorf("expected Sender %q, got %q", "SenderName", r.Sender)
	}
	if r.DateTime != "2026-05-05 09:04:58" {
		t.Errorf("expected DateTime %q, got %q", "2026-05-05 09:04:58", r.DateTime)
	}
	if r.Content != "99w1testcontent" {
		t.Errorf("expected Content %q, got %q", "99w1testcontent", r.Content)
	}
	if r.Read != "No" {
		t.Errorf("expected Read %q, got %q", "No", r.Read)
	}
	if r.ContactName != "ContactName" {
		t.Errorf("expected ContactName %q, got %q", "ContactName", r.ContactName)
	}
}

// TestParseSMSRecvList_multiple verifies parsing of multiple records.
func TestParseSMSRecvList_multiple(t *testing.T) {
	input := "id1^^1^^Sender1^^2026-01-01 00:00:00^^content1^^Yes^^Contact1;id2^^2^^Sender2^^2026-01-02 00:00:00^^content2^^No^^Contact2"
	records, err := ParseSMSRecvList(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].ID != "id1" {
		t.Errorf("record 0: expected ID %q, got %q", "id1", records[0].ID)
	}
	if records[1].ID != "id2" {
		t.Errorf("record 1: expected ID %q, got %q", "id2", records[1].ID)
	}
	if records[0].Read != "Yes" {
		t.Errorf("record 0: expected Read %q, got %q", "Yes", records[0].Read)
	}
	if records[1].Read != "No" {
		t.Errorf("record 1: expected Read %q, got %q", "No", records[1].Read)
	}
}

// TestParseSMSRecvList_senderWithAt verifies that @ suffix is stripped from Sender.
func TestParseSMSRecvList_senderWithAt(t *testing.T) {
	input := "id1^^1^^SomeName@^^2026-01-01 00:00:00^^content^^No^^SomeName@"
	records, err := ParseSMSRecvList(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records[0].Sender != "SomeName" {
		t.Errorf("expected Sender %q, got %q", "SomeName", records[0].Sender)
	}
	if records[0].ContactName != "SomeName" {
		t.Errorf("expected ContactName %q, got %q", "SomeName", records[0].ContactName)
	}
}

// TestParseMyPBXComm verifies parsing of the full MyPBX_COMM value.
func TestParseMyPBXComm(t *testing.T) {
	// Display format: totalRecords,startIndex,viewCount
	// Example: Display:100,1,25 means 100 total, starting at index 1, 25 per page
	input := "&Gsmport:1,2;&SearchInfos:,,,,;&Display:100,1,25;&SMSRecvList:id1^^1^^Sender1^^2026-01-01 00:00:00^^content1^^No^^Contact1;&FilterPlus:0;"
	comm, err := ParseMyPBXComm(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comm.GSMPorts) != 2 {
		t.Errorf("expected 2 GSM ports, got %d", len(comm.GSMPorts))
	}
	if comm.DisplayTotal != 100 {
		t.Errorf("expected DisplayTotal 100, got %d", comm.DisplayTotal)
	}
	if comm.DisplayStart != 1 {
		t.Errorf("expected DisplayStart 1, got %d", comm.DisplayStart)
	}
	if comm.DisplayViewCount != 25 {
		t.Errorf("expected DisplayViewCount 25, got %d", comm.DisplayViewCount)
	}
	if len(comm.SMSRecvList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSRecvList))
	}
}

// TestParseMyPBXComm_htmlEntities verifies that &amp; entities are unescaped.
func TestParseMyPBXComm_htmlEntities(t *testing.T) {
	input := "&amp;Gsmport:1;&amp;SMSRecvList:id1^^1^^S^^2026-01-01^^c^^No^^C;&amp;FilterPlus:0;"
	comm, err := ParseMyPBXComm(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(comm.SMSRecvList) != 1 {
		t.Fatalf("expected 1 SMS record, got %d", len(comm.SMSRecvList))
	}
}

// TestMyPBXComm_TotalPages verifies pagination calculation.
func TestMyPBXComm_TotalPages(t *testing.T) {
	tests := []struct {
		total, viewCount, expected int
	}{
		{100, 25, 4},
		{100, 10, 10},
		{101, 25, 5},
		{141, 25, 6},
		{0, 25, 0},
		{25, 0, 0},
	}
	for _, tt := range tests {
		comm := &MyPBXComm{DisplayTotal: tt.total, DisplayViewCount: tt.viewCount}
		if got := comm.TotalPages(); got != tt.expected {
			t.Errorf("TotalPages(%d, %d) = %d, want %d", tt.total, tt.viewCount, got, tt.expected)
		}
	}
}

// TestEncodeYeastarBase64Shift verifies the +2 shift encoding/decoding round-trip.
func TestEncodeYeastarBase64Shift(t *testing.T) {
	md5Hash := "135d4d40b768220ad92a2d90325202c9"
	shifted := EncodeYeastarBase64Shift(md5Hash)
	decoded, err := DecodeYeastarBase64Shift(shifted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decoded != md5Hash {
		t.Errorf("round-trip failed: expected %q, got %q", md5Hash, decoded)
	}
}

// TestDecodeYeastarBase64Shift_bom verifies the "99w1" prefix decodes to BOM.
func TestDecodeYeastarBase64Shift_bom(t *testing.T) {
	// "99w1" is the shifted form of "77u/" (base64 of UTF-8 BOM \xEF\xBB\xBF)
	// DecodeYeastarBase64Shift shifts -2 to get "77u/", then base64 decodes to get the BOM bytes
	shifted := "99w1"
	decoded, err := DecodeYeastarBase64Shift(shifted)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The decoded result should be the UTF-8 BOM as a string
	if decoded != "\ufeff" {
		t.Errorf("expected BOM \\ufeff, got %q", decoded)
	}
}

// TestBuildCookieString verifies the cookie string contains required fields.
func TestBuildCookieString(t *testing.T) {
	md5Hash := "135d4d40b768220ad92a2d90325202c9"
	shifted := EncodeYeastarBase64Shift(md5Hash)
	cookie := BuildCookieString("sms", shifted)
	if !strings.Contains(cookie, "loginname=sms") {
		t.Error("cookie should contain loginname=sms")
	}
	if !strings.Contains(cookie, "defaultpwd=") {
		t.Error("cookie should contain defaultpwd=")
	}
	if !strings.Contains(cookie, "language=en") {
		t.Error("cookie should contain language=en")
	}
	if !strings.Contains(cookie, "OsVer=xxxx") {
		t.Error("cookie should contain OsVer=xxxx")
	}
	if !strings.Contains(cookie, "password=") {
		t.Error("cookie should contain password=")
	}
	if !strings.Contains(cookie, "Product=TG100") {
		t.Error("cookie should contain Product=TG100")
	}
	if !strings.Contains(cookie, "current=sms") {
		t.Error("cookie should contain current=sms")
	}
	if !strings.Contains(cookie, "Series=") {
		t.Error("cookie should contain Series=")
	}
}

// TestSMSReceived_DecodeSMSContent verifies AMI-style URL-decoded SMS content.
func TestSMSReceived_DecodeSMSContent(t *testing.T) {
	sms := SMSReceived{
		ID:      "1",
		GsmSpan: "2",
		Sender:  "+00000000000",
		Content: "%EF%BB%BF%D0%95%D1%89%D1%91+%D0%BE%D1%82%D0%B2%D0%B5%D1%82+",
	}
	decoded, err := sms.DecodeSMSContent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "Ещё ответ "
	if decoded != expected {
		t.Errorf("expected %q, got %q", expected, decoded)
	}
}

// TestSMSReceived_IsComplete verifies single and multipart message detection.
func TestSMSReceived_IsComplete(t *testing.T) {
	tests := []struct {
		index, total int
		complete     bool
	}{
		{1, 1, true},  // single part
		{1, 2, false}, // first of two
		{2, 2, true},  // last of two
		{1, 3, false}, // first of three
		{2, 3, false}, // second of three
		{3, 3, true},  // last of three
	}
	for _, tt := range tests {
		sms := SMSReceived{Index: tt.index, Total: tt.total}
		if got := sms.IsComplete(); got != tt.complete {
			t.Errorf("IsComplete(%d/%d) = %v, want %v", tt.index, tt.total, got, tt.complete)
		}
	}
}

// TestMultipartCollector_singlePart verifies that a single-part message is
// returned immediately.
func TestMultipartCollector_singlePart(t *testing.T) {
	collector := NewMultipartCollector()
	sms := SMSReceived{
		ID:      "1",
		GsmSpan: "2",
		Sender:  "+00000000000",
		Content: "%EF%BB%BF%D0%A2%D0%B5%D1%81%D1%82",
		Index:   1,
		Total:   1,
	}
	result := collector.Add(sms)
	if result == nil {
		t.Fatal("expected assembled SMS, got nil")
	}
	if result.Sender != "+00000000000" {
		t.Errorf("expected Sender %q, got %q", "+00000000000", result.Sender)
	}
}

// TestMultipartCollector_twoParts verifies that a two-part multipart message
// is assembled when both parts are received.
func TestMultipartCollector_twoParts(t *testing.T) {
	collector := NewMultipartCollector()
	id := "mp-msg-1"

	// First part — should not be complete
	sms1 := SMSReceived{
		ID:      id,
		GsmSpan: "2",
		Sender:  "+00000000000",
		Content: "%D0%A7%D0%B0%D1%81%D1%82%D1%8C+1+", // "Часть 1 "
		Index:   1,
		Total:   2,
	}
	result := collector.Add(sms1)
	if result != nil {
		t.Error("first part should not complete the message")
	}

	// Second part — should complete the message
	sms2 := SMSReceived{
		ID:      id,
		GsmSpan: "2",
		Sender:  "+00000000000",
		Content: "%D0%A7%D0%B0%D1%81%D1%82%D1%8C+2+", // "Часть 2 "
		Index:   2,
		Total:   2,
	}
	result = collector.Add(sms2)
	if result == nil {
		t.Fatal("expected assembled SMS after second part, got nil")
	}
	if result.Sender != "+00000000000" {
		t.Errorf("expected Sender %q, got %q", "+00000000000", result.Sender)
	}
	// Content should be concatenated
	if !strings.Contains(result.Content, "Часть") {
		t.Errorf("expected content to contain 'Часть', got %q", result.Content)
	}
}

// shiftString shifts each character by the given amount.
func shiftString(s string, amount int) string {
	result := make([]byte, len(s))
	for i, b := range []byte(s) {
		result[i] = byte(int(b) + amount)
	}
	return string(result)
}
