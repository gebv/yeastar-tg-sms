package api

import (
	"context"
	"testing"
	"time"
)

func Test_Playground(t *testing.T) {
	t.Skip("playground test — requires real gateway connection")

	cfg := Config{
		Addr:     "GATEWAY_IP:5038",
		Username: "GATEWAY_USER",
		Password: "GATEWAY_PASS",
	}

	c, err := New(context.TODO(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// c.DiscoverSIMCards()
	// c.SIMInfo(1)
	// c.SendSMS(1, "+00000000000", "Test message")

	time.Sleep(time.Second * 10)
}
