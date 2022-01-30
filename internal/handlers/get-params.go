package handlers

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"time"

	b "github.com/ekzjuperi/binance-trading-bot/internal/bot"
)

// GetParams func print bot parameters.
func GetParams(bot *b.Bot) func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		buf := new(bytes.Buffer)

		_, err := buf.WriteString(fmt.Sprintf("Bot ticker: %v \n", bot.GetSymbol()))
		if err != nil {
			log.Printf("GetParams() buf.WriteString(bot.symbol) err: %v\n", err)
		}

		_, err = buf.WriteString(fmt.Sprintf("Sum of open orders for last 24 hours: %v \n", bot.GetSumOfOpenOrders()))
		if err != nil {
			log.Printf("GetParams() buf.WriteString(bot.GetSumOfOpenOrders()) err: %v\n", err)
		}

		_, err = buf.WriteString(fmt.Sprintf("Calculated stop price: %v \n", bot.GetStopPrice()))
		if err != nil {
			log.Printf("GetParams() buf.WriteString(bot.GetStopPrice()) err: %v\n", err)
		}

		_, err = buf.WriteString(fmt.Sprintf("Last trade price: %v \n", bot.GetLastTradePrice()))
		if err != nil {
			log.Printf("GetParams() buf.WriteString(bot.GetLastTradePrice()) err: %v\n", err)
		}

		_, err = buf.WriteString(fmt.Sprintf("Last time trade: %v \n", time.Unix(bot.GetLastTimeTrade(), 0)))
		if err != nil {
			log.Printf("GetParams() buf.WriteString(bot.GetLastTimeTrade()) err: %v\n", err)
		}

		_, err = resWriter.Write(buf.Bytes())
		if err != nil {
			log.Printf("GetParams() resWriter.Write(buf.Bytes()) err: %v\n", err)
		}
	}
}
