package otaindexer

import (
	"github.com/gorilla/websocket"
	"github.com/incognitochain/coin-service/shared"
	"github.com/incognitochain/incognito-chain/incognitokey"
)

type OTAkeyInfo struct {
	ShardID int
	Pubkey  string
	OTAKey  string
	keyset  *incognitokey.KeySet
	KeyInfo *shared.KeyInfoData
}

type OTAAssignRequest struct {
	Key     *shared.SubmittedOTAKeyData
	Respond chan error
}

type worker struct {
	ID          string
	Heartbeat   int64
	OTAAssigned int
	readCh      chan []byte
	writeCh     chan []byte
	conn        *websocket.Conn
}
