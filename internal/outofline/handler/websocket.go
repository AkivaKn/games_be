package handler

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	closeRejected = 4400
	closeReplaced = 4500
	writeTimeout  = time.Second
)

// wsManager tracks one live socket per seat. A reconnect kicks the old socket
// with closeReplaced so a phone that wakes from sleep cannot end up with two
// writers for the same hand.
type wsManager struct {
	mu    sync.RWMutex
	rooms map[string]map[*websocket.Conn]int
}

func newWSManager() *wsManager {
	return &wsManager{rooms: make(map[string]map[*websocket.Conn]int)}
}

func (m *wsManager) AddClient(roomID string, conn *websocket.Conn, seat int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rooms[roomID] == nil {
		m.rooms[roomID] = make(map[*websocket.Conn]int)
	}
	m.rooms[roomID][conn] = seat
}

func (m *wsManager) RemoveClient(roomID string, conn *websocket.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if clients, ok := m.rooms[roomID]; ok {
		delete(clients, conn)
		if len(clients) == 0 {
			delete(m.rooms, roomID)
		}
	}
}

func (m *wsManager) KickSeat(roomID string, seat int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for conn, s := range m.rooms[roomID] {
		if s != seat {
			continue
		}
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(closeReplaced, "replaced by reconnect"),
			time.Now().Add(time.Second))
		delete(m.rooms[roomID], conn)
		_ = conn.Close()
	}
	if len(m.rooms[roomID]) == 0 {
		delete(m.rooms, roomID)
	}
}

func (m *wsManager) SendToSeat(roomID string, seat int, message interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for conn, s := range m.rooms[roomID] {
		if s != seat {
			continue
		}
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteJSON(message); err != nil {
			log.Printf("outofline SendToSeat: room %s seat %d: %v", roomID, seat, err)
			delete(m.rooms[roomID], conn)
			_ = conn.Close()
		}
	}
	if len(m.rooms[roomID]) == 0 {
		delete(m.rooms, roomID)
	}
}
