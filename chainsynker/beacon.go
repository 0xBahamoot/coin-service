package chainsynker

import (
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/incognitochain/coin-service/database"
	"github.com/incognitochain/coin-service/pdexv3/pathfinder"
	"github.com/incognitochain/coin-service/shared"
	"github.com/incognitochain/incognito-chain/blockchain"
	"github.com/incognitochain/incognito-chain/blockchain/pdex"
	"github.com/incognitochain/incognito-chain/common"
	"github.com/incognitochain/incognito-chain/config"
	"github.com/incognitochain/incognito-chain/dataaccessobject/rawdbv2"
	"github.com/incognitochain/incognito-chain/dataaccessobject/statedb"
	instruction "github.com/incognitochain/incognito-chain/instruction/pdexv3"
	"github.com/incognitochain/incognito-chain/metadata"
	metadataCommon "github.com/incognitochain/incognito-chain/metadata/common"
	metadataPdexv3 "github.com/incognitochain/incognito-chain/metadata/pdexv3"
	"github.com/incognitochain/incognito-chain/rpcserver/jsonresult"
)

func processBeacon(bc *blockchain.BlockChain, h common.Hash, height uint64) {
	log.Printf("start processing coin for block %v beacon\n", height)
	startTime := time.Now()
	beaconBestState, _ := Localnode.GetBlockchain().GetBeaconViewStateDataFromBlockHash(h, false)
	blk := beaconBestState.BestBlock
	beaconFeatureStateRootHash := beaconBestState.FeatureStateDBRootHash
	beaconFeatureStateDB, err := statedb.NewWithPrefixTrie(beaconFeatureStateRootHash, statedb.NewDatabaseAccessWarper(Localnode.GetBlockchain().GetBeaconChainDatabase()))
	if err != nil {
		log.Println(err)
	}
	var prevStateV2 *shared.PDEStateV2
	var prevStatev2Inc pdex.State
	// this is a requirement check
	for shardID, blks := range blk.Body.ShardState {
		sort.Slice(blks, func(i, j int) bool { return blks[i].Height > blks[j].Height })
	retry:
		pass := true
		blockProcessedLock.RLock()
		if blockProcessed[int(shardID)] < blks[0].Height {
			pass = false
		}
		blockProcessedLock.RUnlock()
		if !pass {
			time.Sleep(1 * time.Second)
			goto retry
		}
	}
	if height != 1 && height > config.Param().PDexParams.Pdexv3BreakPointHeight {
		prevBeaconFeatureStateDB, err := Localnode.GetBlockchain().GetBestStateBeaconFeatureStateDBByHeight(height-1, Localnode.GetBlockchain().GetBeaconChainDatabase())
		if err != nil {
			log.Println(err)
		}

		pdeState, err := pdex.InitStateFromDB(prevBeaconFeatureStateDB, height-1, 2)
		if err != nil {
			log.Println(err)
		}
		prevStatev2Inc = pdeState
		poolPairs := make(map[string]*shared.PoolPairState)
		err = json.Unmarshal(pdeState.Reader().PoolPairs(), &poolPairs)
		if err != nil {
			panic(err)
		}
		prevStateV2 = &shared.PDEStateV2{
			PoolPairs:         poolPairs,
			StakingPoolsState: pdeState.Reader().StakingPools(),
		}
	}
	// Process PDEstate
	stateV1, err := pdex.InitStateFromDB(beaconFeatureStateDB, beaconBestState.BeaconHeight, 1)
	if err != nil {
		log.Println(err)
	}
	// var stateV1 *shared.PDEStateV1
	var stateV2 *shared.PDEStateV2

	// if pdeState.Version() == 1 {
	poolPairs := make(map[string]*rawdbv2.PDEPoolForPair)
	err = json.Unmarshal(stateV1.Reader().PoolPairs(), &poolPairs)
	if err != nil {
		panic(err)
	}
	waitingContributions := make(map[string]*rawdbv2.PDEContribution)
	err = json.Unmarshal(stateV1.Reader().WaitingContributions(), &waitingContributions)
	if err != nil {
		panic(err)
	}
	pdeStateJSON := jsonresult.CurrentPDEState{
		BeaconTimeStamp:         beaconBestState.BestBlock.Header.Timestamp,
		PDEPoolPairs:            poolPairs,
		PDEShares:               stateV1.Reader().Shares(),
		WaitingPDEContributions: waitingContributions,
		PDETradingFees:          stateV1.Reader().TradingFees(),
	}
	pdeStr, err := json.MarshalToString(pdeStateJSON)
	if err != nil {
		log.Println(err)
	}
	err = database.DBSavePDEState(pdeStr, height, 1)
	if err != nil {
		log.Println(err)
	}
	//process stateV2
	if beaconBestState.BeaconHeight >= config.Param().PDexParams.Pdexv3BreakPointHeight {
		pdeStateV2, err := pdex.InitStateFromDB(beaconFeatureStateDB, beaconBestState.BeaconHeight, 2)
		if err != nil {
			log.Println(err)
		}

		poolPairs := make(map[string]*shared.PoolPairState)
		err = json.Unmarshal(pdeStateV2.Reader().PoolPairs(), &poolPairs)
		if err != nil {
			panic(err)
		}
		poolPairsJSON := make(map[string]*pdex.PoolPairState)
		err = json.Unmarshal(pdeStateV2.Reader().PoolPairs(), &poolPairsJSON)
		if err != nil {
			panic(err)
		}
		stateV2 = &shared.PDEStateV2{
			PoolPairs:         poolPairs,
			StakingPoolsState: pdeStateV2.Reader().StakingPools(),
		}

		pdeStateJSON := jsonresult.Pdexv3State{
			BeaconTimeStamp: beaconBestState.BestBlock.Header.Timestamp,
			PoolPairs:       &poolPairsJSON,
			StakingPools:    &stateV2.StakingPoolsState,
			Params:          pdeStateV2.Reader().Params(),
		}
		pdeStr, err := json.MarshalToString(pdeStateJSON)
		if err != nil {
			log.Println(err)
		}

		pairDatas, poolDatas, sharesDatas, poolStakeDatas, poolStakersDatas, orderBook, poolDatasToBeDel, sharesDatasToBeDel, poolStakeDatasToBeDel, poolStakersDatasToBeDel, orderBookToBeDel, rewardRecords, err := processPoolPairs(stateV2, prevStateV2, pdeStateV2, prevStatev2Inc, &pdeStateJSON, beaconBestState.BeaconHeight)
		if err != nil {
			panic(err)
		}

		err = database.DBSavePDEState(pdeStr, height, 2)
		if err != nil {
			log.Println(err)
		}

		instructions, err := extractBeaconInstruction(beaconBestState.BestBlock.Body.Instructions)
		if err != nil {
			panic(err)
		}

		err = database.DBDeletePDEPoolShareData(sharesDatasToBeDel)
		if err != nil {
			panic(err)
		}

		err = database.DBDeletePDEPoolStakeData(poolStakeDatasToBeDel)
		if err != nil {
			panic(err)
		}

		err = database.DBDeletePDEPoolStakerData(poolStakersDatasToBeDel)
		if err != nil {
			panic(err)
		}
		err = database.DBDeleteOrderProgress(orderBookToBeDel)
		if err != nil {
			panic(err)
		}

		err = database.DBSaveInstructionBeacon(instructions)
		if err != nil {
			panic(err)
		}

		err = database.DBSaveRewardRecord(rewardRecords)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdatePDEPairListData(pairDatas)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdatePDEPoolPairData(poolDatas)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdatePDEPoolShareData(sharesDatas)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdatePDEPoolStakeData(poolStakeDatas)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdatePDEPoolStakerData(poolStakersDatas)
		if err != nil {
			panic(err)
		}

		err = database.DBUpdateOrderProgress(orderBook)
		if err != nil {
			panic(err)
		}

		err = database.DBDeletePDEPoolData(poolDatasToBeDel)
		if err != nil {
			panic(err)
		}
	}

	statePrefix := BeaconData
	err = Localnode.GetUserDatabase().Put([]byte(statePrefix), []byte(fmt.Sprintf("%v", blk.Header.Height)), nil)
	if err != nil {
		panic(err)
	}
	blockProcessedLock.Lock()
	blockProcessed[-1] = blk.Header.Height
	blockProcessedLock.Unlock()
	log.Printf("finish processing coin for block %v beacon in %v\n", blk.GetHeight(), time.Since(startTime))
}

