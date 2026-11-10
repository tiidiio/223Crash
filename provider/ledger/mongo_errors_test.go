package ledger

import (
	"errors"
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func keyPatternRaw(t *testing.T, fields bson.D) bson.Raw {
	t.Helper()
	b, err := bson.Marshal(bson.D{{Key: "keyPattern", Value: fields}})
	if err != nil {
		t.Fatalf("marshal keyPattern: %v", err)
	}
	return bson.Raw(b)
}

func dupException(code int, msg string, raw bson.Raw) mongo.WriteException {
	return mongo.WriteException{
		WriteErrors: mongo.WriteErrors{{Code: code, Message: msg, Raw: raw}},
	}
}

func TestIsIdempotencyDuplicate(t *testing.T) {
	idemMsg := "E11000 duplicate key error collection: db.provider_ledger_entries index: " +
		idempotencyIndexName + " dup key: { operatorId: \"op\", idempotencyKey: \"k\" }"
	txnMsg := "E11000 duplicate key error collection: db.provider_ledger_entries index: " +
		"transactionId_1 dup key: { transactionId: \"txn_x\" }"
	compound := bson.D{{Key: "operatorId", Value: int32(1)}, {Key: "idempotencyKey", Value: int32(1)}}

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"erreur non mongo", errors.New("boom"), false},
		{"message avec nom d'index, sans keyPattern", dupException(11000, idemMsg, nil), true},
		{"keyPattern composé, message générique", dupException(11000, "E11000 duplicate key", keyPatternRaw(t, compound)), true},
		{"keyPattern composé en int64", dupException(11000, "E11000 duplicate key",
			keyPatternRaw(t, bson.D{{Key: "operatorId", Value: int64(1)}, {Key: "idempotencyKey", Value: int64(1)}})), true},
		{"erreur enveloppée", fmt.Errorf("wrap: %w", dupException(11000, idemMsg, nil)), true},
		{"collision transactionId", dupException(11000, txnMsg, keyPatternRaw(t, bson.D{{Key: "transactionId", Value: int32(1)}})), false},
		{"code non duplicate", dupException(121, idemMsg, nil), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isIdempotencyDuplicate(c.err); got != c.want {
				t.Fatalf("isIdempotencyDuplicate = %v, attendu %v", got, c.want)
			}
		})
	}
}

func TestIsOne(t *testing.T) {
	for _, v := range []any{int32(1), int64(1), float64(1)} {
		if !isOne(v) {
			t.Fatalf("isOne(%T(1)) devrait être vrai", v)
		}
	}
	for _, v := range []any{int32(-1), int64(2), float64(0), "1", nil} {
		if isOne(v) {
			t.Fatalf("isOne(%v) devrait être faux", v)
		}
	}
}
