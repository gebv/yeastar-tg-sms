package api

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// WebSMSRecord represents a single SMS record from the Yeastar web UI
// MyPBX_COMM hidden input field.
//
// Format: ID^^Port^^Sender^^DateTime^^EncodedContent^^Read^^ContactName;
// Records are separated by ";"
// Fields are separated by "^^"
type WebSMSRecord struct {
	ID          string // Unique message ID (e.g., "10395585831777946715")
	Port        int    // GSM port (1-indexed, corresponds to GsmSpan)
	Sender      string // Sender name/number (may have @ suffix, e.g., "MegaFon@")
	DateTime    string // Reception time (format: "2026-05-05 09:04:58")
	Content     string // Encoded content (Yeastar's modified Base64 with +2 shift)
	Read        string // "Yes" or "No"
	ContactName string // Contact name (may have @ suffix)
}

// DecodedContent decodes the Yeastar obfuscated SMS content.
// The content uses a modified Base64 encoding with a +2 character shift.
//
// Algorithm (discovered from /js/base64.js):
// 1. Shift each character by -2 (undo the +2 shift applied during encoding)
// 2. Remove characters not in the Base64 alphabet (A-Za-z0-9+/=)
// 3. Base64 decode
// 4. UTF-8 decode
// 5. Strip UTF-8 BOM prefix (\xEF\xBB\xBF or \ufeff) if present
func (r *WebSMSRecord) DecodedContent() (string, error) {
	return DecodeYeastarSMSContent(r.Content)
}

// DecodeYeastarSMSContent decodes SMS text from the Yeastar web UI.
// The content uses a modified Base64 encoding with a +2 character shift.
//
// Example encoded: "99w16qkUOVWnKPE22N6iOV[wOFO?"
// Decoded result: "−15% до 16.03"
//
// The "99w1" prefix is the shifted UTF-8 BOM (base64 of \xEF\xBB\xBF = "77u/", shifted +2 = "99w1").
func DecodeYeastarSMSContent(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}

	// Step 1: URL-decode the content first (web UI uses %XX encoding)
	urlDecoded, err := url.QueryUnescape(encoded)
	if err != nil {
		// If URL decode fails, try with the raw content
		urlDecoded = encoded
	}

	// Step 2: Shift each character by -2 (undo the +2 shift from base64.js)
	shifted := make([]byte, 0, len(urlDecoded))
	for _, r := range urlDecoded {
		shifted = append(shifted, byte(r-2))
	}

	// Step 3: Remove characters not in the Base64 alphabet
	filtered := make([]byte, 0, len(shifted))
	for _, b := range shifted {
		if (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') ||
			(b >= '0' && b <= '9') || b == '+' || b == '/' || b == '=' {
			filtered = append(filtered, b)
		}
	}

	// Step 4: Base64 decode
	decoded, err := base64.StdEncoding.DecodeString(string(filtered))
	if err != nil {
		return "", fmt.Errorf("failed to base64 decode SMS content: %w", err)
	}

	// Step 5: Interpret as UTF-8 and strip BOM
	result := string(decoded)
	result = strings.TrimPrefix(result, "\ufeff")       // UTF-8 BOM as Go string
	result = strings.TrimPrefix(result, "\xEF\xBB\xBF") // UTF-8 BOM as bytes

	return result, nil
}

