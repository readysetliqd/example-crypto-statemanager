package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/joho/godotenv"
	ks "github.com/readysetliqd/crypto-exchange-library-go/pkg/krakenspot"
	"github.com/readysetliqd/crypto-exchange-library-go/pkg/statemanager"
	"github.com/shopspring/decimal"
)

const (
	constMaxBal        = 1          // max inventory
	interval           = uint16(15) // minutes
	baseAsset          = "XXBT"
	pair               = "XBT/USD"
	constOrderMin      = 0.0001
	pairDecimals       = 1
	strategyID         = 100
	sellOrder          = 101
	buyOrder           = 102
	constOrderDist     = 2.0 / 100 // percent
	zeroBalStateName   = "zeroBalState"
	normalBalStateName = "normalBalState"
	maxedBalStateName  = "maxedBalState"
)

var maxBal = decimal.NewFromInt(constMaxBal)
var orderMin = decimal.NewFromFloat(constOrderMin)
var orderVolume = orderMin

var sellOrderDist = decimal.NewFromFloat(1 + constOrderDist)
var buyOrderDist = decimal.NewFromFloat(1 - constOrderDist)

var sellOrderStr = fmt.Sprintf("%v", sellOrder)
var buyOrderStr = fmt.Sprintf("%v", buyOrder)

type orderFill struct {
	userRefStr         string
	direction          string
	filled             bool
	partiallyFilled    bool
	partiallyFilledAmt decimal.Decimal
}

var fillMap = map[int32]*orderFill{
	sellOrder: &orderFill{userRefStr: sellOrderStr, filled: false, direction: "sell", partiallyFilled: false, partiallyFilledAmt: decimal.Zero},
	buyOrder:  &orderFill{userRefStr: buyOrderStr, filled: false, direction: "buy", partiallyFilled: false, partiallyFilledAmt: decimal.Zero},
}

type OrderPrices struct {
	sellPrice decimal.Decimal
	buyPrice  decimal.Decimal
}

type StrategyStates struct {
	kc             *ks.KrakenClient
	sm             *statemanager.StateManager
	logger         *log.Logger
	zeroBalState   *ZeroBalState
	normalBalState *NormalBalState
	maxedBalState  *MaxedBalState
}

type BaseBalState struct {
	*statemanager.DefaultState
	kc *ks.KrakenClient
	sm *statemanager.StateManager
	op *OrderPrices
}

type ZeroBalState struct {
	BaseBalState
}

type NormalBalState struct {
	BaseBalState
}

type MaxedBalState struct {
	BaseBalState
}

// Custom Enter() function overwrites (statemanager.DefaultState) Enter()
func (s *ZeroBalState) Enter(prevState statemanager.State) {
	log.Println("Entering ZeroBalState")
	switch prevState.(type) { // determine which state type was the previous state
	case *NormalBalState: // switching from normal balance state, don't need to cancel sells if only one order config, should just be filled
		log.Println("PrevState was NormalBalState")
	case *statemanager.InitialState: // only on program startup should prevState be *statemanager.InitialState, add buy order
		log.Println("PrevState was nil")
		s.kc.WSAddOrder(ks.WSLimit(s.op.buyPrice.String()), "buy", orderVolume.String(), pair, ks.WSUserRef(buyOrderStr), ks.WSPostOnly())
	default: // we should never reach this if the code is correct
		log.Fatalf("entering from undefined prevState type: %v\n", prevState)
	}
}

// Custom Enter() function overwrites (statemanager.DefaultState) Enter()
func (s *NormalBalState) Enter(prevState statemanager.State) {
	log.Println("Entering NormalBalState")
	switch prevState.(type) { // determine which state type was the previous state
	case *ZeroBalState: // coming from zero balance, add the missing sell order
		log.Println("PrevState was ZeroBalState")
		s.kc.WSAddOrder(ks.WSLimit(s.op.sellPrice.String()), "sell", orderVolume.String(), pair, ks.WSUserRef(sellOrderStr), ks.WSPostOnly())
	case *MaxedBalState: // coming from max balance, add the missing buy order
		log.Println("PrevState was MaxedBalState")
		s.kc.WSAddOrder(ks.WSLimit(s.op.buyPrice.String()), "buy", orderVolume.String(), pair, ks.WSUserRef(buyOrderStr), ks.WSPostOnly())
	case *statemanager.InitialState: // only on program startup should prevState be *statemanager.InitialState, add both buy and sell orders
		log.Println("PrevState was nil")
		s.kc.WSAddOrder(ks.WSLimit(s.op.sellPrice.String()), "sell", orderVolume.String(), pair, ks.WSUserRef(sellOrderStr), ks.WSPostOnly())
		s.kc.WSAddOrder(ks.WSLimit(s.op.buyPrice.String()), "buy", orderVolume.String(), pair, ks.WSUserRef(buyOrderStr), ks.WSPostOnly())
	default: // we should never reach this if the code is correct
		log.Fatalf("entering from undefined prevState type: %v\n", prevState)
	}
}

