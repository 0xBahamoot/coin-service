#!/bin/sh
MONGOHOST="$(getent hosts mongo | awk '{ print $1 }')"
INDEXERADDR="$(getent hosts indexer | awk '{ print $1 }')"
JSON='{"apiport":'"$PORT"',"chaindata":"chain","concurrentotacheck":10,"mode":"'"$MODE"'","mongo":"mongodb://'"${MONGO_USERNAME}"':'"${MONGO_PASSWORD}"'@'"$MONGOHOST"':27017","mongodb":"coin","chainnetwork":"'"$CHAINNETWORK"'","indexerid": '"$INDEXERID"',"masterindexer":"'"$INDEXERADDR"':'"$PORT"'","analyticsAPIEndpoint": "'"$ANALYTICS"'","externaldecimals":"'"$EXDECIMALS"'","fullnodedata":'"$FULLNODEDATA"'}'
echo $JSON
echo $JSON > cfg.json
./coinservice