// ParseMyPBXComm parses the value of the MyPBX_COMM hidden input from the Yeastar web UI.
// The value contains multiple sections separated by "&SectionName:...;".
//
// Example: "&Gsmport:1,2;&SearchInfos:,,,,;&Display:141,1,25;&SMSRecvList:...;&FilterPlus:0;"
func ParseMyPBXComm(value string) (*MyPBXComm, error) {
	result := &MyPBXComm{}

	// Unescape HTML entities
	value = strings.ReplaceAll(value, "&amp;", "&")

	// Find SMSRecvList section
	smsSection := extractSection(value, "SMSRecvList:")
	if smsSection != "" {
		records, err := ParseSMSRecvList(smsSection)
		if err != nil {
			return nil, fmt.Errorf("failed to parse SMSRecvList: %w", err)
		}
		result.SMSRecvList = records
	}

	// Find SMS sent box section
	sentSection := extractSection(value, "SMSSendList:")
	if sentSection != "" {
		records, err := ParseSMSRecvList(sentSection) // Same format
		if err != nil {
			return nil, fmt.Errorf("failed to parse SMSSendList: %w", err)
		}
		result.SMSSendList = records
	}

	// Find GSM port info
	gsmPortSection := extractSection(value, "Gsmport:")
	if gsmPortSection != "" {
		result.GSMPorts = strings.Split(gsmPortSection, ",")
	}

	// Find Display info for pagination
	// Yeastar Display format: totalRecords,startIndex,viewCount
	// Example: Display:141,1,25 means 141 total records, showing from index 1, 25 per page
	displaySection := extractSection(value, "Display:")
	if displaySection != "" {
		parts := strings.Split(displaySection, ",")
		if len(parts) >= 3 {
			result.DisplayTotal, _ = strconv.Atoi(parts[0])
			result.DisplayStart, _ = strconv.Atoi(parts[1])
			result.DisplayViewCount, _ = strconv.Atoi(parts[2])
		}
	}

	return result, nil
}

// extractSection extracts the content between "SectionName:" and the next "&" or end of string.
func extractSection(value, sectionName string) string {
	idx := strings.Index(value, sectionName)
	if idx == -1 {
		return ""
	}
	content := value[idx+len(sectionName):]
	endIdx := strings.Index(content, ";&")
	if endIdx == -1 {
		// Check for "&" at end
		endIdx = strings.Index(content, "&")
		if endIdx == -1 {
			return content
		}
	}
	return content[:endIdx]
}

// ParseSMSRecvList parses the SMSRecvList section from MyPBX_COMM.
// Format: ID^^Port^^Sender^^DateTime^^EncodedContent^^Read^^ContactName;
// Records are separated by ";"
func ParseSMSRecvList(list string) ([]WebSMSRecord, error) {
	if list == "" {
		return nil, nil
	}

	var records []WebSMSRecord

	// Split by ";" to get individual records
	// Note: ";" inside encoded content should not be an issue because
	// the encoded content uses "%3B" for literal semicolons after URL decoding
	rawRecords := strings.Split(list, ";")

	for _, raw := range rawRecords {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		fields := strings.Split(raw, "^^")
		if len(fields) < 7 {
			continue // Not enough fields, skip malformed records
		}

		port, _ := strconv.Atoi(fields[1])

		record := WebSMSRecord{
			ID:          fields[0],
			Port:        port,
			Sender:      strings.TrimSuffix(fields[2], "@"),
			DateTime:    fields[3],
			Content:     fields[4],
			Read:        fields[5],
			ContactName: strings.TrimSuffix(fields[6], "@"),
		}

		records = append(records, record)
	}

	return records, nil
}

// MyPBXComm represents the parsed data from the MyPBX_COMM hidden input.
type MyPBXComm struct {
	SMSRecvList      []WebSMSRecord // Received SMS messages
	SMSSendList      []WebSMSRecord // Sent SMS messages
	GSMPorts         []string       // Available GSM port numbers
	DisplayTotal     int            // Total number of items (first field of Display)
	DisplayStart     int            // Starting index for this page (1-based)
	DisplayViewCount int            // Number of items per page (third field of Display)
}

// TotalPages returns the total number of pages based on DisplayTotal and DisplayPageSize.
func (m *MyPBXComm) TotalPages() int {
	if m.DisplayViewCount <= 0 {
		return 0
	}
	return (m.DisplayTotal + m.DisplayViewCount - 1) / m.DisplayViewCount
}

