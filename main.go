package main

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/patrickmn/go-cache"
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

func serverWs(w http.ResponseWriter, r *http.Request, roomId string, cache *cache.Cache) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
	}
	c := &Connection{send: make(chan []byte, 256), ws: ws}
	s := Subscription{c, roomId}
	hub.register <- s
	go s.writePump(cache)
	go s.readPump(cache)
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

func (s Subscription) readPump(cache *cache.Cache) {
	c := s.conn
	defer func() {
		hub.unregister <- s
		c.ws.Close()
	}()
	c.ws.SetReadLimit(maxMessageSize)
	c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error { c.ws.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {

		var menu Menu
		err := c.ws.ReadJSON(&menu)
		if err != nil {
			log.Fatalf("Error parsing json: %v", err)
			break
		}
		log.Printf("Read Pump Menu: %v", menu.Amount)
		menus := updateCache(cache, s.room, &menu)
		log.Printf("Read Pump Menus: %v", menus.Menus)
		menusArray := menus.toArray()
		log.Printf("Read Pump Array: %v", menus.Menus)
		message, err := json.Marshal(menusArray)
		log.Printf("Read Pump Message: %v", message)
		if err != nil {
			log.Fatalf("Error convert to json: %v", err)
			break
		}
		m := Message{message, s.room}
		hub.broadcast <- m
	}
}

func (c *Connection) write(messageType int, payload []byte) error {
	c.ws.SetWriteDeadline(time.Now().Add(writeWait))
	return c.ws.WriteMessage(messageType, payload)
}

func (c *Connection) sendMessage(cache *cache.Cache, roomId string) error {
	menus := getCache(cache, roomId)
	for _, menu := range menus.Menus {
		err := c.ws.WriteJSON(menu)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Subscription) writePump(cache *cache.Cache) {
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

func main() {
	go hub.run()

	cache := cache.New(5*time.Minute, 10*time.Minute)

	router := gin.New()
	router.LoadHTMLFiles("indy.html")
	router.GET("/room/:roomId", func(c *gin.Context) {
		c.HTML(200, "indy.html", nil)
	})
	router.GET("/ws/:roomId", func(c *gin.Context) {
		roomId := c.Param("roomId")
		serverWs(c.Writer, c.Request, roomId, cache)
	})
	router.Run("0.0.0.0:8088")
}

func setCache(c *cache.Cache, key string, value interface{}) {
	c.Set(key, value, cache.DefaultExpiration)
}

func getCache(c *cache.Cache, key string) Menus {
	some, found := c.Get(key)
	if !found {
		return Menus{map[int]Menu{}}
	}

	val, ok := some.(Menus)
	if !ok {
		return Menus{map[int]Menu{}}
	}
	return val
}

func updateCache(c *cache.Cache, roomId string, addMenu *Menu) Menus {
	log.Printf("addMenu: %v", addMenu)
	menus := getCache(c, roomId)
	updateMenus := calSomeValue(c, roomId, addMenu, menus)
	log.Printf("updateMenus: %v", updateMenus)
	setCache(c, roomId, updateMenus)
	return updateMenus
}

func calSomeValue(c *cache.Cache, roomId string, addMenu *Menu, menus Menus) Menus {
	value, ok := menus.Menus[addMenu.Id]
	if !ok {
		menus.Menus[addMenu.Id] = *addMenu
		return menus
	}
	value.addAmount(addMenu)
	value.addUserId(addMenu)
	return menus
}

func (m *Menu) addAmount(menu *Menu) {
	m.Amount += menu.Amount
	if m.Amount <= 0 {
		m.Amount = 0
	}
}

func (m *Menu) addUserId(menu *Menu) {
	m.UserId = menu.UserId
}

func parseJson(data []byte, menu *Menu) error {
	err := json.Unmarshal(data, &menu)
	if err != nil {
		return err
	}
	return nil
}

func (menus *Menus) toArray() MenuArray {
	var result []Menu
	for _, menu := range menus.Menus {
		result = append(result, menu)
	}
	return MenuArray{result}
}
