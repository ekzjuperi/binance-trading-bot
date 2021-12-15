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

	mux.HandleFunc("/info", handlers.GetAccountInfo(o.bot.Client))
	mux.HandleFunc("/orders", handlers.GetListOpenOrders(o.bot.Client, o.bot.Symbol))
	mux.HandleFunc("/profit", handlers.GetProfitStatictics())
	mux.HandleFunc("/get-logs", handlers.GetLogs())
	mux.HandleFunc("/set-stop-price", o.bot.SetStopPrice())

	return http.ListenAndServe(fmt.Sprintf(":%v", o.port), mux)
}
