package database

import (
	"context"
	"time"

	"github.com/incognitochain/coin-service/shared"
	"github.com/kamva/mgm/v3"
	"github.com/kamva/mgm/v3/operator"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func DBSaveTokenInfo(list []shared.TokenInfoData) error {
	for _, v := range list {
		_, err := mgm.Coll(&shared.TokenInfoData{}).InsertOne(context.Background(), v)
		if err != nil {
			writeErr, ok := err.(mongo.WriteException)
			if !ok {
				panic(err)
			}
			if !writeErr.HasErrorCode(11000) {
				panic(err)
			}
		}
	}
	return nil
}

func DBGetTokenCount() (int64, error) {
	filter := bson.M{"isnft": bson.M{operator.Eq: false}}
	ctx, _ := context.WithTimeout(context.Background(), time.Duration(5)*shared.DB_OPERATION_TIMEOUT)
	c, err := mgm.Coll(&shared.TokenInfoData{}).Collection.CountDocuments(ctx, filter)
	if err != nil {
		return c, err
	}
	return c, nil
}

func DBGetTokenInfo() ([]shared.TokenInfoData, error) {
	list := []shared.TokenInfoData{}
	filter := bson.M{"isnft": bson.M{operator.Eq: false}}
	err := mgm.Coll(&shared.TokenInfoData{}).SimpleFind(&list, filter, nil)
	if err != nil {
		return nil, err
	}
	return list, nil
}

func DBGetNFTCount() (int64, error) {
	filter := bson.M{"isnft": bson.M{operator.Eq: true}}
	ctx, _ := context.WithTimeout(context.Background(), time.Duration(5)*shared.DB_OPERATION_TIMEOUT)
	c, err := mgm.Coll(&shared.TokenInfoData{}).Collection.CountDocuments(ctx, filter)
	if err != nil {
		return c, err
	}
	return c, nil
}
func DBGetNFTInfo() ([]shared.TokenInfoData, error) {
	list := []shared.TokenInfoData{}
	filter := bson.M{"isnft": bson.M{operator.Eq: true}}
	err := mgm.Coll(&shared.TokenInfoData{}).SimpleFind(&list, filter, nil)
	if err != nil {
		return nil, err
	}
	return list, nil
}
