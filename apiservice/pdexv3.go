package apiservice

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/davecgh/go-spew/spew"

	"github.com/gin-gonic/gin"
	"github.com/incognitochain/coin-service/database"
	"github.com/incognitochain/coin-service/pdexv3/analyticsquery"
	"github.com/incognitochain/coin-service/pdexv3/feeestimator"
	"github.com/incognitochain/coin-service/pdexv3/pathfinder"
	"github.com/incognitochain/coin-service/shared"
	"github.com/incognitochain/incognito-chain/metadata"
	pdexv3Meta "github.com/incognitochain/incognito-chain/metadata/pdexv3"
	"github.com/incognitochain/incognito-chain/rpcserver/jsonresult"
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

		tk1Amount, _ := strconv.ParseUint(v.Token1Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(v.Token2Amount, 10, 64)
		data := PdexV3PairData{
			PairID:       v.PairID,
			TokenID1:     v.TokenID1,
			TokenID2:     v.TokenID2,
			Token1Amount: tk1Amount,
			Token2Amount: tk2Amount,
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
	if pair == "all" {
		var result []PdexV3PoolDetail
		err := cacheGet("ListPools-all", &result)
		if err != nil {
			fmt.Println("cacheStoreCustom failed")
			log.Println(err)
		} else {
			fmt.Println("cacheStoreCustom success 1111")
			respond := APIRespond{
				Result: result,
				Error:  nil,
			}
			c.JSON(http.StatusOK, respond)
			return
		}
	}
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
	var wg sync.WaitGroup
	wg.Add(len(list))
	resultCh := make(chan PdexV3PoolDetail, len(list))
	for _, v := range list {
		go func(d shared.PoolInfoData) {
			var data *PdexV3PoolDetail
			defer func() {
				wg.Done()
				if data != nil {
					resultCh <- *data
				}
			}()
			tk1Amount, _ := strconv.ParseUint(d.Token1Amount, 10, 64)
			tk2Amount, _ := strconv.ParseUint(d.Token2Amount, 10, 64)
			if tk1Amount == 0 || tk2Amount == 0 {
				return
			}
			dcrate, err := getPdecimalRate(d.TokenID1, d.TokenID2)
			if err != nil {
				log.Println(err)
				return
			}
			tk1VA, _ := strconv.ParseUint(d.Virtual1Amount, 10, 64)
			tk2VA, _ := strconv.ParseUint(d.Virtual2Amount, 10, 64)
			totalShare, _ := strconv.ParseUint(d.TotalShare, 10, 64)
			data = &PdexV3PoolDetail{
				PoolID:         d.PoolID,
				Token1ID:       d.TokenID1,
				Token2ID:       d.TokenID2,
				Token1Value:    tk1Amount,
				Token2Value:    tk2Amount,
				Virtual1Value:  tk1VA,
				Virtual2Value:  tk2VA,
				Volume:         0,
				PriceChange24h: 0,
				AMP:            d.AMP,
				Price:          (float64(tk2Amount) / float64(tk1Amount)) * dcrate,
				TotalShare:     totalShare,
			}

			if poolChange, found := poolLiquidityChanges[d.PoolID]; found {
				data.PriceChange24h = poolChange.RateChangePercentage
				data.Volume = poolChange.TradingVolume24h
			}

			apy, err := database.DBGetPDEPoolPairRewardAPY(data.PoolID)
			if err != nil {
				log.Println(err)
				return
			}
			data.APY = uint64(apy.APY)
			if _, found := defaultPools[d.PoolID]; found {
				data.IsVerify = true
			}
			return
		}(v)
	}
	wg.Wait()
	close(resultCh)
	for v := range resultCh {
		result = append(result, v)
	}
	go cacheStoreCustom("ListPools-all", result, 10*time.Second)
	fmt.Println("cacheStoreCustom success")
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
	amount, _ := strconv.ParseUint(tradeInfo.Amount, 10, 64)
	minAccept, _ := strconv.ParseUint(tradeInfo.MinAccept, 10, 64)
	result := TradeDataRespond{
		RequestTx:           tradeInfo.RequestTx,
		RespondTxs:          tradeInfo.RespondTxs,
		RespondTokens:       tradeInfo.RespondTokens,
		RespondAmounts:      tradeInfo.RespondAmount,
		WithdrawTxs:         withdrawTxs,
		PoolID:              tradeInfo.PoolID,
		PairID:              tradeInfo.PairID,
		SellTokenID:         tradeInfo.SellTokenID,
		BuyTokenID:          tradeInfo.BuyTokenID,
		Amount:              amount,
		MinAccept:           minAccept,
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
		if (v.Amount == 0) && len(v.TradingFee) == 0 {
			continue
		}

		tk1Amount, _ := strconv.ParseUint(l[0].Token1Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(l[0].Token2Amount, 10, 64)
		totalShare, _ := strconv.ParseUint(l[0].TotalShare, 10, 64)

		result = append(result, PdexV3PoolShareRespond{
			PoolID:       v.PoolID,
			Share:        v.Amount,
			Rewards:      v.TradingFee,
			AMP:          l[0].AMP,
			TokenID1:     l[0].TokenID1,
			TokenID2:     l[0].TokenID2,
			Token1Amount: tk1Amount,
			Token2Amount: tk2Amount,
			TotalShare:   totalShare,
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
				matchedAmount, _ = strconv.ParseUint(tradeInfo.Amount, 10, 64)
				isCompleted = true
			case 2:
				status = "rejected"
				isCompleted = true
			}
			amount, _ := strconv.ParseUint(tradeInfo.Amount, 10, 64)
			minAccept, _ := strconv.ParseUint(tradeInfo.MinAccept, 10, 64)
			trade := TradeDataRespond{
				RequestTx:      tradeInfo.RequestTx,
				RespondTxs:     tradeInfo.RespondTxs,
				RespondTokens:  tradeInfo.RespondTokens,
				RespondAmounts: tradeInfo.RespondAmount,
				WithdrawTxs:    nil,
				PoolID:         tradeInfo.PoolID,
				PairID:         tradeInfo.PairID,
				SellTokenID:    tradeInfo.SellTokenID,
				BuyTokenID:     tradeInfo.BuyTokenID,
				Amount:         amount,
				MinAccept:      minAccept,
				Matched:        matchedAmount,
				Status:         status,
				StatusCode:     tradeInfo.Status,
				Requestime:     tradeInfo.Requesttime,
				NFTID:          tradeInfo.NFTID,
				Fee:            tradeInfo.Fee,
				FeeToken:       tradeInfo.FeeToken,
				Receiver:       tradeInfo.Receiver,
				IsCompleted:    isCompleted,
				TradingPath:    tradeInfo.TradingPath,
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
			amount, _ := strconv.ParseUint(tradeInfo.Amount, 10, 64)
			minAccept, _ := strconv.ParseUint(tradeInfo.MinAccept, 10, 64)
			trade := TradeDataRespond{
				RequestTx:           tradeInfo.RequestTx,
				RespondTxs:          tradeInfo.RespondTxs,
				RespondTokens:       tradeInfo.RespondTokens,
				RespondAmounts:      tradeInfo.RespondAmount,
				WithdrawTxs:         withdrawTxs,
				PoolID:              tradeInfo.PoolID,
				PairID:              tradeInfo.PairID,
				SellTokenID:         tradeInfo.SellTokenID,
				BuyTokenID:          tradeInfo.BuyTokenID,
				Amount:              amount,
				MinAccept:           minAccept,
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
			a, _ := strconv.ParseUint(v.ContributeAmount[0], 10, 64)
			ctrbAmount = append(ctrbAmount, a)
			ctrbAmount = append(ctrbAmount, a)
		} else {
			for _, v := range v.ContributeAmount {
				a, _ := strconv.ParseUint(v, 10, 64)
				ctrbAmount = append(ctrbAmount, a)
			}

		}
		if len(v.RequestTxs) > len(v.ContributeTokens) {
			ctrbToken = append(ctrbToken, v.ContributeTokens[0])
			ctrbToken = append(ctrbToken, v.ContributeTokens[0])
		} else {
			ctrbToken = v.ContributeTokens
		}
		returnAmount := []uint64{}
		for _, v := range v.ReturnAmount {
			a, _ := strconv.ParseUint(v, 10, 64)
			returnAmount = append(returnAmount, a)
		}

		data := PdexV3ContributionData{
			RequestTxs:       v.RequestTxs,
			RespondTxs:       v.RespondTxs,
			ContributeTokens: ctrbToken,
			ContributeAmount: ctrbAmount,
			PairID:           v.PairID,
			PairHash:         v.PairHash,
			ReturnTokens:     v.ReturnTokens,
			ReturnAmount:     returnAmount,
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
			amount1, _ = strconv.ParseUint(v.WithdrawAmount[0], 10, 64)
			amount2, _ = strconv.ParseUint(v.WithdrawAmount[1], 10, 64)
		}
		if len(v.RespondTxs) == 1 {
			amount1, _ = strconv.ParseUint(v.WithdrawAmount[0], 10, 64)
		}
		tks := strings.Split(v.PoolID, "-")
		token1 = tks[0]
		token2 = tks[1]
		shareAmount, err := strconv.ParseUint(v.ShareAmount, 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
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
			ShareAmount: shareAmount,
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
		tokens := make(map[string]uint64)
		for idx, tk := range v.WithdrawTokens {
			tokens[tk], _ = strconv.ParseUint(v.WithdrawAmount[idx], 10, 64)
		}
		result = append(result, PdexV3WithdrawFeeRespond{
			PoolID:         v.PoodID,
			RequestTx:      v.RequestTx,
			RespondTxs:     v.RespondTxs,
			WithdrawTokens: tokens,
			Status:         v.Status,
			Requestime:     v.RequestTime,
		})
	}
	respond := APIRespond{
		Result: result,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) StakingPool(c *gin.Context) {
	list, err := database.DBGetStakePools()
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3StakingPoolInfo
	for _, v := range list {

		apy, err := database.DBGetPDEPoolPairRewardAPY(v.TokenID)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		data := PdexV3StakingPoolInfo{
			Amount:  v.Amount,
			TokenID: v.TokenID,
			APY:     int(apy.APY),
		}
		result = append(result, data)
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
		rewards := make(map[string]uint64)
		for idx, tk := range v.RewardTokens {
			rewards[tk] = v.Amount[idx]
		}
		data := PdexV3StakePoolRewardHistoryData{
			RespondTxs:   v.RespondTxs,
			RequestTx:    v.RequestTx,
			Status:       v.Status,
			TokenID:      v.TokenID,
			NFTID:        v.NFTID,
			RewardTokens: rewards,
			Requesttime:  v.Requesttime,
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
	list, err := database.DBGetPairsByID(req.PairIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	var result []PdexV3PairData
	for _, v := range list {
		tk1Amount, _ := strconv.ParseUint(v.Token1Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(v.Token2Amount, 10, 64)
		data := PdexV3PairData{
			PairID:       v.PairID,
			TokenID1:     v.TokenID1,
			TokenID2:     v.TokenID2,
			Token1Amount: tk1Amount,
			Token2Amount: tk2Amount,
			PoolCount:    v.PoolCount,
		}
		result = append(result, data)
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

	poolLiquidityChanges, err := analyticsquery.APIGetPDexV3PairRateChangesAndVolume24h(req.PoolIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var result []PdexV3PoolDetail
	for _, v := range list {
		tk1Amount, _ := strconv.ParseUint(v.Token1Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(v.Token2Amount, 10, 64)
		if tk1Amount == 0 || tk2Amount == 0 {
			continue
		}
		dcrate, err := getPdecimalRate(v.TokenID1, v.TokenID2)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		tk1VA, _ := strconv.ParseUint(v.Virtual1Amount, 10, 64)
		tk2VA, _ := strconv.ParseUint(v.Virtual2Amount, 10, 64)
		totalShare, _ := strconv.ParseUint(v.TotalShare, 10, 64)
		data := PdexV3PoolDetail{
			PoolID:         v.PoolID,
			Token1ID:       v.TokenID1,
			Token2ID:       v.TokenID2,
			Token1Value:    tk1Amount,
			Token2Value:    tk2Amount,
			Virtual1Value:  tk1VA,
			Virtual2Value:  tk2VA,
			PriceChange24h: 0,
			Volume:         0,
			AMP:            v.AMP,
			Price:          (float64(tk2Amount) / float64(tk1Amount)) * dcrate,
			TotalShare:     totalShare,
		}

		if poolChange, found := poolLiquidityChanges[v.PoolID]; found {
			data.PriceChange24h = poolChange.RateChangePercentage
			data.Volume = poolChange.TradingVolume24h
		}

		apy, err := database.DBGetPDEPoolPairRewardAPY(data.PoolID)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
		data.APY = uint64(apy.APY)
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

// const (
// 	decimal_100 = float64(100)
// 	decimal_10  = float64(10)
// 	decimal_1   = float64(1)
// 	decimal_01  = float64(0.1)
// 	decimal_001 = float64(0.01)
// )

func (pdexv3) GetOrderBook(c *gin.Context) {
	decimal := c.Query("decimal")
	poolID := c.Query("poolid")

	decimalFloat, err := strconv.ParseFloat(decimal, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
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
		amount, _ := strconv.ParseUint(v.Amount, 10, 64)
		minAccept, _ := strconv.ParseUint(v.MinAccept, 10, 64)
		price := float64(minAccept) / float64(amount)
		group := math.Floor(float64(price)*(1/decimalFloat)) * decimalFloat
		groupStr := fmt.Sprintf("%g", group)
		if d, ok := sellVolume[groupStr]; !ok {
			sellVolume[groupStr] = PdexV3OrderBookVolume{
				Price:   group,
				average: price,
				Volume:  amount,
			}
		} else {
			d.Volume += amount
			a := (d.average + price) / 2
			d.average = a
			sellVolume[groupStr] = d
		}
	}

	for _, v := range buySide {
		amount, _ := strconv.ParseUint(v.Amount, 10, 64)
		minAccept, _ := strconv.ParseUint(v.MinAccept, 10, 64)
		price := float64(amount) / float64(minAccept)
		group := math.Floor(float64(price)*(1/decimalFloat)) * decimalFloat
		groupStr := fmt.Sprintf("%g", group)
		if d, ok := buyVolume[groupStr]; !ok {
			buyVolume[groupStr] = PdexV3OrderBookVolume{
				Price:   group,
				average: price,
				Volume:  amount,
			}
		} else {
			d.Volume += amount
			a := (d.average + price) / 2
			d.average = a
			buyVolume[groupStr] = d
		}
	}

	for _, v := range sellVolume {
		data := PdexV3OrderBookVolume{
			Price:  v.Price,
			Volume: v.Volume,
		}
		result.Sell = append(result.Sell, data)
	}
	for _, v := range buyVolume {
		data := PdexV3OrderBookVolume{
			Price:  v.Price,
			Volume: v.Volume,
		}
		result.Buy = append(result.Buy, data)
	}
	sort.SliceStable(result.Sell, func(i, j int) bool {
		return result.Sell[i].Price > result.Sell[j].Price
	})
	sort.SliceStable(result.Buy, func(i, j int) bool {
		return result.Buy[i].Price > result.Buy[j].Price
	})
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
		SellToken  string `form:"selltoken" json:"selltoken" binding:"required"`
		BuyToken   string `form:"buytoken" json:"buytoken" binding:"required"`
		SellAmount uint64 `form:"sellamount" json:"sellamount"`
		BuyAmount  uint64 `form:"buyamount" json:"buyamount"`
		FeeInPRV   bool   `form:"feeinprv" json:"feeinprv"`
		Pdecimal   bool   `form:"pdecimal" json:"pdecimal"`
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
	sellAmount := req.SellAmount
	buyAmount := req.BuyAmount

	if sellAmount <= 0 && buyAmount <= 0 {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(errors.New("sellAmount and buyAmount must be >=0")))
		return
	}
	if sellAmount > 0 && buyAmount > 0 {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(errors.New("only accept sellAmount or buyAmount value individually")))
		return
	}

	var result PdexV3EstimateTradeRespond

	state, err := database.DBGetPDEState(2)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	data := `{"Result":` + state + `}`

	var responseBodyData shared.Pdexv3GetStateRPCResult

	err = json.UnmarshalFromString(data, &responseBodyData)
	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	pools, poolPairStates, err := pathfinder.GetPdexv3PoolDataFromRawRPCResult(responseBodyData.Result.Poolpairs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}

	pdexState, err := feeestimator.GetPdexv3PoolDataFromRawRPCResult(responseBodyData.Result.Params, responseBodyData.Result.Poolpairs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, buildGinErrorRespond(err))
		return
	}
	dcrate := float64(1)
	if req.Pdecimal {
		dcrate, err = getPdecimalRate(buyToken, sellToken)
		if err != nil {
			c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
			return
		}
	}

	var chosenPath []*shared.Pdexv3PoolPairWithId
	var foundSellAmount uint64
	var receive uint64

	spew.Dump(pools, poolPairStates, sellAmount, sellToken, buyAmount, pdexv3Meta.MaxTradePathLength)
	if sellAmount > 0 {
		chosenPath, receive = pathfinder.FindGoodTradePath(
			pdexv3Meta.MaxTradePathLength,
			pools,
			poolPairStates,
			sellToken,
			buyToken,
			sellAmount)
		spew.Dump("chosenPath", chosenPath)
		spew.Dump("MaxGet before multiply", receive)

		result.SellAmount = float64(sellAmount)
		result.MaxGet = float64(receive) * dcrate

	} else {
		chosenPath, foundSellAmount = pathfinder.FindSellAmount(
			pdexv3Meta.MaxTradePathLength,
			pools,
			poolPairStates,
			sellToken,
			buyToken,
			buyAmount)
		sellAmount = foundSellAmount
		receive = buyAmount
		spew.Dump("chosenPath", chosenPath)
		spew.Dump("foundSellAmount before multiply", foundSellAmount)

		result.SellAmount = float64(foundSellAmount) / dcrate
		result.MaxGet = float64(receive)
	}

	result.Route = make([]string, 0)
	if chosenPath != nil {
		for _, v := range chosenPath {
			result.Route = append(result.Route, v.PoolID)
		}
		//TODO: check pointer
		tradingFee, err := feeestimator.EstimateTradingFee(uint64(sellAmount), sellToken, result.Route, *pdexState, feeInPRV)

		if err != nil {
			log.Print("can not estimate fee: ", err)
			c.JSON(http.StatusUnprocessableEntity, buildGinErrorRespond(errors.New("can not estimate fee: "+err.Error())))
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
			Value float64
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

		amount, _ := strconv.ParseUint(tradeInfo.Amount, 10, 64)
		minAccept, _ := strconv.ParseUint(tradeInfo.MinAccept, 10, 64)

		if tradeInfo.IsSwap {
			matchedAmount := uint64(0)
			status := ""
			isCompleted := false
			switch tradeInfo.Status {
			case 0:
				status = "pending"
			case 1:
				status = "accepted"
				matchedAmount = amount
				isCompleted = true
			case 2:
				status = "rejected"
				isCompleted = true
			}

			trade := TradeDataRespond{
				RequestTx:      tradeInfo.RequestTx,
				RespondTxs:     tradeInfo.RespondTxs,
				RespondTokens:  tradeInfo.RespondTokens,
				RespondAmounts: tradeInfo.RespondAmount,
				WithdrawTxs:    nil,
				PoolID:         tradeInfo.PoolID,
				PairID:         tradeInfo.PairID,
				SellTokenID:    tradeInfo.SellTokenID,
				BuyTokenID:     tradeInfo.BuyTokenID,
				Amount:         amount,
				MinAccept:      minAccept,
				Matched:        matchedAmount,
				Status:         status,
				StatusCode:     tradeInfo.Status,
				Requestime:     tradeInfo.Requesttime,
				NFTID:          tradeInfo.NFTID,
				Fee:            tradeInfo.Fee,
				FeeToken:       tradeInfo.FeeToken,
				Receiver:       tradeInfo.Receiver,
				IsCompleted:    isCompleted,
				TradingPath:    tradeInfo.TradingPath,
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
				RespondTokens:       tradeInfo.RespondTokens,
				RespondAmounts:      tradeInfo.RespondAmount,
				WithdrawTxs:         withdrawTxs,
				PoolID:              tradeInfo.PoolID,
				PairID:              tradeInfo.PairID,
				SellTokenID:         tradeInfo.SellTokenID,
				BuyTokenID:          tradeInfo.BuyTokenID,
				Amount:              amount,
				MinAccept:           minAccept,
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

func (pdexv3) GetRate(c *gin.Context) {
	var req struct {
		TokenIDs []string `json:"TokenIDs"`
		Against  string   `json:"Against"`
	}
	err := c.ShouldBindJSON(&req)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	result := make(map[string]float64)

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
	for _, v := range req.TokenIDs {
		a := uint64(10000)
	retry:
		_, receive := pathfinder.FindGoodTradePath(
			pdexv3Meta.MaxTradePathLength,
			pools,
			poolPairStates,
			req.Against,
			v, a)
		if receive == 0 {
			a *= 10
			if a < 1e18 {
				goto retry
			}
			result[v] = 0
		} else {
			result[v] = float64(a) / float64(receive)
		}

	}

	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PendingOrder(c *gin.Context) {
	var result struct {
		Buy  []PdexV3PendingOrderData
		Sell []PdexV3PendingOrderData
	}
	var buyOrders []PdexV3PendingOrderData
	var sellOrders []PdexV3PendingOrderData
	poolID := c.Query("poolid")
	tks := strings.Split(poolID, "-")

	pairID := tks[0] + "-" + tks[1]
	list, err := database.DBGetLimitOrderStatusByPairID(pairID)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}

	var sellSide []shared.LimitOrderStatus
	var buySide []shared.LimitOrderStatus
	txRequestList := []string{}
	orderList := make(map[string]shared.TradeOrderData)
	for _, v := range list {
		if v.Direction == 0 {
			sellSide = append(sellSide, v)
		} else {
			buySide = append(buySide, v)
		}
		txRequestList = append(txRequestList, v.RequestTx)
	}

	l, err := database.DBGetTxTradeFromTxRequest(txRequestList)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	for _, v := range l {
		orderList[v.RequestTx] = v
	}
	for _, v := range sellSide {
		tk1Balance, _ := strconv.ParseUint(v.Token1Balance, 10, 64)
		tk2Balance, _ := strconv.ParseUint(v.Token2Balance, 10, 64)
		if tk1Balance == 0 {
			continue
		}
		tk1Amount, _ := strconv.ParseUint(orderList[v.RequestTx].Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(orderList[v.RequestTx].MinAccept, 10, 64)
		data := PdexV3PendingOrderData{
			TxRequest:     v.RequestTx,
			Token1Balance: tk1Balance,
			Token2Balance: tk2Balance,
			Token1Amount:  tk1Amount,
			Token2Amount:  tk2Amount,
		}
		sellOrders = append(sellOrders, data)
	}
	for _, v := range buySide {
		tk1Balance, _ := strconv.ParseUint(v.Token1Balance, 10, 64)
		tk2Balance, _ := strconv.ParseUint(v.Token2Balance, 10, 64)
		if tk2Balance == 0 {
			continue
		}
		tk1Amount, _ := strconv.ParseUint(orderList[v.RequestTx].Amount, 10, 64)
		tk2Amount, _ := strconv.ParseUint(orderList[v.RequestTx].MinAccept, 10, 64)
		data := PdexV3PendingOrderData{
			TxRequest:     v.RequestTx,
			Token1Balance: tk1Balance,
			Token2Balance: tk2Balance,
			Token1Amount:  tk2Amount,
			Token2Amount:  tk1Amount,
		}
		buyOrders = append(buyOrders, data)
	}
	result.Buy = buyOrders
	result.Sell = sellOrders
	respond := APIRespond{
		Result: result,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}

func (pdexv3) PDEState(c *gin.Context) {
	state, err := database.DBGetPDEState(2)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	pdeState := jsonresult.Pdexv3State{}
	err = json.UnmarshalFromString(state, &pdeState)
	if err != nil {
		c.JSON(http.StatusBadRequest, buildGinErrorRespond(err))
		return
	}
	respond := APIRespond{
		Result: pdeState,
		Error:  nil,
	}
	c.JSON(http.StatusOK, respond)
}
