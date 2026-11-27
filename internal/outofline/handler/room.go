package handler

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/models"
	"boggle-api/internal/outofline/service"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// RoomHandler owns the HTTP and WebSocket edges. All game rules live in the
// service; every successful command triggers a fresh personalized snapshot,
// so clients never patch local state by hand.
type RoomHandler struct {
	game           *service.Service
	decks          *content.Registry
	ws             *wsManager
	allowedOrigins []string
	upgrader       websocket.Upgrader
}

func NewRoomHandler(game *service.Service, decks *content.Registry, origins []string) *RoomHandler {
	h := &RoomHandler{
		game:           game,
		decks:          decks,
		ws:             newWSManager(),
		allowedOrigins: origins,
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

func (h *RoomHandler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	for _, allowed := range h.allowedOrigins {
		if strings.TrimSuffix(origin, "/") == strings.TrimSuffix(allowed, "/") {
			return true
		}
	}
	log.Printf("outofline WS rejected origin: %s", origin)
	return false
}

func (h *RoomHandler) GetDecks(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"decks": h.decks.DeckInfos()})
}

type createRequest struct {
	Name           string   `json:"name"`
	Decks          []string `json:"decks"`
	TargetScore    int      `json:"target_score"`
	AdultConfirmed bool     `json:"adult_confirmed"`
}

func (h *RoomHandler) CreateRoom(c *gin.Context) {
	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	result, err := h.game.CreateRoom(c.Request.Context(), service.CreateInput{
		Name:           req.Name,
		DeckIDs:        req.Decks,
		TargetScore:    req.TargetScore,
		AdultConfirmed: req.AdultConfirmed,
	})
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"room_id": result.Room.ID,
		"code":    result.Room.Code,
		"token":   result.Player.Token,
		"seat":    result.Player.Seat,
		"name":    result.Player.Name,
	})
}

func (h *RoomHandler) PreviewRoom(c *gin.Context) {
	preview, err := h.game.PreviewRoom(c.Request.Context(), normalizeCode(c.Param("code")))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, preview)
}

type tokenRequest struct {
	Token string `json:"token"`
}

