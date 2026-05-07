package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/gebv/yeastar-tg-sms/api"
)

func main() {
	addr := flag.String("addr", "", "Yeastar gateway address (e.g., \"192.168.1.1\")")
	username := flag.String("user", "", "Web UI username")
	password := flag.String("pass", "", "Web UI password")
	port := flag.Int("port", 0, "GSM port filter (0 = all ports, 1-indexed)")
	lastPage := flag.Bool("last", false, "Fetch only the last page of SMS (default: fetch all pages)")
	outbox := flag.Bool("sent", false, "Fetch sent messages instead of inbox")
	flag.Parse()

	if *addr == "" || *username == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "Error: -addr, -user, and -pass are required")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  yeastar-tg-sms -addr <host> -user <login> -pass <password> [flags]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	client := api.NewWebUIClient(*addr, *username, *password)

	session, err := client.Login()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: login failed: %v\n", err)
		os.Exit(1)
	}

	var records []api.WebSMSRecord

	if *lastPage {
		// Fetch only the first (most recent) page
		var comm *api.MyPBXComm
		if *outbox {
			comm, err = session.GetSMSSentBox(1, 25)
		} else {
			comm, err = session.GetSMSInbox(1, 25)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to fetch SMS: %v\n", err)
			os.Exit(1)
		}
		records = filterByPort(comm.SMSRecvList, *port)
		if *outbox {
			records = filterByPort(comm.SMSSendList, *port)
		}
	} else {
		// Fetch all pages
		if *outbox {
			records, err = session.GetAllSMSSentBox()
		} else {
			records, err = session.GetAllSMSInbox()
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: failed to fetch SMS: %v\n", err)
			os.Exit(1)
		}
		if *port > 0 {
			records = filterByPort(records, *port)
		}
	}

	output := make([]smsOutput, 0, len(records))
	for _, r := range records {
		content, decodeErr := r.DecodedContent()
		if decodeErr != nil {
			content = fmt.Sprintf("[decode error: %v]", decodeErr)
		}

		entry := smsOutput{
			ID:          r.ID,
			Port:        r.Port,
			Sender:      r.Sender,
			DateTime:    r.DateTime,
			Content:     content,
			ContentRaw:  r.Content,
			Read:        r.Read,
			ContactName: r.ContactName,
		}

		output = append(output, entry)
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to encode JSON: %v\n", err)
		os.Exit(1)
	}
}

type smsOutput struct {
	ID          string `json:"id"`
	Port        int    `json:"port"`
	Sender      string `json:"sender"`
	DateTime    string `json:"datetime"`
	Content     string `json:"content"`
	ContentRaw  string `json:"content_raw"`
	Read        string `json:"read"`
	ContactName string `json:"contact_name"`
}

func filterByPort(records []api.WebSMSRecord, port int) []api.WebSMSRecord {
	if port <= 0 {
		return records
	}
	var filtered []api.WebSMSRecord
	for _, r := range records {
		if r.Port == port {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
