package main

import (
	"encoding/json"
	"flag"
	"io/ioutil"
	"log"
)

var API_ADDRESS string
var serviceCfg Config

type Config struct {
	APIPort               int    `json:"apiport"`
	ChainDataFolder       string `json:"chaindata"`
	MaxConcurrentOTACheck int    `json:"maxconcurrentotacheck"`
	Mode                  string `json:"mode"`
	MongoAddress          string `json:"mongo"`
}

func init() {
	serviceCfg.APIPort = DefaultAPIPort
	serviceCfg.ChainDataFolder = DefaultChainFolder
	serviceCfg.MaxConcurrentOTACheck = DefaultMaxConcurrentOTACheck
	serviceCfg.Mode = DefaultMode
	serviceCfg.MongoAddress = DefaultMongoAddress
}

func readConfigAndArg() {
	data, err := ioutil.ReadFile("./cfg.json")
	if err != nil {
		log.Println(err)
		// return
	}
	var tempCfg Config
	if data != nil {
		err = json.Unmarshal(data, &tempCfg)
		if err != nil {
			panic(err)
		}
	}

	// overwrite with args
	argMode := flag.String("mode", DefaultMode, "set worker mode")
	argPort := flag.Int("port", DefaultAPIPort, "set worker port")
	argMongo := flag.String("mongo", DefaultMongoAddress, "set mongo address")
	argMaxConcurrentOTACheck := flag.Int("maxotacheck", DefaultMaxConcurrentOTACheck, "set MaxConcurrentOTACheck")
	argChain := flag.String("chain", DefaultChainFolder, "set chain folder")
	flag.Parse()
	if tempCfg.APIPort == 0 {
		tempCfg.APIPort = *argPort
	}
	if tempCfg.ChainDataFolder == "" {
		tempCfg.ChainDataFolder = *argChain
	}
	if tempCfg.MaxConcurrentOTACheck == 0 {
		tempCfg.MaxConcurrentOTACheck = *argMaxConcurrentOTACheck
	}
	if tempCfg.Mode == "" {
		tempCfg.Mode = *argMode
	}
	if tempCfg.MongoAddress == "" {
		tempCfg.MongoAddress = *argMongo
	}

	serviceCfg = tempCfg
}
