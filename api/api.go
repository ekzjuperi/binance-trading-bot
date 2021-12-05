package api

import (
	"fmt"
	"net/http"

	b "github.com/ekzjuperi/binance-trading-bot/internal/bot"
	"github.com/ekzjuperi/binance-trading-bot/internal/handlers"
)

type API struct {
	bot  *b.Bot
	port string
}

// NewAPI creates new Api instance.
func NewAPI(
	bot *b.Bot,
	port string,
) *API {
	return &API{
		bot:  bot,
		port: port,
	}
}

// Start runs API.
func (o *API) Start() error {
	mux := http.NewServeMux()

	mux.HandleFunc("/info", o.bot.GetAccountInfo())
	mux.HandleFunc("/orders", o.bot.GetListOpenOrders())
	mux.HandleFunc("/profit", o.bot.GetProfitStatictics())
	mux.HandleFunc("/set-stop-price", o.bot.SetStopPrice())
	mux.HandleFunc("/get-logs", handlers.GetLogs())

	return http.ListenAndServe(fmt.Sprintf(":%v", o.port), mux)
}