// Custom Enter() function overwrites (statemanager.DefaultState) Enter()
func (s *MaxedBalState) Enter(prevState statemanager.State) {
	log.Println("Entering MaxedBalState")
	switch prevState.(type) { // determine which state type was the previous state
	case *NormalBalState: // entering from normal state, cancel bids but leave the asks
		log.Println("PrevState was NormalBalState")
		s.kc.WSCancelOrder(buyOrderStr)
	case *statemanager.InitialState: // entering from *statemanager.InitialState state (always only on startup) place asks
		log.Println("PrevState was nil")
		s.kc.WSAddOrder(ks.WSLimit(s.op.sellPrice.String()), "sell", orderVolume.String(), pair, ks.WSUserRef(sellOrderStr), ks.WSPostOnly())
	default: // we should never reach this if the code is correct
		log.Fatalf("entering from undefined prevState type: %v\n", prevState)
	}
}

// Custom HandleEvent() function overwrites (statemanager.DefaultState) HandleEvent()
func (s *ZeroBalState) HandleEvent(ctx context.Context, event statemanager.Event, responseChan chan interface{}) error {
	switch e := event.(type) { // determine what type of event is coming through the event channel
	case *newCandle: // new 15 minute candle close, replace or move the bid
		replaceOrEdit(s.kc, buyOrder, e.buyPrice.String())
	case *newTrade: // new trade confirmation, check balance and change state if necessary
		// get balance from internal balance manager
		bal, err := s.kc.AssetBalance(baseAsset)
		if err != nil {
			return err
		}
		// check balance size
		if bal.Cmp(orderVolume) > -1 {
			// change state to normalBalState
			nextState, err := s.sm.GetState(normalBalStateName)
			if err != nil {
				return err
			}
			s.sm.SetState(nextState)
		}
	}
	return nil
}

// Custom HandleEvent() function overwrites (statemanager.DefaultState) HandleEvent()
func (s *NormalBalState) HandleEvent(ctx context.Context, event statemanager.Event, responseChan chan interface{}) error {
	switch e := event.(type) { // determine what type of event is coming through the event channel
	case *newCandle: // new 15 minute candle close, replace or move the bid
		replaceOrEdit(s.kc, sellOrder, e.sellPrice.String())
		replaceOrEdit(s.kc, buyOrder, e.buyPrice.String())
	case *newTrade: // new trade confirmation, check balance and change state if necessary
		// get balance from internal balance manager
		bal, err := s.kc.AssetBalance(baseAsset)
		if err != nil {
			return err
		}
		// check balance size
		if bal.Cmp(orderVolume) == -1 {
			// change state to zeroBalState
			nextState, err := s.sm.GetState(zeroBalStateName)
			if err != nil {
				return err
			}
			s.sm.SetState(nextState)
			// check balance size
		} else if bal.Cmp(maxBal) > -1 {
			// change state to maxedBalState
			nextState, err := s.sm.GetState(maxedBalStateName)
			if err != nil {
				return err
			}
			s.sm.SetState(nextState)
		}
	}
	return nil
}

// Custom HandleEvent() function overwrites (statemanager.DefaultState) HandleEvent()
func (s *MaxedBalState) HandleEvent(ctx context.Context, event statemanager.Event, responseChan chan interface{}) error {
	switch e := event.(type) { // determine what type of event is coming through the event channel
	case *newCandle: // new 15 minute candle close, replace or move the bid
		replaceOrEdit(s.kc, sellOrder, e.sellPrice.String())
	case *newTrade: // new trade confirmation, check balance and change state if necessary
		// get balance from internal balance manager
		bal, err := s.kc.AssetBalance(baseAsset)
		if err != nil {
			return err
		}
		// check balance size
		if bal.Cmp(maxBal) == -1 {
			// change state to normalBalState
			nextState, err := s.sm.GetState(normalBalStateName)
			if err != nil {
				return err
			}
			s.sm.SetState(nextState)
		}
	}
	return nil
}

