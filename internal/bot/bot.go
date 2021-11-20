package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/adshao/go-binance/v2"
	"github.com/ekzjuperi/binance-trading-bot/configs"
	"github.com/ekzjuperi/binance-trading-bot/internal/cache"
)

const (
	sizeChan                         = 100
	profit                           = 220
	timeOutForTimer                  = 120
	pauseAfterTrade                  = 120
	Quantity                         = 0.01
	stopPrice                        = 61500
	timeUntilLastTradePriceWillReset = 2000 //  time after the last trade price is reset
)

type Bot struct {
	cache               *cache.Cache
	client              *binance.Client
	analysisСhan        chan *binance.WsAggTradeEvent
	orderChan           chan *Order
	compleatedTradeChan chan *CompleatedTrade
	Symbol              string // trading pair
	lastTimeTrade       int64  // unix time from last trade
	stopPrice           float64
	lastTradePrice      float64

	wg  *sync.WaitGroup
	rwm *sync.RWMutex
}

type Order struct {
	Symbol   string
	Price    float64
	Quantity float64
}

type CompleatedTrade struct {
	EnterOrder *binance.Order
	ExiteOrder *binance.Order
	Profit     float64
}

// NewBot func initializes the bot.
func NewBot(client *binance.Client, cfg *configs.BotConfig) *Bot {
	var bot Bot

	bot.cache = cache.NewCache()
	bot.client = client
	bot.analysisСhan = make(chan *binance.WsAggTradeEvent, sizeChan)
	bot.orderChan = make(chan *Order, sizeChan)
	bot.compleatedTradeChan = make(chan *CompleatedTrade, sizeChan)
	bot.Symbol = cfg.Symbol

	bot.wg = &sync.WaitGroup{}
	bot.rwm = &sync.RWMutex{}

	bot.getStopPrice()

	return &bot
}

// Start func start bot.
func (o *Bot) Start() {
	o.wg.Add(1)

	go o.Analyze()

	o.wg.Add(1)

	go o.Trade()

	o.wg.Add(1)

	go o.CheckLimitOrder()

	o.wg.Wait()

	log.Println("Bot stop work")
}

// StartPricesStream func stream prices from binance.
func (o *Bot) StartPricesStream() (chan struct{}, error) {
	wsAggTradeHandler := func(event *binance.WsAggTradeEvent) {
		o.analysisСhan <- event
	}
	errHandler := func(err error) {
		log.Println(err)
	}

	doneC, _, err := binance.WsAggTradeServe(o.Symbol, wsAggTradeHandler, errHandler)
	if err != nil {
		log.Println(err)
		return nil, err
	}

	return doneC, nil
}

