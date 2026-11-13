package product

import (
  v1 "github.com/tiidiio/223Crash/contracts/product/v1"
  "github.com/tiidiio/223Crash/internal/admin"
  "github.com/tiidiio/223Crash/internal/engine"
  "github.com/tiidiio/223Crash/internal/productadapter"
  mongo "go.mongodb.org/mongo-driver/v2/mongo"
)

func NewGameEngine(db *mongo.Database) v1.GameEngine {
  ns := admin.NewMongoNonceStore(db)
  cs := admin.NewMongoChainStore(db)
  cfg := engine.DefaultConfig()
  cfg.NonceStore = ns
  cfg.ChainStore = cs
  inner, _ := engine.NewCrashEngine(cfg)
  return productadapter.NewCrashAdapter(inner)
}

func NewGameEngineWithInner(db *mongo.Database) (v1.GameEngine, *engine.CrashEngine) {
  ns := admin.NewMongoNonceStore(db)
  cs := admin.NewMongoChainStore(db)
  cfg := engine.DefaultConfig()
  cfg.NonceStore = ns
  cfg.ChainStore = cs
  inner, _ := engine.NewCrashEngine(cfg)
  adapter := productadapter.NewCrashAdapter(inner)
  return adapter, inner
}