func processPoolPairs(statev2 *shared.PDEStateV2, prevStatev2 *shared.PDEStateV2, statev2Inc pdex.State, prevStatev2Inc pdex.State, stateV2Json *jsonresult.Pdexv3State, beaconHeight uint64) ([]shared.PairData, []shared.PoolPairData, []shared.PoolShareData, []shared.PoolStakeData, []shared.PoolStakerData, []shared.LimitOrderStatus, []shared.PoolPairData, []shared.PoolShareData, []shared.PoolStakeData, []shared.PoolStakerData, []shared.LimitOrderStatus, []shared.RewardRecord, error) {
	var pairList []shared.PairData
	pairListMap := make(map[string][]shared.PoolPairData)
	var poolPairs []shared.PoolPairData
	var poolShare []shared.PoolShareData
	var stakePools []shared.PoolStakeData
	var poolStaking []shared.PoolStakerData
	var orderStatus []shared.LimitOrderStatus
	var rewardRecords []shared.RewardRecord

	var poolPairsToBeDelete []shared.PoolPairData
	var poolShareToBeDelete []shared.PoolShareData
	var stakePoolsToBeDelete []shared.PoolStakeData
	var poolStakingToBeDelete []shared.PoolStakerData
	var orderStatusToBeDelete []shared.LimitOrderStatus

	poolPairsInc := make(map[string]*pdex.PoolPairState)
	err := json.Unmarshal(statev2Inc.Reader().PoolPairs(), &poolPairsInc)
	if err != nil {
		panic(err)
	}

	for poolID, state := range statev2.PoolPairs {
		poolData := shared.PoolPairData{
			Version:        2,
			PoolID:         poolID,
			PairID:         state.State.Token0ID().String() + "-" + state.State.Token1ID().String(),
			AMP:            state.State.Amplifier(),
			TokenID1:       state.State.Token0ID().String(),
			TokenID2:       state.State.Token1ID().String(),
			Token1Amount:   state.State.Token0RealAmount(),
			Token2Amount:   state.State.Token1RealAmount(),
			Virtual1Amount: state.State.Token0VirtualAmount().Uint64(),
			Virtual2Amount: state.State.Token1VirtualAmount().Uint64(),
			TotalShare:     state.State.ShareAmount(),
		}
		poolPairs = append(poolPairs, poolData)
		pairListMap[poolData.PairID] = append(pairListMap[poolData.PairID], poolData)
		for shareID, share := range state.Shares {
			tradingFee := make(map[string]uint64)
			shareIDHash, err := common.Hash{}.NewHashFromStr(shareID)
			if err != nil {
				panic(err)
			}
			rewards, err := poolPairsInc[poolID].RecomputeLPFee(*shareIDHash)
			if err != nil {
				panic(err)
			}
			for k, v := range rewards {
				tradingFee[k.String()] = v
			}
			shareData := shared.PoolShareData{
				Version:    2,
				PoolID:     poolID,
				Amount:     share.Amount(),
				TradingFee: tradingFee,
				NFTID:      shareID,
			}
			poolShare = append(poolShare, shareData)
		}

		for _, order := range state.Orderbook.Orders {
			newOrder := shared.LimitOrderStatus{
				RequestTx:     order.Id(),
				Token1Balance: fmt.Sprintf("%v", order.Token0Balance()),
				Token2Balance: fmt.Sprintf("%v", order.Token1Balance()),
				Direction:     order.TradeDirection(),
				PoolID:        poolID,
				PairID:        poolData.PairID,
			}
			orderStatus = append(orderStatus, newOrder)
		}
	}

	for pairID, pools := range pairListMap {
		data := shared.PairData{
			PairID:    pairID,
			TokenID1:  pools[0].TokenID1,
			TokenID2:  pools[0].TokenID2,
			PoolCount: len(pools),
		}

		for _, v := range pools {
			data.Token1Amount += v.Token1Amount
			data.Token2Amount += v.Token2Amount
		}

		pairList = append(pairList, data)
	}

	for tokenID, stakeData := range statev2.StakingPoolsState {
		poolData := shared.PoolStakeData{
			Amount:  stakeData.Liquidity(),
			TokenID: tokenID,
		}
		stakePools = append(stakePools, poolData)
		for shareID, staker := range stakeData.Stakers() {
			rewardMap := make(map[string]uint64)

			shareIDHash, err := common.Hash{}.NewHashFromStr(shareID)
			if err != nil {
				panic(err)
			}
			reward, err := statev2Inc.Reader().StakingPools()[tokenID].RecomputeStakingRewards(*shareIDHash)
			if err != nil {
				panic(err)
			}
			for k, v := range reward {
				rewardMap[k.String()] = v
			}
			stake := shared.PoolStakerData{
				TokenID: tokenID,
				NFTID:   shareID,
				Amount:  staker.Liquidity(),
				Reward:  rewardMap,
			}
			poolStaking = append(poolStaking, stake)
		}
	}
	for tokenID, _ := range prevStatev2.StakingPoolsState {
		willDelete := false
		if _, ok := statev2Inc.Reader().Params().StakingPoolsShare[tokenID]; !ok {
			willDelete = true
		}
		if willDelete {
			poolData := shared.PoolStakeData{
				TokenID: tokenID,
			}
			stakePoolsToBeDelete = append(stakePoolsToBeDelete, poolData)
		}
	}

	//comparing with old state
	if prevStatev2 != nil {
		var poolPairsArr []*shared.Pdexv3PoolPairWithId
		for poolId, element := range statev2.PoolPairs {

			var poolPair rawdbv2.Pdexv3PoolPair
			var poolPairWithId shared.Pdexv3PoolPairWithId

			poolPair = element.State
			poolPairWithId = shared.Pdexv3PoolPairWithId{
				poolPair,
				shared.Pdexv3PoolPairChild{
					PoolID: poolId},
			}

			poolPairsArr = append(poolPairsArr, &poolPairWithId)
		}

		if beaconHeight%config.Param().EpochParam.NumberOfBlockInEpoch == 0 {
			for poolID, state := range statev2.StakingPoolsState {
				rw, err := extractPDEStakingReward(poolID, statev2Inc, prevStatev2Inc, beaconHeight)
				if err != nil {
					panic(err)
				}
				rewardReceive := uint64(0)
				tokenStakeAmount := state.Liquidity()

				_, receiveStake := pathfinder.FindGoodTradePath(
					5,
					poolPairsArr,
					*stateV2Json.PoolPairs,
					poolID,
					common.PRVCoinID.String(),
					tokenStakeAmount)

				for tk, v := range rw {
					_, receive := pathfinder.FindGoodTradePath(
						5,
						poolPairsArr,
						*stateV2Json.PoolPairs,
						tk,
						common.PRVCoinID.String(),
						v)
					rewardReceive += receive
				}
				var rwInfo struct {
					RewardPerToken     map[string]uint64
					TokenAmount        map[string]uint64
					RewardReceiveInPRV uint64
					TotalAmountInPRV   uint64
				}
				rwInfo.TokenAmount = make(map[string]uint64)
				rwInfo.RewardPerToken = rw
				rwInfo.TokenAmount[poolID] = tokenStakeAmount
				rwInfo.RewardReceiveInPRV = rewardReceive
				rwInfo.TotalAmountInPRV = receiveStake

				rwInfoBytes, err := json.Marshal(rwInfo)
				if err != nil {
					panic(err)
				}
				data := shared.RewardRecord{
					DataID:       poolID,
					Data:         string(rwInfoBytes),
					BeaconHeight: beaconHeight,
				}
				rewardRecords = append(rewardRecords, data)
			}
			for poolID, state := range statev2.PoolPairs {
				rw, err := extractLqReward(poolID, statev2Inc, prevStatev2Inc, beaconHeight)
				if err != nil {
					panic(err)
				}
				rewardReceive := uint64(0)
				token1Amount := state.State.Token0RealAmount()
				token2Amount := state.State.Token1RealAmount()

				_, receive1 := pathfinder.FindGoodTradePath(
					5,
					poolPairsArr,
					*stateV2Json.PoolPairs,
					state.State.Token0ID().String(),
					common.PRVCoinID.String(),
					token1Amount)
				_, receive2 := pathfinder.FindGoodTradePath(
					5,
					poolPairsArr,
					*stateV2Json.PoolPairs,
					state.State.Token1ID().String(),
					common.PRVCoinID.String(),
					token2Amount)

				totalAmount := receive1 + receive2
				for tk, v := range rw {
					_, receive := pathfinder.FindGoodTradePath(
						5,
						poolPairsArr,
						*stateV2Json.PoolPairs,
						tk,
						common.PRVCoinID.String(),
						v)
					rewardReceive += receive
				}
				var rwInfo struct {
					RewardPerToken     map[string]uint64
					TokenAmount        map[string]uint64
					RewardReceiveInPRV uint64
					TotalAmountInPRV   uint64
				}
				rwInfo.TokenAmount = make(map[string]uint64)
				rwInfo.RewardPerToken = rw
				rwInfo.TokenAmount[state.State.Token0ID().String()] = token1Amount
				rwInfo.TokenAmount[state.State.Token1ID().String()] = token2Amount
				rwInfo.RewardReceiveInPRV = rewardReceive
				rwInfo.TotalAmountInPRV = totalAmount

				rwInfoBytes, err := json.Marshal(rwInfo)
				if err != nil {
					panic(err)
				}
				data := shared.RewardRecord{
					DataID:       poolID,
					Data:         string(rwInfoBytes),
					BeaconHeight: beaconHeight,
				}
				rewardRecords = append(rewardRecords, data)
			}
		}

		for poolID, state := range prevStatev2.PoolPairs {
			willDelete := false
			if _, ok := statev2.PoolPairs[poolID]; !ok {
				willDelete = true
			}
			if willDelete {
				poolData := shared.PoolPairData{
					Version: 2,
					PoolID:  poolID,
					PairID:  state.State.Token0ID().String() + "-" + state.State.Token1ID().String(),
				}
				poolPairsToBeDelete = append(poolPairsToBeDelete, poolData)
				for shareID, _ := range state.Shares {
					shareData := shared.PoolShareData{
						PoolID:  poolID,
						NFTID:   shareID,
						Version: 2,
					}
					poolShareToBeDelete = append(poolShareToBeDelete, shareData)
				}
				for _, order := range state.Orderbook.Orders {
					newOrder := shared.LimitOrderStatus{
						RequestTx: order.Id(),
					}
					orderStatusToBeDelete = append(orderStatusToBeDelete, newOrder)
				}
			} else {
				newState := statev2.PoolPairs[poolID]
				for shareID, _ := range state.Shares {
					willDelete := false
					if _, ok := newState.Shares[shareID]; !ok {
						willDelete = true
					}
					if willDelete {
						shareData := shared.PoolShareData{
							PoolID:  poolID,
							NFTID:   shareID,
							Version: 2,
						}
						poolShareToBeDelete = append(poolShareToBeDelete, shareData)
					}
				}
				for _, order := range state.Orderbook.Orders {
					willDelete := true
					for _, v := range newState.Orderbook.Orders {
						if v.Id() == order.Id() {
							willDelete = false
						}
					}
					if willDelete {
						newOrder := shared.LimitOrderStatus{
							RequestTx: order.Id(),
						}
						orderStatusToBeDelete = append(orderStatusToBeDelete, newOrder)
					}
				}
			}

		}
		for tokenID, stakeData := range prevStatev2.StakingPoolsState {
			willDelete := false
			if _, ok := statev2.StakingPoolsState[tokenID]; !ok {
				willDelete = true
			}
			if willDelete {
				poolData := shared.PoolStakeData{
					TokenID: tokenID,
				}
				stakePoolsToBeDelete = append(stakePoolsToBeDelete, poolData)
				for nftID, _ := range stakeData.Stakers() {
					stake := shared.PoolStakerData{
						TokenID: tokenID,
						NFTID:   nftID,
					}
					poolStakingToBeDelete = append(poolStakingToBeDelete, stake)
				}
			} else {
				newStaker := statev2.StakingPoolsState[tokenID].Stakers()
				for nftID, _ := range stakeData.Stakers() {
					willDelete := false
					if _, ok := newStaker[nftID]; !ok {
						willDelete = true
					}
					if willDelete {
						stake := shared.PoolStakerData{
							TokenID: tokenID,
							NFTID:   nftID,
						}
						poolStakingToBeDelete = append(poolStakingToBeDelete, stake)
					}
				}
			}
		}
	}

	return pairList, poolPairs, poolShare, stakePools, poolStaking, orderStatus, poolPairsToBeDelete, poolShareToBeDelete, stakePoolsToBeDelete, poolStakingToBeDelete, orderStatusToBeDelete, rewardRecords, nil
}