// Analyze func analyzes prices from the exchange stream.
func (o *Bot) Analyze() {
	defer o.wg.Done()

	doneC, err := o.StartPricesStream()
	if err != nil {
		log.Printf("o.StartPricesStream() err: %v\n", err)

		return
	}

	oldEvent := <-o.analysisСhan
	oldEvent2 := <-o.analysisСhan
	oldEvent3 := <-o.analysisСhan

	price, _ := strconv.ParseFloat(oldEvent.Price, 32)

	o.lastTradePrice = price

	timer := time.NewTimer(time.Second * timeOutForTimer)
	timer2 := time.NewTimer(time.Second * 60)
	timer3 := time.NewTimer(time.Second * 60)
	timer4 := time.NewTimer(time.Minute * 5)

	log.Println("Start trading")

	for {
		select {
		case <-timer.C:
			o.MakeDecision(oldEvent)
			timer.Reset(time.Second * timeOutForTimer)

		case <-timer2.C:
			o.MakeDecision(oldEvent2)
			timer2.Reset(time.Second * timeOutForTimer)

		case <-timer3.C:
			o.MakeDecision(oldEvent3)
			timer3.Reset(time.Second * 60)

		case <-timer4.C:
			o.getStopPrice()
			timer4.Reset(time.Minute * 5)

		default:
			select {
			case <-doneC:
				// if doneC send event, restart o.Analyze()
				log.Println("doneC send event")

				o.wg.Add(1)

				go o.Analyze()

				return

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
	log.Println("newEventPrice = ", newEventPrice)

	if newEventPrice >= o.stopPrice {
		*oldEvent = *newEvent

		return
	}

	if difference < 99.7 {
		order := Order{
			Symbol:   newEvent.Symbol,
			Price:    newEventPrice,
			Quantity: 0.05,
		}

		o.orderChan <- &order
	} else if difference < 99.82 {
		order := Order{
			Symbol:   newEvent.Symbol,
			Price:    newEventPrice,
			Quantity: 0.003,
		}

		o.orderChan <- &order
	} else if difference < 99.89 {
		order := Order{
			Symbol:   newEvent.Symbol,
			Price:    newEventPrice,
			Quantity: 0.002,
		}

		o.orderChan <- &order
	}

	*oldEvent = *newEvent
}

func (o *Bot) Trade() {
	defer o.wg.Done()

	// get order from orderChan.
	for order := range o.orderChan {
		log.Printf("order: %v\n", order)

		// if not enough time has passed since the last trade, skip new trade.
		timeFromLastTrade := (time.Now().Unix() - o.lastTimeTrade)
		if timeFromLastTrade < pauseAfterTrade {
			log.Printf("order: %v skip, %v s. has passed since the last trade s\n", order, timeFromLastTrade)
			continue
		}

		// if enough time has passed since the last trade, reset the price of the last trade.
		if timeFromLastTrade > timeUntilLastTradePriceWillReset {
			o.rwm.Lock()
			o.lastTradePrice = 0
			o.rwm.Unlock()
		}

		// if last trade price >= new price, skip new trade.
		if (o.lastTradePrice != 0) && order.Price >= o.lastTradePrice {
			log.Printf("order: %v skip, order.Price(%v) >= o.lastTradePrice(%v)\n", order, order.Price, o.lastTradePrice)
			continue
		}

		// get the highest price bid in the order book.
		depth, err := o.client.NewDepthService().Symbol(order.Symbol).
			Do(context.Background())
		if err != nil {
			log.Printf("o.client.NewDepthService(%v) err: %v\n", order.Symbol, err)
			continue
		}

		price, err := strconv.ParseFloat(depth.Bids[0].Price, 32)
		if err != nil {
			log.Printf("strconv.ParseFloat(depth.Bids[0].Price) err: %v\n", err)
			continue
		}

		order.Price = price

		// create order
		firstOrderResolve, err := o.CreateOrder(order, binance.SideTypeBuy, binance.OrderTypeLimitMaker, binance.TimeInForceTypeGTC)
		if err != nil && err.Error() == "<APIError> code=-2010, msg=Account has insufficient balance for requested action." {
			log.Printf("Account has insufficient balance for buy %v\n", order)

			continue
		}

		if err != nil {
			log.Printf("An error occurred during order execution, order: %v, err: %v\n", order, err)

			// if  api response have err, retry create order
			firstOrderResolve, order, err = o.retryCreateOrder(order)
			if err != nil {
				fmt.Println(err)

				continue
			}
		}

		o.rwm.Lock()
		o.lastTimeTrade = time.Now().Unix()
		o.lastTradePrice = order.Price - profit
		o.rwm.Unlock()

		go func(firstOrderResolve *binance.CreateOrderResponse, order *Order) {
			timeNow := time.Now().Unix()

			var entryBinanceOrder *binance.Order

			for {
				orderTimeout := (time.Now().Unix() - timeNow)

				entryBinanceOrder, err = o.client.NewGetOrderService().Symbol(firstOrderResolve.Symbol).
					OrderID(firstOrderResolve.OrderID).Do(context.Background())
				if err != nil {
					log.Println(err)
					continue
				}

				if entryBinanceOrder.Status == binance.OrderStatusTypeFilled {
					log.Printf("entry order execute price: %v, quantity: %v", entryBinanceOrder.Price, entryBinanceOrder.ExecutedQuantity)
					break
				}

				if orderTimeout > 100 && entryBinanceOrder.Status == binance.OrderStatusTypeNew {
					_, err := o.client.NewCancelOrderService().Symbol(firstOrderResolve.Symbol).
						OrderID(firstOrderResolve.OrderID).Do(context.Background())
					if err != nil {
						log.Println(err)
						continue
					}

					o.rwm.Lock()
					o.lastTimeTrade = 0
					o.lastTradePrice = order.Price + profit
					o.rwm.Unlock()

					log.Printf("order %v canceled after timeout", order)

					return
				}

				time.Sleep(time.Second * 3)
			}

			log.Printf("Order %v executed\n", order)

			order.Price += profit

			for {
				secondOrderResolve, err := o.CreateOrder(order, binance.SideTypeSell, binance.OrderTypeLimit, binance.TimeInForceTypeGTC)
				if err != nil {
					log.Printf("An error occurred during order execution, order: %v, type: %v, err: %v\n", order, binance.OrderTypeLimit, err)
					continue
				}

				log.Printf("Order limit create %v\n", secondOrderResolve)

				exitBinanceOrder, err := o.client.NewGetOrderService().Symbol(secondOrderResolve.Symbol).
					OrderID(secondOrderResolve.OrderID).Do(context.Background())
				if err != nil {
					log.Println(err)
					continue
				}

				cTrade := &CompleatedTrade{
					EnterOrder: entryBinanceOrder,
					ExiteOrder: exitBinanceOrder,
				}

				o.compleatedTradeChan <- cTrade

				break
			}

			o.lastTimeTrade = time.Now().Unix()
		}(firstOrderResolve, order)
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
	case binance.OrderTypeLimitMaker:
		orderResponse, err = o.client.NewCreateOrderService().Symbol(order.Symbol).
			Side(side).
			Type(typeOrder).
			Quantity(fmt.Sprintf("%f", order.Quantity)).
			Price(fmt.Sprintf("%.2f", order.Price)).
			Do(context.Background())

	case binance.OrderTypeLimit:
		orderResponse, err = o.client.NewCreateOrderService().Symbol(order.Symbol).
			Side(side).
			Type(typeOrder).
			TimeInForce(timeInForce).
			Quantity(fmt.Sprintf("%v", order.Quantity)).
			Price(fmt.Sprintf("%.2f", order.Price)).
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

func (o *Bot) CheckLimitOrder() {
	defer o.wg.Done()

	go func() {
		for {
			time.Sleep(time.Second * 10)

			ordersAwaitingCompletion := o.cache.Cache.Items()

			if len(ordersAwaitingCompletion) == 0 {
				continue
			}

			orders, err := o.client.NewListOrdersService().Symbol(o.Symbol).Do(context.Background())
			if err != nil {
				log.Println(err)
				continue
			}

			listBinanceOrders := map[string]*binance.Order{}

			for _, order := range orders {
				listBinanceOrders[fmt.Sprintf("%v", order.OrderID)] = order
			}

			for _, awaitingOrder := range ordersAwaitingCompletion {
				b, err := json.Marshal(awaitingOrder.Object)
				if err != nil {
					log.Printf("json.Marshal(awaitingOrder.Object) err: %v\n", err)

					continue
				}

				var compleatedTrade CompleatedTrade

				err = json.Unmarshal(b, &compleatedTrade)
				if err != nil {
					log.Printf("json.Unmarshal(b, compleatedTrade) err: %v\n", err)

					continue
				}

				binanceOrder, ok := listBinanceOrders[fmt.Sprintf("%v", compleatedTrade.ExiteOrder.OrderID)]
				if !ok {
					log.Printf("listBinanceOrders didn't have awaitingOrder")
					continue
				}

				if binanceOrder.Status == binance.OrderStatusTypeFilled {
					compleatedTrade.ExiteOrder = binanceOrder
					compleatedTrade.Profit = calculateProfit(compleatedTrade)
					SaveCompleatedTradeInFile(compleatedTrade)

					o.cache.Cache.Delete(fmt.Sprintf("%v", compleatedTrade.ExiteOrder.OrderID))

					newlastTradePrice, _ := strconv.ParseFloat(compleatedTrade.ExiteOrder.Price, 32)

					o.rwm.Lock()
					o.cache.SaveCache()

					if o.lastTradePrice == 0 {
						o.lastTradePrice = newlastTradePrice - profit
					}

					o.lastTimeTrade = time.Now().Unix()
					o.rwm.Unlock()

					log.Printf("exite order execute price: %v, quantity: %v profit %v",
						binanceOrder.Price,
						binanceOrder.ExecutedQuantity,
						compleatedTrade.Profit,
					)
				}
			}
		}
	}()

	for compleatedOrder := range o.compleatedTradeChan {
		err := o.cache.Cache.Add(fmt.Sprintf("%v", compleatedOrder.ExiteOrder.OrderID), compleatedOrder, 0)
		if err != nil {
			log.Printf("o.cache.Cache.Add() err: %v\n", err)
		}

		o.rwm.Lock()
		o.cache.SaveCache()
		o.rwm.Unlock()
	}
}

func SaveCompleatedTradeInFile(compleatedTrade CompleatedTrade) {
	file, err := os.OpenFile("statistics/trade-statistics.json", os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		log.Printf("os.OpenFile(trade-statistics.json) err: %v\n", err)
	}

	defer file.Close()

	b, err := json.Marshal(compleatedTrade)
	if err != nil {
		log.Printf("json.Marshal(compleatedTrade) err: %v\n", err)
		return
	}

	if _, err = io.WriteString(file, "\n"); err != nil {
		log.Printf("f.WriteString(/n) err: %v\n", err)
	}

	if _, err = io.WriteString(file, string(b)); err != nil {
		log.Printf("f.Write(string(b)) err: %v\n", err)
	}
}

func calculateProfit(compleatedTrade CompleatedTrade) float64 {
	EnterCummulativeQuoteQuantity, _ := strconv.ParseFloat(compleatedTrade.EnterOrder.CummulativeQuoteQuantity, 32)
	ExiteCummulativeQuoteQuantity, _ := strconv.ParseFloat(compleatedTrade.ExiteOrder.CummulativeQuoteQuantity, 32)

	return ExiteCummulativeQuoteQuantity - EnterCummulativeQuoteQuantity
}

func (o *Bot) GetAccountInfo() func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		res, err := o.client.NewGetAccountService().Do(context.Background())
		if err != nil {
			log.Printf("o.client.NewGetAccountService() err: %v\n", err)
			return
		}

		var notZeroAssets []binance.Balance

		for _, ass := range res.Balances {
			if ass.Asset == "BUSD" || ass.Asset == "USDT" || ass.Asset == "BTC" {
				notZeroAssets = append(notZeroAssets, ass)
			}
		}

		bb, err := json.Marshal(notZeroAssets)
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

func (o *Bot) SetStopPrice() func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		price := query["price"][0]

		if price == "" {
			_, err := resWriter.Write([]byte(fmt.Sprintf("incorrect stop price = %v", price)))
			if err != nil {
				log.Printf("resWriter.Write() err: %v\n", err)
			}

			return
		}

		stopPriceFloat, _ := strconv.ParseFloat(price, 32)

		o.stopPrice = stopPriceFloat

		_, err := resWriter.Write([]byte(fmt.Sprintf("stop price now %v", stopPriceFloat)))
		if err != nil {
			log.Printf("resWriter.Write() err: %v\n", err)
		}
	}
}

func (o *Bot) GetProfitStatictics() func(http.ResponseWriter, *http.Request) {
	return func(resWriter http.ResponseWriter, req *http.Request) {
		trades := []*CompleatedTrade{}

		jsonFile, err := os.Open("statistics/trade-statistics.json")
		if err != nil {
			log.Printf("GetProfitStatictics() os.Open(statistics/trade-statistics.json) err: %v\n", err)

			return
		}
		defer jsonFile.Close()

		fileScanner := bufio.NewScanner(jsonFile)
		for fileScanner.Scan() {
			if fileScanner.Text() == "" {
				continue
			}

			trade := &CompleatedTrade{}

			err = json.Unmarshal([]byte(fileScanner.Text()), &trade)
			if err != nil {
				log.Printf("GetProfitStatictics() json.Unmarshal(%v) err: %v\n", trade, err)

				return
			}

			trades = append(trades, trade)
		}

		err = fileScanner.Err()
		if err != nil {
			log.Printf("Error while reading file: %s\n", err)

			return
		}

		if len(trades) == 0 {
			_, err = resWriter.Write([]byte("No deals"))
			if err != nil {
				log.Printf("GetProfitStatictics() resWriter.Write(totalProfit) err: %v\n", err)
			}

			return
		}

		sort.SliceStable(trades, func(i, j int) bool {
			return trades[i].ExiteOrder.UpdateTime < trades[j].ExiteOrder.UpdateTime
		})

		totalProfit := float64(0)
		dayProfit := float64(0)

		firstData := time.Unix((trades[0].ExiteOrder.UpdateTime / int64(time.Microsecond)), 0)

		previousDay := firstData.Format("02-01-06")

		currentDay := ""

		res := fmt.Sprintf("Day %v \n", previousDay)

		_, err = resWriter.Write([]byte(res))
		if err != nil {
			log.Printf("GetProfitStatictics() resWriter.Write(firstDay) err: %v\n", err)
		}

		for i, trade := range trades {
			tm := time.Unix((trade.ExiteOrder.UpdateTime / int64(time.Microsecond)), 0)

			currentDay = tm.Format("02-01-06")

			if currentDay != previousDay {
				res = fmt.Sprintf("Day profit: %.2f\n\n", dayProfit)

				_, err = resWriter.Write([]byte(res))
				if err != nil {
					log.Printf("GetProfitStatictics() resWriter.Write(totalProfit) err: %v\n", err)
				}

				res = fmt.Sprintf("Day %v \n", currentDay)

				_, err = resWriter.Write([]byte(res))
				if err != nil {
					log.Printf("GetProfitStatictics() resWriter.Write(currentDay) err: %v\n", err)
				}

				previousDay = currentDay

				dayProfit = 0
			}

			totalProfit += trade.Profit
			dayProfit += trade.Profit

			res = fmt.Sprintf("%v quantity:%s profit: %.2f\n", tm.Format("02-01-06 15:04"), trade.ExiteOrder.ExecutedQuantity[:6], trade.Profit)

			_, err = resWriter.Write([]byte(res))
			if err != nil {
				log.Printf("GetProfitStatictics() resWriter.Write(trade) err: %v\n", err)
			}

			if i == len(trades)-1 {
				res = fmt.Sprintf("Day profit: %.2f\n", dayProfit)

				_, err = resWriter.Write([]byte(res))
				if err != nil {
					log.Printf("GetProfitStatictics() resWriter.Write(dayProfit) err: %v\n", err)
				}
			}
		}

		_, err = resWriter.Write([]byte("\n"))
		if err != nil {
			log.Printf("GetProfitStatictics() resWriter.Write(totalProfit) err: %v\n", err)
		}

		res = fmt.Sprintf("Total profit: %.2f\n", totalProfit)

		_, err = resWriter.Write([]byte(res))
		if err != nil {
			log.Printf("GetProfitStatictics() resWriter.Write(totalProfit) err: %v\n", err)
		}
	}
}

func (o *Bot) retryCreateOrder(order *Order) (*binance.CreateOrderResponse, *Order, error) {
	firstErrTime := time.Now().Unix()

	for time.Now().Unix()-firstErrTime <= 3 {
		time.Sleep(time.Second * 1)

		depth, err := o.client.NewDepthService().Symbol(order.Symbol).
			Do(context.Background())

		if err != nil {
			log.Printf("o.client.NewDepthService(%v) err: %v\n", order.Symbol, err)

			continue
		}

		price, err := strconv.ParseFloat(depth.Bids[0].Price, 32)
		if err != nil {
			log.Printf("strconv.ParseFloat(depth.Bids[0].Price) err: %v\n", err)

			continue
		}

		if price-order.Price > 10 {
			log.Printf("newPrice: %v > oldPrice: %v\n", price, order.Price)

			continue
		}

		order.Price = price

		firstOrderResolve, err := o.CreateOrder(order, binance.SideTypeBuy, binance.OrderTypeLimitMaker, binance.TimeInForceTypeGTC)
		if err != nil {
			log.Printf("An error occurred during order execution, order: %v, err: %v\n", order, err)

			continue
		}

		return firstOrderResolve, order, nil
	}

	return nil, nil, fmt.Errorf("number of attempts to create an order %v exceeded", order)
}

func (o *Bot) getStopPrice() {
	last24Hours := time.Now().UnixNano()/(int64(time.Millisecond)/int64(time.Nanosecond)) - int64(1000*24*60*60)

	klines, err := o.client.NewKlinesService().Symbol(o.Symbol).StartTime(last24Hours).Interval("15m").Do(context.Background())
	if err != nil {
		log.Printf("o.client.NewKlinesService(%v) err: %v\n", o.Symbol, err)
		return
	}

	minPrice := float64(0)
	maxPrice := float64(0)

	for _, kline := range klines {
		priceHigh, err := strconv.ParseFloat(kline.High, 32)
		if err != nil {
			log.Printf("getStopPrice() strconv.ParseFloat(%v, 32)) err: %v\n", kline.High, err)
			return
		}

		priceLow, err := strconv.ParseFloat(kline.Low, 32)
		if err != nil {
			log.Printf("getStopPrice() strconv.ParseFloat(%v, 32)) err: %v\n", kline.Low, err)
			return
		}

		if minPrice == 0 && priceLow != 0 {
			minPrice = priceLow
		}

		if priceHigh > maxPrice {
			maxPrice = priceHigh
		}

		if priceLow < minPrice {
			minPrice = priceLow
		}
	}

	stopPrice := maxPrice - ((maxPrice - minPrice) * 0.3)

	o.rwm.Lock()
	o.stopPrice = stopPrice
	o.rwm.Unlock()

	log.Printf("new stop price: %v\n", stopPrice)
}