// custom event type
type newCandle struct {
	*statemanager.DefaultEvent // embed DefaultEvent
	closeStr                   string
	close                      decimal.Decimal
	sellPrice                  decimal.Decimal
	buyPrice                   decimal.Decimal
}

// Custom Process() method to overwrite embedded DefaultEvent.Process()
func (c *newCandle) Process(ctx context.Context) error {
	// convert incoming close price (string type from Kraken's WebSocket server) to decimal
	close, err := decimal.NewFromString(c.closeStr)
	if err != nil {
		return err
	}
	c.close = close
	// calculate and assign new sell price and buy price by multiplying by predefined distance "constants"
	// round to nearest pairDecimals so invalid price is not sent to exchange
	c.sellPrice = c.close.Mul(sellOrderDist).Round(pairDecimals)
	c.buyPrice = c.close.Mul(buyOrderDist).Round(pairDecimals)
	return nil
}

// New custom event type, just need to embed DefaultEvent to match interface,
// but no need to change/overwrite methods as we only use it's type to signal
type newTrade struct {
	*statemanager.DefaultEvent
}

func main() {
	// Load and initialize Kraken Client and apikeys.env
	err := godotenv.Load("apikeys.env")
	if err != nil {
		log.Fatal("Error loading .env file |", err)
	}
	kc, err := ks.NewKrakenClient(os.Getenv("KRAKEN_API_KEY"), os.Getenv("KRAKEN_API_SECRET"), 2)
	if err != nil {
		log.Fatal("error creating NewKrakenClient |", err)
	}

	// Open and start loggers
	file, err := os.OpenFile("errors.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Fatal("error opening errors file |", err)
	}
	defer file.Close()
	logger := kc.SetErrorLogger(file)
	err = kc.StartTradeLogger("trades.jsonl")
	if err != nil {
		logger.Println("error starting trade logger |", err)
	}
	defer kc.StopTradeLogger()

	// Start required features
	err = kc.StartBalanceManager()
	if err != nil {
		logger.Fatal("error starting balance manager |", err)
	}
	sms := statemanager.StartStateManagement()
	err = kc.StartOpenOrderManager()
	if err != nil {
		logger.Fatal("error starting open order manager |", err)
	}

	// Shutdown routine in case of interrupt or certain event errors occur
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		cleanup(kc, logger, file)
		// shutdown after cleanup
		os.Exit(0)
	}()

	// defer function to attempt graceful shutdown/cleanup in event of panic
	defer func() {
		if r := recover(); r != nil {
			logger.Println("Recovered from panic:", r)
			cleanup(kc, logger, file)
			panic(r) // re-throw panic after clean-up
		}
	}()

	//initialize orderPrices
	orderPrices := &OrderPrices{}

	// Initialize state manager with unique strategyID
	// we pass statemanager.WithRunTypeNoUpdate() to use less compute since we
	// didn't write a custom Update method for any state
	sm := sms.NewStateManager(strategyID, statemanager.WithRunTypeNoUpdate())

	// constructor function for BaseBalState with common required fields for all states
	baseState := func() BaseBalState {
		return BaseBalState{
			kc: kc,
			sm: sm,
			op: orderPrices,
		}
	}

	// Initialize states and Add their names to state manager (necessary for
	// GetState method calls in custom HandleEvent methods)
	ss := &StrategyStates{
		kc:     kc,
		sm:     sm,
		logger: logger,
		zeroBalState: &ZeroBalState{
			BaseBalState: baseState(),
		},
		normalBalState: &NormalBalState{
			BaseBalState: baseState(),
		},
		maxedBalState: &MaxedBalState{
			BaseBalState: baseState(),
		},
	}
	sm.AddState(zeroBalStateName, ss.zeroBalState)
	sm.AddState(normalBalStateName, ss.normalBalState)
	sm.AddState(maxedBalStateName, ss.maxedBalState)

	// log a message confirming system is online, otherwise attempt cleanup and
	// shut down. System status callbacks will be called any time system status
	// type messages are received from Kraken's WebSocket server. A message *should*
	// be sent every time system status changes, but in practice sometimes they
	// don't. Best not to rely on these if you don't have to
	systemStatusCallback := func(status string) {
		if status == "online" {
			logger.Println("system online; connected")
			return
		} else { // system not online
			cleanup(kc, logger, file)
			logger.Fatal("system not online; status:", status)
		}
	}

	// Connect to Kraken WebSocket server authenticated and public and wait for confirmation
	err = kc.Connect(systemStatusCallback)
	if err != nil {
		logger.Fatal("error connecting |", err)
	}
	err = kc.WaitForConnect()
	if err != nil {
		logger.Fatal("error calling WaitForConnect |", err)
	}

	// sets the ordersStatusCallback function, these will be called every time an
	// "orderStatus" type message is received. These come in after sending a
	// request over WebSocket to edit, add, or cancel an order. By no means
	// is this error handling exhaustive, only meant to show we can do different
	// things depeneding on type of message and its contents. In this case, we're
	// just calling interrupt and shutting down gracefully on all errors
	orderStatusCallback := func(data interface{}) {
		switch msg := data.(type) {
		case ks.WSEditOrderResp:
			if msg.Status == "error" {
				if strings.Contains(msg.ErrorMessage, "Invalid arguments") { // code is likely incorrect
					logger.Println("error editing order with invalid arguments; shutting down | ", msg.ErrorMessage)
					sigs <- os.Interrupt
				} else {
					logger.Println("some other error editing order; shutting down | ", msg.ErrorMessage)
					sigs <- os.Interrupt
				}
			}
		case ks.WSCancelOrderResp:
			if msg.Status == "error" {
				logger.Println("error canceling order; shutting down | ", msg.ErrorMessage)
				sigs <- os.Interrupt
			}
		case ks.WSAddOrderResp:
			if msg.Status == "error" {
				logger.Println("error adding order; shutting down | ", msg.ErrorMessage)
				sigs <- os.Interrupt
			}
		}
	}
	kc.SetOrderStatusCallback(orderStatusCallback)

	//// Callbacks for channels and handling events
	// "ohlc" WebSocket channel
	firstMessageReceived := false
	var lastTimeStamp string
	ohlcCallback := func(data interface{}) {
		msg, ok := data.(ks.WSOHLCResp)
		if !ok {
			logger.Println("unknown data type sent to ohlcCallback; shutting down")
			sigs <- os.Interrupt
		}
		log.Println(msg)
		// create a newCandle event with close
		newClose := &newCandle{
			closeStr: msg.OHLC.Close,
		}

		// Call custom process method to convert close price to decimal.Decimal
		// and calculate order prices
		err = newClose.Process(context.TODO())
		if err != nil {
			logger.Println("error processing newClose event; shutting down")
			sigs <- os.Interrupt
		}

		// Copy order prices from event to orderPrices struct which all states
		// have access to
		orderPrices.CopyPrices(newClose)

		// Don't signal newCandle event on first message as we don't want to
		// the statemanager to attempt to update orders before setting the initial
		// state at the bottom of this program
		// Check if ohlc end time is different than the last ohlc timestamp signalling
		// a new candle and send event. Hacky way to go about it since the server
		// doesn't send a message exactly on the interval change, a better
		// implementation would be to use custom state.Update() methods to
		// check the time instead.
		if firstMessageReceived && lastTimeStamp != msg.OHLC.EndTime {
			sm.SendEvent(newClose)
			lastTimeStamp = msg.OHLC.EndTime
		} else {
			lastTimeStamp = msg.OHLC.EndTime
			firstMessageReceived = true
		}
	}

	// "ownTrades" WebSocket channel receives a message every time a new trade
	// is confirmed
	ownTradesCallback := func(data interface{}) {
		msg, ok := data.(ks.WSOwnTradesResp)
		if !ok {
			logger.Println("error asserting data to WSOwnTradesResp")
		}
		for _, trades := range msg.OwnTrades {
			for _, trade := range trades {
				if fill, ok := fillMap[trade.UserRef]; ok { // ignores orders that aren't in this program
					vol, err := decimal.NewFromString(trade.Volume)
					if err != nil {
						logger.Printf("error converting volume string to decimal; shutting down | volume: %s\n", trade.Volume)
						sigs <- os.Interrupt
					} else {
						if vol.Cmp(orderVolume) == -1 { // partial order fill
							fill.partiallyFilledAmt.Add(vol)
							if fill.partiallyFilledAmt.Cmp(orderVolume) == -1 {
								fill.partiallyFilled = true
							} else { // partial fill closed order, reset partial fill fields
								fill.partiallyFilled = false
								fill.partiallyFilledAmt = decimal.Zero
								fill.filled = true
							}
						} else { // order filled in one trade
							fill.filled = true
						}
					}
				}
			}
		}
		trade := &newTrade{}
		sm.SendEvent(trade)
	}

	// Subscribe required channels
	err = kc.SubscribeOwnTrades(ownTradesCallback, ks.WithoutSnapshot())
	if err != nil {
		logger.Fatal("error calling SubscribeOwnTrades |", err)
	}
	err = kc.SubscribeOpenOrders(nil, ks.WithRateCounter())
	if err != nil {
		logger.Fatal("error calling SubscribeOpenOrders |", err)
	}
	err = kc.SubscribeOHLC(pair, interval, ohlcCallback)
	if err != nil {
		logger.Fatal("error calling SubscribeOHLC |", err)
	}
	err = kc.WaitForSubscriptions()
	if err != nil {
		logger.Fatal("error waiting for subscriptions |", err)
	}

	// Determine initial state from current baseAsset balance and enter it
	time.Sleep(time.Millisecond * 1000) // hardcoded wait for first OHLC message received and processed
	ss.GetBalanceAndSetInitialState()

	// Check for disconnect every 100 seconds and replace orders
	go func() {
		kc.WaitForDisconnect()
		sm.Reset()
		kc.WaitForReconnect()
		kc.WaitForSubscriptions()
		sm.Restart()
		//wait for open orders manager to build new orders
		time.Sleep(time.Millisecond * 300)
		// Find and cancel any remaining open orders from before disconnect
		orders := kc.MapOpenOrders()
		var cancelOrdersQueue []string
		for id, order := range orders {
			// if order userref is in fillmap (order is from this program)
			if _, ok := fillMap[int32(order.UserRef)]; ok {
				// add to slice to be cancelled
				cancelOrdersQueue = append(cancelOrdersQueue, id)
			}
		}
		kc.WSCancelOrders(cancelOrdersQueue)
		ss.GetBalanceAndSetInitialState()
	}()

	// block indefinitely
	select {}
}

