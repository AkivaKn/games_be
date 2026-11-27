package server

import (
	"log"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	mgin "github.com/ulule/limiter/v3/drivers/middleware/gin"
	"github.com/ulule/limiter/v3/drivers/store/memory"

	"boggle-api/internal/app"
	"boggle-api/internal/misclue/handler"
	"boggle-api/internal/misclue/llm"
)

const RoutePrefix = "misclue"

type Module struct {
	handler *handler.Handler
	aiLimit gin.HandlerFunc
}

func NewModule(limiters app.Limiters) *Module {
	client := llm.NewFromEnv()
	return &Module{
		handler: handler.New(client),
		aiLimit: aiTurnLimiter(limiters.Strict),
	}
}

func aiTurnLimiter(fallback gin.HandlerFunc) gin.HandlerFunc {
	raw := os.Getenv("MISCLUE_AI_RATE_LIMIT_PER_MIN")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		log.Printf("misclue: invalid MISCLUE_AI_RATE_LIMIT_PER_MIN=%q, using default strict limiter", raw)
		return fallback
	}
	if n > app.StrictLimitPerMinute {
		log.Printf("misclue: MISCLUE_AI_RATE_LIMIT_PER_MIN=%d above strict default %d, using default strict limiter", n, app.StrictLimitPerMinute)
		return fallback
	}
	log.Printf("misclue: AI rate limit set to %d/min", n)
	return mgin.NewMiddleware(limiter.New(memory.NewStore(), limiter.Rate{Limit: int64(n), Period: time.Minute}))
}

func (m *Module) RegisterRoutes(engine *gin.Engine) {
	api := engine.Group("/api/" + RoutePrefix)
	{
		api.POST("/ai/turn", m.aiLimit, m.handler.Turn)
	}
}