// WebUIClient handles authentication and data retrieval from the Yeastar web UI.
// It uses the modified Base64 with +2 shift for password encoding (see base64.js).
type WebUIClient struct {
	Addr     string // Gateway address (e.g., "192.168.1.1")
	Username string // Web UI username
	Password string // Web UI password
	Product  string // Gateway product model (e.g., "TG100"). Used in the Product cookie for authentication.
}

// NewWebUIClient creates a new Web UI client with the given credentials.
func NewWebUIClient(addr, username, password string) *WebUIClient {
	return &WebUIClient{
		Addr:     addr,
		Username: username,
		Password: password,
		Product:  "TG100", // default product model
	}
}

// EncodePasswordForYeastar encodes a password for Yeastar web UI authentication.
// The algorithm (from /js/base64.js):
// 1. Compute MD5 hash of the password
// 2. Base64 encode the hex MD5 hash
// 3. Shift each character by +2
func EncodePasswordForYeastar(password string) string {
	// Note: MD5 hash is computed externally since Go's crypto/md5
	// returns raw bytes, not hex. The caller should provide the hex MD5.
	// This function does steps 2-3: base64(hexMD5) + shift +2
	panic("use EncodeYeastarBase64Shift instead, providing the hex MD5 hash")
}

// EncodeYeastarBase64Shift performs the modified Base64 encoding used by Yeastar.
// It takes the hex MD5 hash of the password, base64 encodes it,
// then shifts each character by +2 (as defined in /js/base64.js).
func EncodeYeastarBase64Shift(hexMD5Hash string) string {
	// Step 1: Base64 encode the hex MD5 hash
	rawB64 := base64.StdEncoding.EncodeToString([]byte(hexMD5Hash))

	// Step 2: Shift each character by +2
	shifted := make([]byte, 0, len(rawB64))
	for _, b := range []byte(rawB64) {
		shifted = append(shifted, b+2)
	}

	return string(shifted)
}

// DecodeYeastarBase64Shift performs the reverse of EncodeYeastarBase64Shift.
// It shifts each character by -2, then base64 decodes, returning the hex MD5 hash.
func DecodeYeastarBase64Shift(shifted string) (string, error) {
	// Step 1: Shift each character by -2
	unshifted := make([]byte, 0, len(shifted))
	for _, b := range []byte(shifted) {
		unshifted = append(unshifted, b-2)
	}

	// Step 2: Base64 decode
	decoded, err := base64.StdEncoding.DecodeString(string(unshifted))
	if err != nil {
		return "", fmt.Errorf("failed to base64 decode: %w", err)
	}

	return string(decoded), nil
}

// BuildCookieString builds the Cookie header value for Yeastar web UI requests.
// It creates the authentication cookies in the format expected by the Yeastar CGI.
func BuildCookieString(username, shiftedBase64MD5 string) string {
	return fmt.Sprintf(
		"language=en; loginname=%s; Series=; Product=TG100; current=%s; password=%s; OsVer=xxxx; defaultpwd=; curUrl=; TabIndex=0; TabIndexwithback=0",
		username,
		username,
		url.QueryEscape(shiftedBase64MD5),
	)
}

// SMSRecvListRegex is a compiled regex for extracting SMSRecvList from MyPBX_COMM value.
var SMSRecvListRegex = regexp.MustCompile(`SMSRecvList:([^;&]+)`)

// ParseSMSRecvListFromHTML extracts the SMSRecvList from the full MyPBX_COMM value
// found in the HTML hidden input.
func ParseSMSRecvListFromHTML(mypbxCommValue string) ([]WebSMSRecord, error) {
	// Unescape HTML entities
	mypbxCommValue = strings.ReplaceAll(mypbxCommValue, "&amp;", "&")

	match := SMSRecvListRegex.FindStringSubmatch(mypbxCommValue)
	if match == nil || len(match) < 2 {
		return nil, nil
	}

	return ParseSMSRecvList(match[1])
}
