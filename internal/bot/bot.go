package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/ekzjuperi/binance-trading-bot/configs"
)

const (
	sizeChan = 0
	profit   = 100
	timeOut  = 120
)

type Bot struct {
	client       *binance.Client
	analysisСhan chan *binance.WsAggTradeEvent
	orderChan    chan *Order
	Symbol       string // trading pair
	lastTimeDeal int64  // unix time from last deal
}

type Order struct {
	Symbol   string
	Price    float64
	Quantity float64
}

func NewBot(client *binance.Client, cfg *configs.BotConfig) *Bot {
	var bot Bot

	bot.client = client
	bot.analysisСhan = make(chan *binance.WsAggTradeEvent, sizeChan)
	bot.orderChan = make(chan *Order, sizeChan)
	bot.Symbol = cfg.Symbol

	return &bot
}

func (o *Bot) Start() {
	var wg sync.WaitGroup

	wg.Add(1)

	go o.Analyze()

	wg.Add(1)

	go o.Trade()

	wg.Wait()

	log.Println("Bot stop work")
}

func StartPricesStream(analysisСhan chan *binance.WsAggTradeEvent) chan struct{} {
	wsAggTradeHandler := func(event *binance.WsAggTradeEvent) {
		analysisСhan <- event
	}
	errHandler := func(err error) {
		log.Println(err)
	}

	doneC, _, err := binance.WsAggTradeServe("BTCUSDT", wsAggTradeHandler, errHandler)
	if err != nil {
		log.Println(err)
		return nil
	}

	return doneC
}

func (o *Bot) Analyze() {
	wsAggTradeHandler := func(event *binance.WsAggTradeEvent) {
		o.analysisСhan <- event
	}

	errHandler := func(err error) {
		log.Println(err)
	}

	doneC, _, err := binance.WsAggTradeServe("BTCUSDT", wsAggTradeHandler, errHandler)
	if err != nil {
		log.Println(err)
		return
	}

	oldEvent := <-o.analysisСhan
	oldEvent2 := <-o.analysisСhan
	oldEvent3 := <-o.analysisСhan

	timer := time.NewTimer(time.Second * timeOut)
	timer2 := time.NewTimer(time.Second * 60)
	timer3 := time.NewTimer(time.Second * 60)

	log.Println("oldEvent = ", oldEvent)
	log.Println("Start trading")

	for {
		select {
		case <-timer.C:
			log.Printf("timer 1")
			o.MakeDecision(oldEvent)

			timer.Reset(time.Second * timeOut)
		case <-timer2.C:
			log.Printf("timer 2")
			o.MakeDecision(oldEvent2)

			timer2.Reset(time.Second * timeOut)
		case <-timer3.C:
			log.Printf("timer 3")
			o.MakeDecision(oldEvent3)

			timer3.Reset(time.Second * 60)
		default:
			select {
			case <-doneC:
				log.Println("doneC send event")
				break

			case <-o.analysisСhan:
				continue
			}
		}
	}
}

func (o *Bot) MakeDecision(oldEvent *binance.WsAggTradeEvent) {
	newEvent := <-o.analysisСhan
	newEventPrice, _ := strconv.ParseFloat(newEvent.Price, 32)
	oldEventPrice, _ := strconv.ParseFloat(oldEvent.Price, 32)

	difference := newEventPrice / oldEventPrice * 100
	log.Println("difference = ", difference)

	if difference < 99.91 {
		order := Order{
			Symbol:   newEvent.Symbol,
			Price:    newEventPrice,
			Quantity: 0.001,
		}

		o.orderChan <- &order
	}

	*oldEvent = *newEvent
}

func (o *Bot) Trade() {
	for order := range o.orderChan {
		log.Printf("order: %v\n", order)

		timeFromLastDeal := (time.Now().Unix() - o.lastTimeDeal)
		if timeFromLastDeal < 60 {
			log.Printf("order: %v skip, %v time has passed since the last deal s\n", order, timeFromLastDeal)
			continue
		}

		_, err := o.CreateOrder(order, binance.SideTypeBuy, binance.OrderTypeMarket, binance.TimeInForceTypeGTC)
		if err != nil {
			log.Printf("An error occurred during order execution, order: %v, err: %v\n", order, err)
			continue
		}

		log.Printf("Order %v executed\n", order)

		for {
			response, err := o.CreateOrder(order, binance.SideTypeSell, binance.OrderTypeLimit, binance.TimeInForceTypeGTC)

			if err != nil {
				log.Printf("An error occurred during order execution, order: %v, type: %v, err: %v\n", order, binance.OrderTypeLimit, err)
				continue
			}

			log.Printf("Order limit create %v\n", response)

			break
		}

		o.lastTimeDeal = time.Now().Unix()
	}
}

func (o *Bot) CreateOrder(
	order *Order,
	side binance.SideType,
	typeOrder binance.OrderType,
	timeInForce binance.TimeInForceType,
) (*binance.CreateOrderResponse, error) {
	var orderResponse *binance.CreateOrderResponse

	var err error

	switch typeOrder {
	case binance.OrderTypeMarket:
		orderResponse, err = o.client.NewCreateOrderService().Symbol(order.Symbol).
			Side(side).
			Type(typeOrder).
			Quantity(fmt.Sprintf("%f", order.Quantity)).
			Do(context.Background())

	case binance.OrderTypeLimit:
		getProfit := int(order.Price + profit)

		orderResponse, err = o.client.NewCreateOrderService().Symbol(order.Symbol).
			Side(side).
			Type(typeOrder).
			TimeInForce(timeInForce).
			Quantity(fmt.Sprintf("%v", order.Quantity)).
			Price(fmt.Sprintf("%v", getProfit)).
			Do(context.Background())
	case binance.OrderTypeStopLoss:
		stopLoss := int(order.Price - profit)

		orderResponse, err = o.client.NewCreateOrderService().Symbol(order.Symbol).
			Side(side).
			Type(typeOrder).
			TimeInForce(timeInForce).
			Quantity(fmt.Sprintf("%v", order.Quantity)).
			Price(fmt.Sprintf("%v", stopLoss)).
			Do(context.Background())
	}

	return orderResponse, err
}

func (o *Bot) GetAccountInfo() func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		res, err := o.client.NewGetAccountService().Do(context.Background())
		if err != nil {
			log.Printf("o.client.NewGetAccountService() err: %v\n", err)
			return
		}

		var usdt binance.Balance

		for _, ass := range res.Balances {
			if ass.Asset == "USDT" {
				usdt = ass
				break
			}
		}

		bb, err := json.Marshal(usdt)
		if err != nil {
			log.Printf("json.Marshal(model) err: %v\n", err)
		}

		log.Printf("account info: %v\n", string(bb))

		_, err = resWriter.Write(bb)
		if err != nil {
			log.Printf("resWriter.Write() err: %v\n", err)
		}
	}
}

func (o *Bot) ListOpenOrders() func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		openOrders, err := o.client.NewListOpenOrdersService().Symbol("").
			Do(context.Background())
		if err != nil {
			log.Printf("o.client.NewListOpenOrdersService() err: %v\n", err)
			return
		}

		log.Printf("open orders: %v\n", openOrders)

		bb, err := json.Marshal(openOrders)
		if err != nil {
			log.Printf("json.Marshal(model) err: %v\n", err)
		}

		_, err = resWriter.Write(bb)
		if err != nil {
			log.Printf("resWriter.Write() err: %v\n", err)
		}
	}
}