func extractBeaconInstruction(insts [][]string) ([]shared.InstructionBeaconData, error) {
	var result []shared.InstructionBeaconData
	for _, inst := range insts {
		data := shared.InstructionBeaconData{}

		metadataType, err := strconv.Atoi(inst[0])
		if err != nil {
			continue // Not error, just not PDE instructions
		}
		data.Metatype = inst[0]
		switch metadataType {
		case metadata.PDEWithdrawalRequestMeta:
			contentBytes, err := base64.StdEncoding.DecodeString(inst[3])
			if err != nil {
				panic(err)
			}
			var withdrawalRequestAction metadata.PDEWithdrawalRequestAction
			err = json.Unmarshal(contentBytes, &withdrawalRequestAction)
			if err != nil {
				panic(err)
			}
			data.Content = inst[3]
			data.Status = inst[2]
			data.TxRequest = withdrawalRequestAction.TxReqID.String()
		case metadata.PDEFeeWithdrawalRequestMeta:
			contentStr := inst[3]
			contentBytes, err := base64.StdEncoding.DecodeString(contentStr)
			if err != nil {
				panic(err)
			}
			var feeWithdrawalRequestAction metadata.PDEFeeWithdrawalRequestAction
			err = json.Unmarshal(contentBytes, &feeWithdrawalRequestAction)
			if err != nil {
				panic(err)
			}
			data.Content = inst[3]
			data.Status = inst[2]
			data.TxRequest = feeWithdrawalRequestAction.TxReqID.String()

		case metadataCommon.Pdexv3WithdrawLiquidityRequestMeta:
			data.Status = inst[1]
			data.Content = inst[2]
			switch inst[1] {
			case common.PDEWithdrawalRejectedChainStatus:
				rejectWithdrawLiquidity := instruction.NewRejectWithdrawLiquidity()
				err := rejectWithdrawLiquidity.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = rejectWithdrawLiquidity.TxReqID().String()
			case common.PDEWithdrawalAcceptedChainStatus:
				acceptWithdrawLiquidity := instruction.NewAcceptWithdrawLiquidity()
				err := acceptWithdrawLiquidity.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = acceptWithdrawLiquidity.TxReqID().String()
			}
		// case metadataCommon.Pdexv3TradeRequestMeta:

		case metadataCommon.Pdexv3WithdrawLPFeeRequestMeta:
			data.Status = inst[2]
			data.Content = inst[3]
			var actionData metadataPdexv3.WithdrawalLPFeeContent
			err := json.Unmarshal([]byte(inst[3]), &actionData)
			if err != nil {
				panic(err)
			}
			data.TxRequest = actionData.TxReqID.String()
		// case metadataCommon.Pdexv3WithdrawProtocolFeeRequestMeta:

		// case metadataCommon.Pdexv3AddOrderRequestMeta:

		case metadataCommon.Pdexv3WithdrawOrderRequestMeta:
			data.Status = inst[1]
			data.Content = inst[2]
			switch inst[1] {
			case strconv.Itoa(metadataPdexv3.WithdrawOrderAcceptedStatus):
				currentOrder := &instruction.Action{Content: &metadataPdexv3.AcceptedWithdrawOrder{}}
				err := currentOrder.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = currentOrder.RequestTxID().String()
			case strconv.Itoa(metadataPdexv3.WithdrawOrderRejectedStatus):
				currentOrder := &instruction.Action{Content: &metadataPdexv3.RejectedWithdrawOrder{}}
				err := currentOrder.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = currentOrder.RequestTxID().String()
			}
		// case metadataCommon.Pdexv3DistributeStakingRewardMeta:

		case metadataCommon.Pdexv3StakingRequestMeta:
			data.Status = inst[1]
			data.Content = inst[2]
			switch inst[1] {
			case common.Pdexv3AcceptUnstakingStatus:
				acceptInst := instruction.NewAcceptStaking()
				err := acceptInst.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = acceptInst.TxReqID().String()
			case common.Pdexv3RejectUnstakingStatus:
				rejectInst := instruction.NewRejectStaking()
				err := rejectInst.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = rejectInst.TxReqID().String()
			}
		case metadataCommon.Pdexv3UnstakingRequestMeta:
			data.Status = inst[1]
			data.Content = inst[2]
			switch inst[1] {
			case common.Pdexv3AcceptUnstakingStatus:
				acceptInst := instruction.NewAcceptUnstaking()
				err := acceptInst.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = acceptInst.TxReqID().String()
			case common.Pdexv3RejectUnstakingStatus:
				rejectInst := instruction.NewRejectUnstaking()
				err := rejectInst.FromStringSlice(inst)
				if err != nil {
					panic(err)
				}
				data.TxRequest = rejectInst.TxReqID().String()
			}
		case metadataCommon.Pdexv3WithdrawStakingRewardRequestMeta:
			var actionData metadataPdexv3.WithdrawalStakingRewardContent
			err := json.Unmarshal([]byte(inst[3]), &actionData)
			if err != nil {
				panic(err)
			}
			data.Status = inst[2]
			data.Content = inst[3]
			data.TxRequest = actionData.TxReqID.String()
		default:
			continue
		}

		result = append(result, data)
	}
	return result, nil
}