func (h *RoomHandler) PostStart(c *gin.Context) {
	room, req, ok := h.roomAndToken(c)
	if !ok {
		return
	}
	if err := h.game.Start(c.Request.Context(), room.ID, req.Token); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type submitRequest struct {
	Token string   `json:"token"`
	Cards []string `json:"cards"`
}

func (h *RoomHandler) PostSubmit(c *gin.Context) {
	var req submitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	room, ok := h.roomForCode(c)
	if !ok {
		return
	}
	if err := h.game.Submit(c.Request.Context(), room.ID, req.Token, req.Cards); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type pickRequest struct {
	Token        string `json:"token"`
	SubmissionID string `json:"submission_id"`
}

func (h *RoomHandler) PostPick(c *gin.Context) {
	var req pickRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	room, ok := h.roomForCode(c)
	if !ok {
		return
	}
	if err := h.game.Pick(c.Request.Context(), room.ID, req.Token, req.SubmissionID); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *RoomHandler) PostNext(c *gin.Context) {
	room, req, ok := h.roomAndToken(c)
	if !ok {
		return
	}
	if err := h.game.Next(c.Request.Context(), room.ID, req.Token); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *RoomHandler) PostForce(c *gin.Context) {
	room, req, ok := h.roomAndToken(c)
	if !ok {
		return
	}
	if err := h.game.Force(c.Request.Context(), room.ID, req.Token); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type removeRequest struct {
	Token string `json:"token"`
	Seat  int    `json:"seat"`
}

func (h *RoomHandler) PostRemove(c *gin.Context) {
	var req removeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	room, ok := h.roomForCode(c)
	if !ok {
		return
	}
	seat, err := h.game.RemovePlayer(c.Request.Context(), room.ID, req.Token, req.Seat)
	if err != nil {
		writeError(c, err)
		return
	}
	h.ws.KickSeat(room.ID, seat)
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *RoomHandler) PostRematch(c *gin.Context) {
	room, req, ok := h.roomAndToken(c)
	if !ok {
		return
	}
	if err := h.game.Rematch(c.Request.Context(), room.ID, req.Token); err != nil {
		writeError(c, err)
		return
	}
	h.broadcastRoom(c.Request.Context(), room.ID)
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// JoinRoomWS is the only WebSocket entrypoint:
//   - a valid token rejoins that seat (the old socket is kicked);
//   - no token claims the next free seat in a lobby;
//   - every rejection is an error event followed by close 4400, never a
//     silent network-looking drop.
func (h *RoomHandler) JoinRoomWS(c *gin.Context) {
	code := normalizeCode(c.Param("code"))
	room, err := h.game.RoomByCode(c.Request.Context(), code)
	if err != nil {
		h.upgradeAndReject(c, "room not found")
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("outofline WS upgrade: %v", err)
		return
	}
	defer conn.Close()

	var player *models.Player
	token := strings.TrimSpace(c.Query("token"))
	if token != "" {
		player, err = h.game.PlayerByToken(c.Request.Context(), room.ID, token)
	} else {
		player, err = h.game.AddPlayer(
			c.Request.Context(),
			room.ID,
			c.Query("name"),
			queryBool(c.Query("adult_confirmed")),
		)
	}
	if err != nil {
		h.reject(conn, userMessage(err))
		return
	}

	h.ws.KickSeat(room.ID, player.Seat)
	h.ws.AddClient(room.ID, conn, player.Seat)
	defer h.ws.RemoveClient(room.ID, conn)

	h.broadcastRoom(c.Request.Context(), room.ID)
	h.readLoop(room.ID, conn)
}

func (h *RoomHandler) readLoop(roomID string, conn *websocket.Conn) {
	conn.SetReadLimit(4096)
	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("outofline readLoop: room %s: %v", roomID, err)
			}
			return
		}
	}
}

// broadcastRoom sends each seat its own snapshot. It intentionally walks the
// roster rather than broadcasting one object: the judge sees anonymous entries
// while players see only their own hand.
func (h *RoomHandler) broadcastRoom(ctx context.Context, roomID string) {
	players, err := h.game.Players(ctx, roomID)
	if err != nil {
		log.Printf("outofline broadcast players: %v", err)
		return
	}
	for _, player := range players {
		snapshot, err := h.game.Snapshot(ctx, roomID, player.Seat)
		if err != nil {
			log.Printf("outofline snapshot room %s seat %d: %v", roomID, player.Seat, err)
			continue
		}
		h.ws.SendToSeat(roomID, player.Seat, snapshot)
	}
}

func (h *RoomHandler) roomForCode(c *gin.Context) (*models.Room, bool) {
	room, err := h.game.RoomByCode(c.Request.Context(), normalizeCode(c.Param("code")))
	if err != nil {
		writeError(c, err)
		return nil, false
	}
	return room, true
}

func (h *RoomHandler) roomAndToken(c *gin.Context) (*models.Room, tokenRequest, bool) {
	var req tokenRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return nil, req, false
	}
	room, ok := h.roomForCode(c)
	if !ok {
		return nil, req, false
	}
	return room, req, true
}

func (h *RoomHandler) upgradeAndReject(c *gin.Context, message string) {
	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("outofline WS upgrade: %v", err)
		return
	}
	defer conn.Close()
	h.reject(conn, message)
}

func (h *RoomHandler) reject(conn *websocket.Conn, message string) {
	_ = conn.WriteJSON(gin.H{"event": "error", "message": message})
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(closeRejected, message),
		time.Now().Add(time.Second))
}

func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func queryBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func userMessage(err error) string {
	if ge, ok := service.IsGameError(err); ok {
		return ge.Message
	}
	log.Printf("outofline internal error: %v", err)
	return "could not join the room"
}

func writeError(c *gin.Context, err error) {
	if ge, ok := service.IsGameError(err); ok {
		c.JSON(ge.Status, gin.H{"error": ge.Message})
		return
	}
	log.Printf("outofline internal error: %v", err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "something went wrong"})
}
