# example-crypto-statemanager
Example project showing usage of crypto-exchange-library-go packages "krakenspot" and "statemanager"

# Disclaimer
Imported packages showcased in this project are highly experimental and very likely to change in form and function in the future. 
Strategy used here is not a valid trading strategy and again, is only here for the purposes of showcasing potential package usage.
This was a briefly put together example and has not been tested thoroughly for logical bugs, edge cases, and/or proper error handling
Following the instructions here and/or using any part of this project trades with real money live from your cryptocurrency exchange account potentially resulting in permanent loss of funds due to bugs, errors, variance, or any other unforseen circumstances. Use at your own risk, devs will not be held liable for any losses.

# Instructions
- Remove .example file type from apikeys.env
- Enter api-key and secret and save
- (optional) Change constants as desired
    - constMaxBal is the number of coins in which the program will enter, if decimal is required, must change var maxBal as well 
    - interval is the timeframe in minutes that orders will update, check Kraken OHLC docs for enums
    - pair must be a valid tradable websocket name for the pair and asset must be corresponding base asset for pair, default is XBT/USD (Bitcoin) asset XXBT
    - buyOrder and sellOrder numbers are the user reference numbers passed to the exhcange; can be any int32, must be unique
    - constOrderDist is the percent order distance or spread for how far away from candle close orders will be placed, expressed in decimals
    - state names can be any string, must be unique
    - constOrderMin and pairDecimals are determined by the exchange, you can find these from calling krakenspot.GetTradableAssetPairsInfo()
    - orderVolume is the size of the orders, set to orderMin by default

# Description
This project initializes a state manager and client to interact with Kraken's WebSocket server. The state manager tracks the current balance of the asset and places orders on either side of the candle closes. 

The program updates the orders to new prices (same distance away) every time a new candle closes (based on a new message from Kraken's OHLC WebSocket channel, in practice it can be delayed some seconds after candle closes depending on actual trade activity on the exchange). When balance exceeds the specified max balance, it will cancel and stop placing bids. When balance goes below the specified order size, the program cancels and stops placing asks. 

The program continues this indefinitely until user stops the program or Kraken's exchange system status changes.