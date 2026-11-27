package server

import (
	"log"

	"github.com/gin-gonic/gin"
	"github.com/vingarcia/ksql"

	"boggle-api/internal/app"
	"boggle-api/internal/outofline/content"
	"boggle-api/internal/outofline/handler"
	"boggle-api/internal/outofline/repository"
	"boggle-api/internal/outofline/service"
)

// RoutePrefix is the shared engine prefix. The hyphen is part of the public
// URL (/api/out-of-line), not the Go package name.
const RoutePrefix = "out-of-line"

type Module struct {
	handler      *handler.RoomHandler
	strictLimit  gin.HandlerFunc
	generalLimit gin.HandlerFunc
}

func NewModule(db ksql.DB, origins []string, limiters app.Limiters) *Module {
	decks, err := content.Load()
	if err != nil {
		// Bad embedded content must fail the process at startup, not at the
		// first game. This is a build/deploy error, not a player error.
		log.Fatalf("outofline content: %v", err)
	}
	repo := repository.NewRepository(db)
	game := service.New(db, repo, decks)
	return &Module{
		handler:      handler.NewRoomHandler(game, decks, origins),
		strictLimit:  limiters.Strict,
		generalLimit: limiters.General,
	}
}

func (m *Module) RegisterRoutes(engine *gin.Engine) {
	api := engine.Group("/api/" + RoutePrefix)
	{
		api.Use(m.generalLimit)
		api.GET("/decks", m.handler.GetDecks)
		api.GET("/rooms/:code", m.handler.PreviewRoom)
		// Creation is the one endpoint that can create durable rows on a
		// whim, so it gets the strict limiter. Gameplay commands stay on the
		// general budget: a 12-player room on one shared IP can legitimately
		// exceed 10 writes/minute.
		api.POST("/rooms", m.strictLimit, m.handler.CreateRoom)

		rooms := api.Group("/rooms/:code")
		{
			rooms.POST("/start", m.handler.PostStart)
			rooms.POST("/submit", m.handler.PostSubmit)
			rooms.POST("/pick", m.handler.PostPick)
			rooms.POST("/next", m.handler.PostNext)
			rooms.POST("/force", m.handler.PostForce)
			rooms.POST("/remove", m.handler.PostRemove)
			rooms.POST("/rematch", m.handler.PostRematch)
		}
	}

	ws := engine.Group("/ws/" + RoutePrefix)
	{
		ws.GET("/rooms/:code", m.handler.JoinRoomWS)
	}
}
