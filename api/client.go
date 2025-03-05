package api

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"sync"
)

type Client struct {
	conn     net.Conn
	cmdChan  chan string
	incoming chan []string
	wg       sync.WaitGroup
}

func New(ctx context.Context, addr string) (*Client, error) {
	var dial net.Dialer
	conn, err := dial.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	c := &Client{
		conn:     conn,
		cmdChan:  make(chan string),
		incoming: make(chan []string),
	}

	c.wg.Add(3)
	go func() {
		defer c.wg.Done()
		for {
			select {
			case msg := <-c.incoming:
				log.Printf("[INCOMING] %q\n", msg)
			case <-ctx.Done():
				return
			}
		}
	}()
	go c.runReader()
	go c.runWriter()

	return c, nil
}

func (c *Client) Login(username, password string) {
	c.SendCommand(fmt.Sprintf("Action: Login\r\nUsername: %s\r\nSecret: %s\r\n\r\n", username, password))
}

func (c *Client) DiscoverSIMCards() {
	c.SendCommand("Action: smscommand\r\ncommand: gsm show spans\r\n\r\n")
}

func (c *Client) SIMInfo(port int) {
	c.SendCommand(fmt.Sprintf("Action: smscommand\r\ncommand: gsm show span %d\r\n\r\n", port+1))
}

func (c *Client) UUSD(port int, cmd string) {
	// port += 1
	// c.SendCommand(fmt.Sprintf("Action: smscommand\r\ncommand: gsm send ussd %d \"%s\"\r\n\r\n", port, "*100"+url.QueryEscape("#")))
	panic("not supported")
}

func (c *Client) SendSMS(port int, dst, msg string) {
	port += 1
	c.SendCommand(fmt.Sprintf("Action: smscommand\r\ncommand: gsm send sms %d %s \"%s\"\r\n\r\n", port, dst, url.QueryEscape(msg)))
}

func (c *Client) SendCommand(cmd string) {
	c.cmdChan <- cmd
}

func (c *Client) Close() {
	close(c.cmdChan)
	c.conn.Close()
	c.wg.Wait()
}

func (c *Client) runReader() {
	defer c.wg.Done()

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
		// if c.debug {
		log.Printf("[RAW] << %q\n", line)
		// }

		if line == "" {
			if len(buffer) > 0 {
				select {
				case c.incoming <- buffer:
					// case <-c.ctx.Done():
					// 	return
				}
				buffer = nil
			}
		} else {
			buffer = append(buffer, line)
		}
	}

	// if err := scanner.Err(); err != nil {
	// 	select {
	// 	case c.errors <- err:
	// 	default:
	// 	}
	// }

	if len(buffer) > 0 {
		select {
		case c.incoming <- buffer:
			// case <-c.ctx.Done():
		}
	}
}

func (c *Client) runWriter() {
	defer c.wg.Done()

	for cmd := range c.cmdChan {
		fmt.Printf("[RAW] >> %q\n", cmd)
		_, err := c.conn.Write([]byte(cmd))
		if err != nil {
			log.Printf("Write error: %v", err)
			return
		}
	}
}
