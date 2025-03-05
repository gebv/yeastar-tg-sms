package api

import (
	"context"
	"testing"
	"time"
)

func Test_Playground(t *testing.T) {
	c, err := New(context.TODO(), "192.168.1.32:5038")
	if err != nil {
		t.Fatal(err)
	}

	c.Login("apiuser", "apipass")
	time.Sleep(time.Second * 1)
	// c.DiscoverSIMCards()
	// time.Sleep(time.Second * 1)
	// c.SIMInfo(1)

	// c.UUSD(1, "*100#")
	// c.SendSMS(1, "+79xxxxxxxxx", "Сообщение кириллицой")

	time.Sleep(time.Second * 120)
}