func (ss *StrategyStates) GetBalanceAndSetInitialState() {
	bal, err := ss.kc.AssetBalance(baseAsset)
	if err != nil {
		ss.logger.Fatal("error getting initial asset balance |", err)
	}
	if bal.Cmp(orderVolume) == -1 {
		ss.sm.SetState(ss.zeroBalState)
	} else if bal.Cmp(maxBal) == -1 {
		ss.sm.SetState(ss.normalBalState)
	} else {
		ss.sm.SetState(ss.maxedBalState)
	}
}

func replaceOrEdit(kc *ks.KrakenClient, userRef int32, price string) {
	if fillMap[userRef].filled {
		kc.WSAddOrder(ks.WSLimit(price), fillMap[userRef].direction, orderVolume.String(), pair, ks.WSUserRef(fillMap[userRef].userRefStr), ks.WSPostOnly())
		fillMap[userRef].filled = false
		fillMap[userRef].partiallyFilled = false
		fillMap[userRef].partiallyFilledAmt = decimal.Zero
	} else if fillMap[userRef].partiallyFilled {
		kc.WSCancelOrder(fillMap[userRef].userRefStr)
		kc.WSAddOrder(ks.WSLimit(price), fillMap[userRef].direction, orderVolume.String(), pair, ks.WSUserRef(fillMap[userRef].userRefStr), ks.WSPostOnly())
		fillMap[userRef].filled = false
		fillMap[userRef].partiallyFilled = false
		fillMap[userRef].partiallyFilledAmt = decimal.Zero
	} else {
		kc.WSEditOrder(fillMap[userRef].userRefStr, pair, ks.WSNewPrice(price), ks.WSNewPostOnly(), ks.WSNewUserRef(fillMap[userRef].userRefStr))
		fillMap[userRef].filled = false
		fillMap[userRef].partiallyFilled = false
		fillMap[userRef].partiallyFilledAmt = decimal.Zero
	}
}

func (orderPrices *OrderPrices) CopyPrices(newClose *newCandle) {
	orderPrices.sellPrice = newClose.sellPrice
	orderPrices.buyPrice = newClose.buyPrice
}

func cleanup(kc *ks.KrakenClient, logger *log.Logger, file *os.File) {
	kc.WSCancelOrders([]string{buyOrderStr, sellOrderStr})
	// hardcoded wait for order cancellation message from
	// kraken and update internal open orders manager for logging
	time.Sleep(time.Millisecond * 300)
	err := kc.StopTradeLogger()
	if err != nil {
		logger.Println("error stopping trade logger:", err)
	}
	err = kc.LogOpenOrders("open_orders.jsonl", true)
	if err != nil {
		logger.Println("error logging orders:", err)
	}
	err = kc.UnsubscribeAll()
	if err != nil {
		logger.Println("error calling UnsubscribeAll")
	}
	file.Close()
}