func extractLqReward(poolID string, curState pdex.State, prevState pdex.State, beaconHeight uint64) (map[string]uint64, error) {
	result := make(map[string]uint64)

	curLPFeesPerShare, shareAmount, err := getLPFeesPerShare(poolID, beaconHeight, curState)
	if err != nil {
		return nil, err
	}

	oldLPFeesPerShare, _, err := getLPFeesPerShare(poolID, beaconHeight-(config.Param().EpochParam.NumberOfBlockInEpoch), prevState)
	if err != nil {
		oldLPFeesPerShare = map[common.Hash]*big.Int{}
	}

	fmt.Println("curLPFeesPerShare", curLPFeesPerShare, "oldLPFeesPerShare", oldLPFeesPerShare)
	for tokenID := range curLPFeesPerShare {
		oldFees, isExisted := oldLPFeesPerShare[tokenID]
		if !isExisted {
			oldFees = big.NewInt(0)
		}
		newFees := curLPFeesPerShare[tokenID]

		reward := new(big.Int).Mul(new(big.Int).Sub(newFees, oldFees), new(big.Int).SetUint64(shareAmount))
		reward = new(big.Int).Div(reward, pdex.BaseLPFeesPerShare)

		if !reward.IsUint64() {
			return nil, fmt.Errorf("Reward of token %v is out of range", tokenID)
		}
		if reward.Uint64() > 0 {
			result[tokenID.String()] = reward.Uint64()
		}
	}

	return result, nil
}

