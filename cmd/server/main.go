package main

// Copyright (c) 2018 Bhojpur Consulting Private Limited, India. All rights reserved.

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:

// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.

// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/julienschmidt/httprouter"
	"github.com/unrolled/render"

	engine "github.com/bhojpur/risk/pkg/engine"
)

const (
	// Time allowed to write a msg to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong msg from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum msg size allowed from peer.
	// maxMessageSize = 512
)

var addr = flag.String("addr", "0.0.0.0:9113", "HTTP service address")
var server = flag.String("server", "ws://localhost:9111/", "Bhojpur Trade server address")
var username = flag.String("username", "admin", "username to login to Bhojpur Trade server")
var passwd = flag.String("passwd", "test", "passwd to login to Bhojpur Trade server")
var rd = render.New()
var clients = sync.Map{}
var clientCounter int64 = 0

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func index(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	rd.JSON(w, http.StatusOK, map[string]interface{}{"hello": "index page"})
}

func api(w http.ResponseWriter, r *http.Request, p httprouter.Params) {
	fmt.Fprintf(w, "api: %s\n", p.ByName("name"))
}

func publish2Client(ch chan []byte, c *websocket.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		log.Println("publish2Client ended")
		ticker.Stop()
		c.Close()
	}()
	for {
		select {
		case msg := <-ch:
			c.SetWriteDeadline(time.Now().Add(writeWait))
			err := c.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				log.Println("publish2Client write error:", err)
				return
			}
		case <-ticker.C:
			c.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				log.Print(err)
				return
			}
		}
	}
}

type Client struct {
	Ch     chan []byte
	UserId int
	Conn   *websocket.Conn
}

func serveClient(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddInt64(&clientCounter, 1)
	self := &Client{}
	clients.Store(n, self)

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	log.Println("received client connection", n)
	ch := make(chan []byte)
	self.Ch = ch
	self.Conn = c
	defer func() {
		log.Println("client connection", n, "closed")
		clients.Delete(n)
		close(ch)
		c.Close()
	}()
	// c.SetReadLimit(maxMessageSize)
	c.SetReadDeadline(time.Now().Add(pongWait))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	go publish2Client(ch, c)
	for {
		// mt is an int with value
		// websocket.BinaryMessage or websocket.TextMessage
		mt, raw, err := c.ReadMessage()
		if err != nil {
			log.Println("read error:", err)
			break
		}
		var msg []interface{}
		err = json.Unmarshal(raw, &msg)
		if err != nil {
			log.Println("received non-json msg", msg, mt)
			continue
		}
		action, _ := msg[0].(string)
		if action == "login" {
			msg[0] = "validate_user"
		} else if action == "saveRiskFile" {
			fn, _ := msg[1].(string)
			content, _ := msg[2].(string)
			if path.Ext(fn) == ".py" {
				tmp := "." + strconv.Itoa(int(n)) + "tmp_" + fn
				err = ioutil.WriteFile(tmp, []byte(content), 0755)
				if err != nil {
					str, _ := json.Marshal([]interface{}{"saveRiskFile", fn, err.Error()})
					ch <- str
					continue
				}
				err = engine.CheckPy(tmp)
				os.Remove(tmp)
				if err != nil {
					str, _ := json.Marshal([]interface{}{"saveRiskFile", fn, err.Error()})
					ch <- str
					continue
				}
			} else if path.Ext(fn) == ".ini" {
				cfg, err := engine.ParseIni(content)
				if err != nil {
					str, _ := json.Marshal([]interface{}{"saveRiskFile", fn, err.Error()})
					ch <- str
					continue
				}
				_, err = engine.ParsePortfolio(cfg, engine.GetPath(self.UserId))
				if err != nil {
					str, _ := json.Marshal([]interface{}{"saveRiskFile", fn, err.Error()})
					ch <- str
					continue
				}
			}
		}
		msg = append(msg, n)
		engine.Request(msg)
	}
}

