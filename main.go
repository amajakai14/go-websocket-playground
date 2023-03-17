package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	AddMenu int = iota
	RemoveMenu
	GetMenu
	ResetMenu
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Connection struct {
	ws   *websocket.Conn
	send chan []byte
}

type Subscription struct {
	conn *Connection
	room string
}

type Hub struct {
	rooms      map[string]map[*Connection]bool
	broadcast  chan Message
	register   chan Subscription
	unregister chan Subscription
}

type Message struct {
	data []byte
	room string
}

type CartMessage struct {
	data []byte
	room string
}

type MenuMessage struct {
	Type        int  `json:"type"`
	ReceiveMenu Menu `json:"menu"`
}

type Menu struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	Amount int    `json:"amount"`
	UserId []int  `json:"user_id"`
}

type Menus struct {
	Menus map[int]Menu
}

func test() {
	var menus []Menu
	x := Menu{Id: 1, Name: "test", Amount: 1, UserId: []int{1, 2, 3}}
	y := Menu{Id: 2, Name: "test2", Amount: 2, UserId: []int{1, 2, 3}}
	menus = append(menus, x)
	menus = append(menus, y)
}

type MenuArray struct {
	Menus []Menu `json:"menus"`
}

const (
	writeWait      = 10 * time.Second
	pongWait       = 20 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512
)

var hub = Hub{
	broadcast:  make(chan Message),
	register:   make(chan Subscription),
	unregister: make(chan Subscription),
	rooms:      make(map[string]map[*Connection]bool),
}

func serverWs(w http.ResponseWriter, r *http.Request, roomId string) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
	}
	c := &Connection{send: make(chan []byte, 256), ws: ws}
	s := Subscription{c, roomId}
	hub.register <- s
	go s.writePump()
	go s.readPump()
}

func (h *Hub) run() {
	for {
		select {
		case s := <-h.register:
			connections := h.rooms[s.room]
			if connections == nil {
				connections = make(map[*Connection]bool)
				h.rooms[s.room] = connections
			}
			h.rooms[s.room][s.conn] = true
		case s := <-h.unregister:
			connections := h.rooms[s.room]
			if connections != nil {
				if _, ok := connections[s.conn]; ok {
					delete(connections, s.conn)
					close(s.conn.send)
					if len(connections) == 0 {
						delete(h.rooms, s.room)
					}
				}
			}
		case m := <-h.broadcast:
			connections := h.rooms[m.room]
			for c := range connections {
				select {
				case c.send <- m.data:
				default:
					close(c.send)
					delete(connections, c)
					if len(connections) == 0 {
						delete(h.rooms, m.room)
					}
				}
			}
		}
	}
}

func (s Subscription) readPump() {
	c := s.conn
	defer func() {
		hub.unregister <- s
		c.ws.Close()
	}()
	c.ws.SetReadLimit(maxMessageSize)
	c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error { c.ws.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {

		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			log.Fatalf("Error parsing json: %v", err)
			break
		}
		if err != nil {
			log.Fatalf("Error convert to json: %v", err)
			break
		}
		message := &Message{data: msg, room: s.room}
		hub.broadcast <- *message
	}
}

func (c *Connection) write(messageType int, payload []byte) error {
	c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteMessage(messageType, payload)
}

func (s *Subscription) writePump() {
	c := s.conn
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.ws.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			if !ok {
				c.write(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.write(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.write(websocket.PingMessage, []byte{}); err != nil {
				return
			}
		}
	}
}

var addr = flag.String("addr", ":8088", "http service address")

func main() {
	go hub.run()

	router := gin.New()
	router.LoadHTMLFiles("indy.html", "index.html")
	router.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", nil)
	})
	router.GET("/room/:roomId", func(c *gin.Context) {
		c.HTML(200, "indy.html", nil)
	})
	router.GET("/ws/:roomId", func(c *gin.Context) {
		roomId := c.Param("roomId")
		serverWs(c.Writer, c.Request, roomId)
	})
	router.Run(*addr)
}