func getLPFeesPerShare(pairID string, beaconHeight uint64, pdexState pdex.State) (map[common.Hash]*big.Int, uint64, error) {
	poolPairs := make(map[string]*pdex.PoolPairState)
	err := json.Unmarshal(pdexState.Reader().PoolPairs(), &poolPairs)
	if err != nil {
		return nil, 0, err
	}

	if _, ok := poolPairs[pairID]; !ok {
		return nil, 0, fmt.Errorf("Pool pair %s not found", pairID)
	}
	pair := poolPairs[pairID]
	pairState := pair.State()

	return pair.LpFeesPerShare(), pairState.ShareAmount(), nil
}

func getStakingRewardsPerShare(
	stakingPoolID string, beaconHeight uint64, pdexState pdex.State,
) (map[common.Hash]*big.Int, uint64, error) {

	stakingPools := pdexState.Reader().StakingPools()

	if _, ok := stakingPools[stakingPoolID]; !ok {
		return nil, 0, fmt.Errorf("Staking pool %s not found", stakingPoolID)
	}

	pool := stakingPools[stakingPoolID].Clone()

	return pool.RewardsPerShare(), pool.Liquidity(), nil
}

func extractPDEStakingReward(poolID string, curState pdex.State, prevState pdex.State, beaconHeight uint64) (map[string]uint64, error) {
	result := make(map[string]uint64)

	curLPFeesPerShare, shareAmount, err := getStakingRewardsPerShare(poolID, beaconHeight, curState)
	if err != nil {
		return nil, err
	}

	oldLPFeesPerShare, _, err := getStakingRewardsPerShare(poolID, beaconHeight-1, prevState)
	if err != nil {
		oldLPFeesPerShare = map[common.Hash]*big.Int{}
	}

	for tokenID := range curLPFeesPerShare {
		oldFees, isExisted := oldLPFeesPerShare[tokenID]
		if !isExisted {
			oldFees = big.NewInt(0)
		}
		newFees := curLPFeesPerShare[tokenID]

		reward := new(big.Int).Mul(new(big.Int).Sub(newFees, oldFees), new(big.Int).SetUint64(shareAmount))
		reward = new(big.Int).Div(reward, pdex.BaseLPFeesPerShare)

		if !reward.IsUint64() {
			return nil, fmt.Errorf("Reward of token %v is out of range", tokenID)
		}
		if reward.Uint64() > 0 {
			result[tokenID.String()] = reward.Uint64()
		}
	}

	return result, nil
}
