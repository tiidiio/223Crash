package wallet

import "time"

// CallbackType identifie le sens du mouvement d'argent vis-à-vis de l'opérateur.
type CallbackType string

const (
	CallbackDebit    CallbackType = "DEBIT"
	CallbackCredit   CallbackType = "CREDIT"
	CallbackRollback CallbackType = "ROLLBACK"
)

// CallbackStatus est l'état d'un callback dans la queue de retry Mongo.
type CallbackStatus string

const (
	StatusPending CallbackStatus = "pending"
	StatusDone    CallbackStatus = "done"
	StatusDead    CallbackStatus = "dead"
)

// Callback représente un mouvement DEBIT/CREDIT/ROLLBACK à envoyer (ou déjà envoyé)
// à l'endpoint wallet de l'opérateur. Amount est en unité mineure (centimes), jamais
// en float, pour éviter toute dérive d'arrondi sur de l'argent réel.
type Callback struct {
	ID             string       `bson:"_id"            json:"id"`
	Type           CallbackType `bson:"type"            json:"type"`
	OperatorID     string       `bson:"operatorId"      json:"operatorId"`
	PlayerID       string       `bson:"playerId"        json:"playerId"`
	RoundID        string       `bson:"roundId"         json:"roundId"`
	BetID          string       `bson:"betId"           json:"betId"`
	Amount         int64        `bson:"amount"          json:"amount"`
	IdempotencyKey string       `bson:"idempotencyKey"  json:"idempotencyKey"`
	Status         CallbackStatus `bson:"status"        json:"status"`
	Attempts       int          `bson:"attempts"        json:"attempts"`
	DueAt          time.Time    `bson:"dueAt"           json:"-"`
	CreatedAt      time.Time    `bson:"createdAt"       json:"-"`
	LastError      string       `bson:"lastError,omitempty" json:"-"`
}

// CallbackResponse est ce que l'endpoint opérateur doit renvoyer après traitement.
type CallbackResponse struct {
	Balance int64 `json:"balance"`
}
