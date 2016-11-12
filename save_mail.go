package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/mail"
	"strconv"
	"strings"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/jhillyerd/go.enmime"
)

type savePayload struct {
	client *Client
	server *SmtpdServer
}

var SaveMailChan chan *savePayload // workers for saving mail

type redisClient struct {
	count int
	conn  redis.Conn
	time  int
}

func saveMail() {
	var to, recipient string

	var redis_err error
	var length int
	redisClient := &redisClient{}
	//  receives values from the channel repeatedly until it is closed.
	for {
		payload := <-SaveMailChan
		if user, host, addr_err := validateEmailData(payload.client); addr_err != nil {
			payload.server.logln(1, fmt.Sprintf("mail_from didnt validate: %v", addr_err)+" client.mail_from:"+payload.client.mail_from)
			// notify client that a save completed, -1 = error
			payload.client.savedNotify <- -1
			continue
		} else {
			recipient = user + "@" + host
			to = user + "@" + mainConfig.Primary_host
		}
		length = len(payload.client.data)
		ts := strconv.FormatInt(time.Now().UnixNano(), 10)
		payload.client.subject = mimeHeaderDecode(payload.client.subject)
		payload.client.hash = md5hex(
			&to,
			&payload.client.mail_from,
			&payload.client.subject,
			&ts)
		// Add extra headers
		raw := ""
		raw += "Delivered-To: " + to + "\r\n"
		raw += "Received: from " + payload.client.helo + " (" + payload.client.helo + "  [" + payload.client.address + "])\r\n"
		raw += "	by " + payload.server.Config.Host_name + " with SMTP id " + payload.client.hash + "@" +
			payload.server.Config.Host_name + ";\r\n"
		raw += "	" + time.Now().Format(time.RFC1123Z) + "\r\n"
		raw += payload.client.data
		// compress to save space
		//payload.client.data = compress(&add_head, &payload.client.data)

		r := strings.NewReader(raw)
		msg, err := mail.ReadMessage(r)
		if err != nil {
			log.Fatal(err)
		}
		mime, _ := enmime.ParseMIMEBody(msg) // Parse message body with enmime

		redis_err = redisClient.redisConnection()
		if redis_err == nil {
			group := struct {
				To        string    `json:"to, omitempty"`
				From      string    `json:"from, omitempty"`
				Subject   string    `json:"subject, omitempty"`
				Text      string    `json:"text, omitempty"`
				Html      string    `json:"html, omitempty"`
				Raw       string    `json:"raw, omitempty"`
				Hash      string    `json:"hash, omitempty"`
				Recipient string    `json:"recipient, omitempty"`
				Address   string    `json:"address, omitempty"`
				MailFrom  string    `json:"mailFrom, omitempty"`
				TLS       bool      `json:"tls, omitempty"`
				Date      time.Time `json:"date, omitempty"`
			}{
				to,
				payload.client.mail_from,
				mime.GetHeader("Subject"),
				mime.Text,
				mime.HTML,
				raw,
				payload.client.hash,
				recipient,
				payload.client.address,
				payload.client.mail_from,
				payload.client.tls_on,
				time.Now(),
			}
			serialized, err := json.Marshal(&group)
			if err != nil {
				fmt.Println("error:", err)
			}

			_, doErr := redisClient.conn.Do("SETEX", to+":"+payload.client.hash, mainConfig.Redis_expire_seconds, string(serialized))

			if doErr == nil {
				payload.server.logln(0, "Email saved "+payload.client.hash+" len:"+strconv.Itoa(length))
				payload.client.savedNotify <- 1
			} else {
				payload.server.logln(1, fmt.Sprintf("redis: %v", doErr))
				payload.client.savedNotify <- -1
			}

		} else {
			payload.server.logln(1, fmt.Sprintf("redis: %v", redis_err))
			payload.client.savedNotify <- -1
		}
	}
}

func (c *redisClient) redisConnection() (err error) {

	if c.count == 0 {
		c.conn, err = redis.Dial("tcp", mainConfig.Redis_interface)
		if err != nil {
			// handle error
			return err
		}
	}
	return nil
}

// test database connection settings
func testDbConnections() (err error) {
	redisClient := &redisClient{}
	if redis_err := redisClient.redisConnection(); redis_err != nil {
		err = errors.New("Redis cannot connect, check your settings. " + redis_err.Error())
	}

	return
}