func tradeServerJob(ch chan []interface{}, c *websocket.Conn) {
	c.SetReadDeadline(time.Now().Add(pongWait))
	c.SetPongHandler(func(string) error { c.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	riskTicker := time.NewTicker(time.Second)
	pingTicker := time.NewTicker(pingPeriod)
	defer func() {
		log.Println("tradeServerJob ended")
		riskTicker.Stop()
		pingTicker.Stop()
	}()
	for {
		select {
		case msg, _ := <-engine.ChannelWriteTradeServer:
			action, _ := msg[0].(string)
			if action == "riskFile" || action == "saveRiskFile" || action == "deleteRiskFile" || action == "historicalRisk" {
				n, _ := msg[len(msg)-1].(int64)
				tmp, _ := clients.Load(n)
				if tmp != nil {
					client := tmp.(*Client)
					out := []interface{}{action}
					if action == "historicalRisk" {
						portfolios := engine.UserPortfolios[client.UserId]
						if portfolios == nil {
							continue
						}
						portfolioName, _ := msg[1].(string)
						portfolio := portfolios[portfolioName]
						if portfolio == nil {
							continue
						}
						riskName, _ := msg[2].(string)
						paramName, _ := msg[3].(string)
						out = append(out, portfolioName)
						out = append(out, riskName)
						out = append(out, paramName)
						for _, r := range portfolio.RiskDefs {
							if riskName == r.DisplayName {
								for _, rp := range r.Params {
									if rp.Name == paramName {
										if rp.Graph {
											out = append(out, rp.History)
										}
										break
									}
								}
								break
							}
						}
					} else {
						fn, _ := msg[1].(string)
						out = append(out, fn)
						if action == "riskFile" {
							content, err := engine.GetFile(client.UserId, fn)
							if err == nil {
								out = append(out, string(content))
							} else {
								out = append(out, nil)
								out = append(out, err.Error())
							}
						} else if action == "deleteRiskFile" {
							err := engine.DeleteFile(client.UserId, fn)
							if err != nil {
								out = append(out, err.Error)
							}
						} else if action == "saveRiskFile" {
							err := engine.SaveFile(client.UserId, fn, msg[2].(string))
							if err != nil {
								out = append(out, err.Error)
							}
						}
					}
					str, _ := json.Marshal(out)
					client.Ch <- str
				}
			} else {
				c.SetWriteDeadline(time.Now().Add(writeWait))
				str, _ := json.Marshal(msg)
				err := c.WriteMessage(websocket.TextMessage, str)
				if err != nil {
					log.Fatal("trade server: ", err)
				}
			}
		case msg, ok := <-ch:
			if !ok {
				log.Print("trader server chan closed")
				return
			}
			action := msg[0].(string)
			if action == "connection" {
				status := msg[1].(string)
				if status != "ok" {
					log.Printf("admin failed to login: %s", msg)
					// Cleanly close the connection by sending a close msg and then
					// waiting (with timeout) for the server to close the connection.
					c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
					log.Fatal("exit")
				} else {
					params := msg[2].(map[string]interface{})
					userId := int(params["userId"].(float64))
					log.Printf("admin login success: %d", userId)
					engine.Request(engine.Array{"securities"})
				}
			} else if action == "security" {
				engine.ParseSecurity(msg)
			} else if action == "securities" {
				log.Printf("%s", msg)
				engine.Request(engine.Array{"bod"})
				engine.Request(engine.Array{"target"})
				engine.Request(engine.Array{"offline", 0})
				engine.Request(engine.Array{"pnl"})
			} else if action == "bod" {
				engine.ParseBod(msg)
			} else if action == "Pnl" {
				// pass
			} else if action == "pnl" {
				engine.ParsePnl(msg)
			} else if action == "offline" {
				engine.ParseOffline(msg)
			} else if action == "Order" {
				engine.ParseOrder(msg, false)
			} else if action == "order" {
				engine.ParseOrder(msg, true)
			} else if action == "md" {
				engine.ParseMd(msg)
			} else if action == "user_validation" {
				userId := int(msg[1].(float64))
				token := int64(msg[2].(float64))
				tmp, _ := clients.Load(token)
				if tmp == nil {
					continue
				}
				client := tmp.(*Client)
				if userId > 0 {
					client.UserId = userId
					log.Println("client", int(token), ":", userId)
					if out, err := json.Marshal([]interface{}{"riskFiles", engine.GetFiles(userId)}); err == nil {
						client.Ch <- out
					}
				} else {
					client.Conn.Close()
				}
			} else if action == "sub_account" {
				// pass
			} else if action == "broker_account" {
				// pass
			} else if action == "algo_def" {
				// pass
			} else if action == "market" {
				// pass
			} else if action == "target" {
				engine.ParseTarget(msg)
			} else if action == "user_sub_account" {
				engine.ParseUserIdAcc(msg)
			} else {
				log.Printf("%s", msg)
			}
		case <-pingTicker.C:
			c.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				log.Print(err)
				return
			}
		case <-riskTicker.C:
			rpts := engine.RunUserPortfolios()
			clients.Range(func(_, c interface{}) bool {
				client := c.(*Client)
				rpt := rpts[client.UserId]
				out, err := json.Marshal([]interface{}{"risk", rpt})
				if err != nil {
					log.Println("failed to Marshal:", rpt)
					return true
				}
				client.Ch <- out
				return true
			})
		}
	}
}

func tradeServer() {
	log.Printf("connecting to Bhojpur Trade server: %s", *server)
	c, _, err := websocket.DefaultDialer.Dial(*server, nil)
	if err != nil {
		log.Fatal("dial trade server: ", err)
	}
	ch := make(chan []interface{})
	defer func() {
		c.Close()
		close(ch)
	}()
	go tradeServerJob(ch, c)

	engine.Request(engine.Array{
		"login",
		*username,
		*passwd,
		true,
	})

	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			log.Fatal("trade server: ", err)
		}
		var msg []interface{}
		err = json.Unmarshal(raw, &msg)
		if err != nil {
			log.Printf("received non-json msg from trade server: %s", raw)
			continue
		}
		ch <- msg
	}
}

func main() {
	log.Print("Bhojpur Risk server engine")
	log.Print("Copyright (c) 2018 by Bhojpur Consulting Private Limited, India.")
	log.Print("All rights reserved.")

	flag.Parse()
	engine.InitPy()
	router := httprouter.New()
	router.GET("/", index)
	router.GET("/risk/", func(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
		serveClient(w, r)
	})
	router.GET("/api/:name", api)
	log.Print("risk server listening on ", *addr)
	go tradeServer()
	log.Fatal(http.ListenAndServe(*addr, router))
}
