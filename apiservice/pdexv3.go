package apiservice

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"

	"github.com/gin-gonic/gin"
	"github.com/incognitochain/coin-service/database"
	"github.com/incognitochain/coin-service/pdexv3/analyticsquery"
	"github.com/incognitochain/coin-service/pdexv3/feeestimator"
	"github.com/incognitochain/coin-service/pdexv3/pathfinder"
	"github.com/incognitochain/coin-service/shared"
	"github.com/incognitochain/incognito-chain/common"
	"github.com/incognitochain/incognito-chain/metadata"
)

type pdexv3 struct{}

func (pdexv3) ListPairs(c *gin.Context) {
	var result []PdexV3PairData
	list, err := database.DBGetPdexPairs()
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	for _, v := range list {
		data := PdexV3PairData{
			PairID:       v.PairID,
			TokenID1:     v.TokenID1,
			TokenID2:     v.TokenID2,
			Token1Amount: v.Token1Amount,
			Token2Amount: v.Token2Amount,
			PoolCount:    v.PoolCount,
		}
		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) ListPools(c *gin.Context) {
	pair := c.Query("pair")
	list, err := database.DBGetPoolPairsByPairID(pair)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	var defaultPools map[string]struct{}
	if err := cacheGet(defaultPoolsKey, defaultPools); err != nil {
		defaultPools, err = database.DBGetDefaultPool()
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		err = cacheStore(defaultPoolsKey, defaultPools)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	}
	// Get pool pair rate changes
	poolIds := make([]string, 0)
	for _, v := range list {
		poolIds = append(poolIds, v.PoolID)
	}
	poolLiquidityChanges, err := analyticsquery.APIGetPDexV3PairRateChangesAndVolume24h(poolIds)

	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3PoolDetail
	for _, v := range list {
		data := PdexV3PoolDetail{
			PoolID:         v.PoolID,
			Token1ID:       v.TokenID1,
			Token2ID:       v.TokenID2,
			Token1Value:    v.Token1Amount,
			Token2Value:    v.Token2Amount,
			Virtual1Value:  v.Virtual1Amount,
			Virtual2Value:  v.Virtual2Amount,
			Volume:         0,
			PriceChange24h: 0,
			AMP:            v.AMP,
			Price:          float64(v.Token1Amount) / float64(v.Token2Amount),
			TotalShare:     v.TotalShare,
		}

		//TODO @yenle add pool volume and price change 24h
		// data.APY

		if poolChange, found := poolLiquidityChanges[v.PoolID]; found {
			data.PriceChange24h = poolChange.RateChangePercentage
			data.Volume = poolChange.TradingVolume24h
		}
		if _, found := defaultPools[v.PoolID]; found {
			data.IsVerify = true
		}

		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) TradeStatus(c *gin.Context) {
	requestTx := c.Query("requesttx")
	tradeInfo, tradeStatus, err := database.DBGetTradeInfoAndStatus(requestTx)
	if err != nil && tradeInfo == nil {
		errStr := err.Error()
		respond := APIRespond{
			Result: nil,
			Error:  &errStr,
		}
		c.JSON(http.StatusOK, respond)
		return
	}
	matchedAmount, sellTokenBl, buyTokenBl, sellTokenWD, buyTokenWD, statusCode, status, withdrawTxs, isCompleted, err := getTradeStatus(tradeInfo, tradeStatus)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	result := TradeDataRespond{
		RequestTx:           tradeInfo.RequestTx,
		RespondTxs:          tradeInfo.RespondTxs,
		WithdrawTxs:         withdrawTxs,
		PoolID:              tradeInfo.PoolID,
		PairID:              tradeInfo.PairID,
		SellTokenID:         tradeInfo.SellTokenID,
		BuyTokenID:          tradeInfo.BuyTokenID,
		Amount:              tradeInfo.Amount,
		MinAccept:           tradeInfo.MinAccept,
		Matched:             matchedAmount,
		Status:              status,
		StatusCode:          statusCode,
		Requestime:          tradeInfo.Requesttime,
		NFTID:               tradeInfo.NFTID,
		Fee:                 tradeInfo.Fee,
		FeeToken:            tradeInfo.FeeToken,
		Receiver:            tradeInfo.Receiver,
		IsCompleted:         isCompleted,
		SellTokenBalance:    sellTokenBl,
		BuyTokenBalance:     buyTokenBl,
		SellTokenWithdrawed: sellTokenWD,
		BuyTokenWithdrawed:  buyTokenWD,
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PoolShare(c *gin.Context) {
	nftID := c.Query("nftid")
	list, err := database.DBGetShare(nftID)
	if err != nil {
		errStr := err.Error()
		respond := APIRespond{
			Result: nil,
			Error:  &errStr,
		}
		c.JSON(http.StatusOK, respond)
		return
	}
	var result []PdexV3PoolShareRespond
	for _, v := range list {
		l, err := database.DBGetPoolPairsByPoolID([]string{v.PoolID})
		if err != nil {
			errStr := err.Error()
			respond := APIRespond{
				Result: nil,
				Error:  &errStr,
			}
			c.JSON(http.StatusOK, respond)
			return
		}
		if len(l) == 0 {
			continue
		}
		tk1Reward := uint64(0)
		tk2Reward := uint64(0)
		prvReward := uint64(0)
		if rw, ok := v.TradingFee[l[0].TokenID1]; ok {
			tk1Reward = rw
		}
		if rw, ok := v.TradingFee[l[0].TokenID2]; ok {
			tk2Reward = rw
		}

		if rw, ok := v.TradingFee[common.PRVCoinID.String()]; ok {
			prvReward = rw
		}
		result = append(result, PdexV3PoolShareRespond{
			PoolID:       v.PoolID,
			Share:        v.Amount,
			Token1Reward: tk1Reward,
			Token2Reward: tk2Reward,
			PRVReward:    prvReward,
			AMP:          l[0].AMP,
			TokenID1:     l[0].TokenID1,
			TokenID2:     l[0].TokenID2,
			Token1Amount: l[0].Token1Amount,
			Token2Amount: l[0].Token2Amount,
			TotalShare:   l[0].TotalShare,
		})
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) TradeHistory(c *gin.Context) {
	startTime := time.Now()
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	otakey := c.Query("otakey")
	poolid := c.Query("poolid")
	nftid := c.Query("nftid")

	if poolid == "" {
		pubkey, err := extractPubkeyFromKey(otakey, true)
		if err != nil {
			errStr := err.Error()
			respond := APIRespond{
				Result: nil,
				Error:  &errStr,
			}
			c.JSON(http.StatusOK, respond)
			return
		}
		fmt.Println("pubkey, metadata.Pdexv3TradeRequestMeta", pubkey, metadata.Pdexv3TradeRequestMeta)
		txList, err := database.DBGetTxByMetaAndOTA(pubkey, metadata.Pdexv3TradeRequestMeta, int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		txRequest := []string{}
		for _, tx := range txList {
			txRequest = append(txRequest, tx.TxHash)
		}
		var result []TradeDataRespond
		list, err := database.DBGetTxTradeFromTxRequest(txRequest)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		for _, tradeInfo := range list {

			matchedAmount := uint64(0)
			status := ""
			isCompleted := false
			switch tradeInfo.Status {
			case 0:
				status = "pending"
			case 1:
				status = "accepted"
				matchedAmount = tradeInfo.Amount
				isCompleted = true
			case 2:
				status = "rejected"
			}
			trade := TradeDataRespond{
				RequestTx:   tradeInfo.RequestTx,
				RespondTxs:  tradeInfo.RespondTxs,
				WithdrawTxs: nil,
				PoolID:      tradeInfo.PoolID,
				PairID:      tradeInfo.PairID,
				SellTokenID: tradeInfo.SellTokenID,
				BuyTokenID:  tradeInfo.BuyTokenID,
				Amount:      tradeInfo.Amount,
				MinAccept:   tradeInfo.MinAccept,
				Matched:     matchedAmount,
				Status:      status,
				StatusCode:  tradeInfo.Status,
				Requestime:  tradeInfo.Requesttime,
				NFTID:       tradeInfo.NFTID,
				Fee:         tradeInfo.Fee,
				FeeToken:    tradeInfo.FeeToken,
				Receiver:    tradeInfo.Receiver,
				IsCompleted: isCompleted,
				TradingPath: tradeInfo.TradingPath,
			}
			result = append(result, trade)
		}
		respond := APIRespond{
			Result: result,
			Error:  nil,
		}
		log.Println("APIGetTradeHistory time:", time.Since(startTime))
		c.JSON(http.StatusOK, respond)
	} else {
		//limit order
		tradeList, err := database.DBGetTxTradeFromPoolAndNFT(poolid, nftid, int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		txRequest := []string{}
		for _, tx := range tradeList {
			txRequest = append(txRequest, tx.RequestTx)
		}
		tradeStatusList, err := database.DBGetTradeStatus(txRequest)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		var result []TradeDataRespond
		for _, tradeInfo := range tradeList {
			matchedAmount := uint64(0)
			var tradeStatus *shared.LimitOrderStatus
			if t, ok := tradeStatusList[tradeInfo.RequestTx]; ok {
				tradeStatus = &t
			}
			matchedAmount, sellTokenBl, buyTokenBl, sellTokenWD, buyTokenWD, statusCode, status, withdrawTxs, isCompleted, err := getTradeStatus(&tradeInfo, tradeStatus)
			if err != nil {
				c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
				return
			}
			trade := TradeDataRespond{
				RequestTx:           tradeInfo.RequestTx,
				RespondTxs:          tradeInfo.RespondTxs,
				WithdrawTxs:         withdrawTxs,
				PoolID:              tradeInfo.PoolID,
				PairID:              tradeInfo.PairID,
				SellTokenID:         tradeInfo.SellTokenID,
				BuyTokenID:          tradeInfo.BuyTokenID,
				Amount:              tradeInfo.Amount,
				MinAccept:           tradeInfo.MinAccept,
				Matched:             matchedAmount,
				Status:              status,
				StatusCode:          statusCode,
				Requestime:          tradeInfo.Requesttime,
				NFTID:               tradeInfo.NFTID,
				Fee:                 tradeInfo.Fee,
				FeeToken:            tradeInfo.FeeToken,
				Receiver:            tradeInfo.Receiver,
				IsCompleted:         isCompleted,
				SellTokenBalance:    sellTokenBl,
				BuyTokenBalance:     buyTokenBl,
				SellTokenWithdrawed: sellTokenWD,
				BuyTokenWithdrawed:  buyTokenWD,
			}
			result = append(result, trade)
		}
		respond := APIRespond{
			Result: result,
			Error:  nil,
		}
		log.Println("APIGetTradeHistory time:", time.Since(startTime))
		c.JSON(http.StatusOK, respond)
	}
}

func (pdexv3) ContributeHistory(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	poolID := c.Query("poolid")
	nftID := c.Query("nftid")
	var err error
	var list []shared.ContributionData
	if poolID != "" {

	} else {
		list, err = database.DBGetPDEV3ContributeRespond(nftID, int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	}

	var result []PdexV3ContributionData

	for _, v := range list {
		ctrbAmount := []uint64{}
		ctrbToken := []string{}
		if len(v.RequestTxs) > len(v.ContributeAmount) {
			ctrbAmount = append(ctrbAmount, v.ContributeAmount[0])
			ctrbAmount = append(ctrbAmount, v.ContributeAmount[0])
		} else {
			ctrbAmount = v.ContributeAmount
		}
		if len(v.RequestTxs) > len(v.ContributeTokens) {
			ctrbToken = append(ctrbToken, v.ContributeTokens[0])
			ctrbToken = append(ctrbToken, v.ContributeTokens[0])
		} else {
			ctrbToken = v.ContributeTokens
		}
		data := PdexV3ContributionData{
			RequestTxs:       v.RequestTxs,
			RespondTxs:       v.RespondTxs,
			ContributeTokens: ctrbToken,
			ContributeAmount: ctrbAmount,
			PairID:           v.PairID,
			PairHash:         v.PairHash,
			ReturnTokens:     v.ReturnTokens,
			ReturnAmount:     v.ReturnAmount,
			NFTID:            v.NFTID,
			RequestTime:      v.RequestTime,
			PoolID:           v.PoolID,
			Status:           "waiting",
		}
		if len(v.RequestTxs) == 2 && len(v.RespondTxs) == 0 {
			if v.ContributeTokens[0] != v.ContributeTokens[1] {
				data.Status = "completed"
			} else {
				data.Status = "refunding"
			}
		}
		if len(v.RespondTxs) > 0 {
			data.Status = "refunded"
		}
		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) WaitingLiquidity(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	// poolid := c.Query("poolid")
	nftid := c.Query("nftid")

	result, err := database.DBGetPDEV3ContributeWaiting(nftid, int64(limit), int64(offset))
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) WithdrawHistory(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	poolID := c.Query("poolid")
	nftID := c.Query("nftid")
	var result []PdexV3WithdrawRespond
	var err error
	var list []shared.WithdrawContributionData

	if poolID != "" {
		list, err = database.DBGetPDEV3WithdrawRespond(nftID, poolID, int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	} else {
		list, err = database.DBGetPDEV3WithdrawRespond(nftID, "", int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	}

	for _, v := range list {
		var token1, token2 string
		var amount1, amount2 uint64
		if len(v.RespondTxs) == 2 {
			token1 = v.WithdrawTokens[0]
			amount1 = v.WithdrawAmount[0]
			token2 = v.WithdrawTokens[1]
			amount2 = v.WithdrawAmount[1]
		}
		if len(v.RespondTxs) == 1 {
			token1 = v.WithdrawTokens[0]
			amount1 = v.WithdrawAmount[0]
		}
		result = append(result, PdexV3WithdrawRespond{
			PoolID:      v.PoolID,
			RequestTx:   v.RequestTx,
			RespondTxs:  v.RespondTxs,
			TokenID1:    token1,
			Amount1:     amount1,
			TokenID2:    token2,
			Amount2:     amount2,
			Status:      v.Status,
			ShareAmount: v.ShareAmount,
			Requestime:  v.RequestTime,
		})
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) WithdrawFeeHistory(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	poolID := c.Query("poolid")
	nftID := c.Query("nftid")
	var result []PdexV3WithdrawFeeRespond
	var err error
	var list []shared.WithdrawContributionFeeData

	if poolID != "" {
		list, err = database.DBGetPDEV3WithdrawFeeRespond(nftID, poolID, int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	} else {
		list, err = database.DBGetPDEV3WithdrawFeeRespond(nftID, "", int64(limit), int64(offset))
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	}

	for _, v := range list {
		var token1, token2 string
		var amount1, amount2 uint64
		if len(v.RespondTxs) == 2 {
			token1 = v.WithdrawTokens[0]
			amount1 = v.WithdrawAmount[0]
			token2 = v.WithdrawTokens[1]
			amount2 = v.WithdrawAmount[1]
		}
		if len(v.RespondTxs) == 1 {
			token1 = v.WithdrawTokens[0]
			amount1 = v.WithdrawAmount[0]
		}
		result = append(result, PdexV3WithdrawFeeRespond{
			PoolID:     v.PoodID,
			RequestTx:  v.RequestTx,
			RespondTxs: v.RespondTxs,
			TokenID1:   token1,
			Amount1:    amount1,
			TokenID2:   token2,
			Amount2:    amount2,
			Status:     v.Status,
			Requestime: v.RequestTime,
		})
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) StakingPool(c *gin.Context) {
	result, err := database.DBGetStakePools()
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) StakeInfo(c *gin.Context) {
	nftid := c.Query("nftid")
	result, err := database.DBGetStakingInfo(nftid)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) StakeHistory(c *gin.Context) {

	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	nftid := c.Query("nftid")
	tokenid := c.Query("tokenid")

	list, err := database.DBGetStakingPoolHistory(nftid, tokenid, int64(limit), int64(offset))
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	var result []PdexV3StakingPoolHistoryData
	for _, v := range list {
		data := PdexV3StakingPoolHistoryData{
			IsStaking:   v.IsStaking,
			RequestTx:   v.RequestTx,
			RespondTx:   v.RespondTx,
			Status:      v.Status,
			TokenID:     v.TokenID,
			NFTID:       v.NFTID,
			Amount:      v.Amount,
			Requesttime: v.Requesttime,
		}
		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) StakeRewardHistory(c *gin.Context) {
	offset, _ := strconv.Atoi(c.Query("offset"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	nftid := c.Query("nftid")
	tokenid := c.Query("tokenid")

	list, err := database.DBGetStakePoolRewardHistory(nftid, tokenid, int64(limit), int64(offset))
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	var result []PdexV3StakePoolRewardHistoryData
	for _, v := range list {
		data := PdexV3StakePoolRewardHistoryData{
			RespondTx:   v.RespondTx,
			RequestTx:   v.RequestTx,
			Status:      v.Status,
			TokenID:     v.TokenID,
			NFTID:       v.NFTID,
			Amount:      v.Amount,
			Requesttime: v.Requesttime,
		}
		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PairsDetail(c *gin.Context) {
	var req struct {
		PairIDs []string
	}
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	result, err := database.DBGetPairsByID(req.PairIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PoolsDetail(c *gin.Context) {
	var req struct {
		PoolIDs []string
	}
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	list, err := database.DBGetPoolPairsByPoolID(req.PoolIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	poolLiquidityChanges, err := analyticsquery.APIGetPDexV3PairRateChangesAndVolume24h(req.PoolIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3PoolDetail
	for _, v := range list {
		data := PdexV3PoolDetail{
			PoolID:         v.PoolID,
			Token1ID:       v.TokenID1,
			Token2ID:       v.TokenID2,
			Token1Value:    v.Token1Amount,
			Token2Value:    v.Token2Amount,
			Virtual1Value:  v.Virtual1Amount,
			Virtual2Value:  v.Virtual2Amount,
			PriceChange24h: 0,
			Volume:         0,
			AMP:            v.AMP,
			Price:          float64(v.Token1Amount) / float64(v.Token2Amount),
			TotalShare:     v.TotalShare,
		}

		//TODO @yenle add pool volume and price change 24h
		// data.APY

		if poolChange, found := poolLiquidityChanges[v.PoolID]; found {
			data.PriceChange24h = poolChange.RateChangePercentage
			data.Volume = poolChange.TradingVolume24h
		}

		result = append(result, data)
	}
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

const (
	decimal_10 = float64(10)
	decimal_1  = float64(1)
	decimal_01 = float64(0.1)
)

func (pdexv3) GetOrderBook(c *gin.Context) {
	decimal := c.Query("decimal")
	poolID := c.Query("poolid")

	decimalFloat, err := strconv.ParseFloat(decimal, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	if decimalFloat != decimal_10 && decimalFloat != decimal_1 && decimalFloat != decimal_01 {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(errors.New("wrong decimal")))
		return
	}
	tks := strings.Split(poolID, "-")
	pairID := tks[0] + "-" + tks[1]
	list, err := database.DBGetPendingOrderByPairID(pairID)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var result PdexV3OrderBookRespond
	var sellSide []shared.TradeOrderData
	var buySide []shared.TradeOrderData
	for _, v := range list {
		if v.SellTokenID == tks[0] {
			sellSide = append(sellSide, v)
		} else {
			buySide = append(buySide, v)
		}
	}

	sellVolume := make(map[string]PdexV3OrderBookVolume)
	buyVolume := make(map[string]PdexV3OrderBookVolume)

	for _, v := range sellSide {
		amount := float64(v.Amount) / float64(1e9) * decimalFloat * 10
		group := math.Floor(amount) / 10
		groupStr := fmt.Sprintf("%g", group)
		if d, ok := sellVolume[groupStr]; !ok {
			sellVolume[groupStr] = PdexV3OrderBookVolume{
				Price:  group,
				Volume: v.Amount,
			}
		} else {
			d.Volume += v.Amount
			sellVolume[groupStr] = d
		}
	}

	for _, v := range buySide {
		amount := float64(v.Amount) / float64(1e9) * decimalFloat * 10
		group := math.Floor(amount) / 10
		groupStr := fmt.Sprintf("%g", group)
		if d, ok := sellVolume[groupStr]; !ok {
			buyVolume[groupStr] = PdexV3OrderBookVolume{
				Price:  group,
				Volume: v.Amount,
			}
		} else {
			d.Volume += v.Amount
			buyVolume[groupStr] = d
		}
	}

	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) GetLatestTradeOrders(c *gin.Context) {
	isswap := c.Query("isswap")
	getSwap := false
	if isswap == "true" {
		getSwap = true
	}
	result, err := database.DBGetLatestTradeTx(getSwap)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) EstimateTrade(c *gin.Context) {
	var req struct {
		SellToken string `form:"selltoken" json:"selltoken" binding:"required"`
		BuyToken  string `form:"buytoken" json:"buytoken" binding:"required"`
		Amount    uint64 `form:"amount" json:"amount" binding:"required"`
		FeeInPRV  bool   `form:"feeinprv" json:"feeinprv"`
	}
	err := c.ShouldBindQuery(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	spew.Dump(req)
	sellToken := req.SellToken
	buyToken := req.BuyToken
	feeInPRV := req.FeeInPRV
	amount := req.Amount

	if amount < 0 {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(errors.New("invalid sell amount")))
		return
	}

	var result PdexV3EstimateTradeRespond

	pdexv3StateRPCResponse, err := pathfinder.GetPdexv3StateFromRPC()

	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(errors.New("can not get data from RPC pdexv3_getState")))
		return
	}

	pools, poolPairStates, err := pathfinder.GetPdexv3PoolDataFromRawRPCResult(pdexv3StateRPCResponse.Result.Poolpairs)

	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	pdexState, err := feeestimator.GetPdexv3PoolDataFromRawRPCResult(pdexv3StateRPCResponse.Result.Params, pdexv3StateRPCResponse.Result.Poolpairs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	chosenPath, receive := pathfinder.FindGoodTradePath(
		4,
		pools,
		poolPairStates,
		sellToken,
		buyToken,
		amount)

	spew.Dump("chosenPath", chosenPath)
	log.Printf("receive %d\n", receive)

	result.MaxGet = receive
	result.Route = make([]string, 0)
	if chosenPath != nil {
		for _, v := range chosenPath {
			result.Route = append(result.Route, v.PoolID)
		}
		//TODO: check pointer
		tradingFee, err := feeestimator.EstimateTradingFee(uint64(amount), sellToken, result.Route, *pdexState, feeInPRV)

		if err != nil {
			log.Print("can not estimate fee: ", err)
			c.JSON(http.StatusInternalServerError, buildGinErrorRespond(errors.New("can not estimate fee: "+err.Error())))
			return
		}
		result.Fee = tradingFee
	}

	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PriceHistory(c *gin.Context) {
	poolid := c.Query("poolid")
	period := c.Query("period")
	intervals := c.Query("intervals")

	analyticsData, err := analyticsquery.APIGetPDexV3PairRateHistories(poolid, period, intervals)

	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3PriceHistoryRespond

	for _, v := range analyticsData.Result {
		tm, _ := time.Parse(time.RFC3339, v.Timestamp)

		var pdexV3PriceHistoryRespond = PdexV3PriceHistoryRespond{
			Timestamp: tm.Unix(),
			High:      v.High,
			Low:       v.Low,
			Open:      v.Open,
			Close:     v.Close,
		}
		result = append(result, pdexV3PriceHistoryRespond)
	}

	respond := APIRespond{
		Result: result,
	}

	c.JSON(http.StatusOK, respond)
}

func (pdexv3) LiquidityHistory(c *gin.Context) {
	poolid := c.Query("poolid")
	period := c.Query("period")
	intervals := c.Query("intervals")

	analyticsData, err := analyticsquery.APIGetPDexV3PoolLiquidityHistories(poolid, period, intervals)

	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3LiquidityHistoryRespond

	for _, v := range analyticsData.Result {
		tm, _ := time.Parse(time.RFC3339, v.Timestamp)

		var pdexV3LiquidityHistoryRespond = PdexV3LiquidityHistoryRespond{
			Timestamp:           tm.Unix(),
			Token0RealAmount:    v.Token0RealAmount,
			Token1RealAmount:    v.Token1RealAmount,
			Token0VirtualAmount: v.Token0VirtualAmount,
			Token1VirtualAmount: v.Token1VirtualAmount,
			ShareAmount:         v.ShareAmount,
		}
		result = append(result, pdexV3LiquidityHistoryRespond)
	}

	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) TradeVolume24h(c *gin.Context) {
	pair := c.Query("pair")

	_ = pair

	analyticsData, err := analyticsquery.APIGetPDexV3TradingVolume24H(pair)

	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	respond := APIRespond{
		Result: struct {
			Value uint64
		}{
			Value: analyticsData.Result.Value,
		},
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) TradeDetail(c *gin.Context) {
	txhash := c.Query("txhash")

	tradeList, err := database.DBGetTxTradeFromTxRequest([]string{txhash})
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	txRequest := []string{txhash}
	tradeStatusList, err := database.DBGetTradeStatus(txRequest)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	var result []TradeDataRespond
	for _, tradeInfo := range tradeList {
		if tradeInfo.IsSwap {
			matchedAmount := uint64(0)
			status := ""
			isCompleted := false
			switch tradeInfo.Status {
			case 0:
				status = "pending"
			case 1:
				status = "accepted"
				matchedAmount = tradeInfo.Amount
				isCompleted = true
			case 2:
				status = "rejected"
			}

			trade := TradeDataRespond{
				RequestTx:   tradeInfo.RequestTx,
				RespondTxs:  tradeInfo.RespondTxs,
				WithdrawTxs: nil,
				PoolID:      tradeInfo.PoolID,
				PairID:      tradeInfo.PairID,
				SellTokenID: tradeInfo.SellTokenID,
				BuyTokenID:  tradeInfo.BuyTokenID,
				Amount:      tradeInfo.Amount,
				MinAccept:   tradeInfo.MinAccept,
				Matched:     matchedAmount,
				Status:      status,
				StatusCode:  tradeInfo.Status,
				Requestime:  tradeInfo.Requesttime,
				NFTID:       tradeInfo.NFTID,
				Fee:         tradeInfo.Fee,
				FeeToken:    tradeInfo.FeeToken,
				Receiver:    tradeInfo.Receiver,
				IsCompleted: isCompleted,
			}
			result = append(result, trade)
		} else {
			matchedAmount := uint64(0)
			var tradeStatus *shared.LimitOrderStatus
			if t, ok := tradeStatusList[tradeInfo.RequestTx]; ok {
				tradeStatus = &t
			}
			matchedAmount, sellTokenBl, buyTokenBl, sellTokenWD, buyTokenWD, statusCode, status, withdrawTxs, isCompleted, err := getTradeStatus(&tradeInfo, tradeStatus)
			if err != nil {
				c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
				return
			}
			trade := TradeDataRespond{
				RequestTx:           tradeInfo.RequestTx,
				RespondTxs:          tradeInfo.RespondTxs,
				WithdrawTxs:         withdrawTxs,
				PoolID:              tradeInfo.PoolID,
				PairID:              tradeInfo.PairID,
				SellTokenID:         tradeInfo.SellTokenID,
				BuyTokenID:          tradeInfo.BuyTokenID,
				Amount:              tradeInfo.Amount,
				MinAccept:           tradeInfo.MinAccept,
				Matched:             matchedAmount,
				Status:              status,
				StatusCode:          statusCode,
				Requestime:          tradeInfo.Requesttime,
				NFTID:               tradeInfo.NFTID,
				Fee:                 tradeInfo.Fee,
				FeeToken:            tradeInfo.FeeToken,
				Receiver:            tradeInfo.Receiver,
				IsCompleted:         isCompleted,
				SellTokenBalance:    sellTokenBl,
				BuyTokenBalance:     buyTokenBl,
				SellTokenWithdrawed: sellTokenWD,
				BuyTokenWithdrawed:  buyTokenWD,
			}
			result = append(result, trade)
		}

	}
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}
