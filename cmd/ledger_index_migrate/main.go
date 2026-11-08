// ledger_index_migrate — migration d'index Atlas, usage unique, protégée.
//
// Supprime UNIQUEMENT l'index "idempotencyKey_1" (clé exacte {idempotencyKey:1},
// unique, sans filtre) de provider_ledger_entries, pour permettre au serveur de
// créer lui-même l'index (operatorId, idempotencyKey).
// Aucun document n'est lu en détail, modifié ou supprimé.
// Sans -apply : simulation en lecture seule.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	collName  = "provider_ledger_entries"
	legacyIdx = "idempotencyKey_1"
)

type indexSpec struct {
	Name    string   `bson:"name"`
	Key     bson.D   `bson:"key"`
	Unique  bool     `bson:"unique"`
	Sparse  bool     `bson:"sparse"`
	Partial bson.Raw `bson:"partialFilterExpression,omitempty"`
}

func d(kv ...any) bson.D {
	out := make(bson.D, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, bson.E{Key: kv[i].(string), Value: kv[i+1]})
	}
	return out
}

func die(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(code)
}

func numOne(v any) bool {
	switch x := v.(type) {
	case int32:
		return x == 1
	case int64:
		return x == 1
	case float64:
		return x == 1
	}
	return false
}

func isLegacyShape(s indexSpec) bool {
	return s.Name == legacyIdx && len(s.Key) == 1 && s.Key[0].Key == "idempotencyKey" &&
		numOne(s.Key[0].Value) && s.Unique && !s.Sparse && len(s.Partial) == 0
}

func isPerOperator(s indexSpec) bool {
	if len(s.Key) != 2 || !s.Unique {
		return false
	}
	a, b := s.Key[0].Key, s.Key[1].Key
	return (a == "operatorId" && b == "idempotencyKey") || (a == "idempotencyKey" && b == "operatorId")
}

func listIndexes(ctx context.Context, c *mongo.Collection) ([]bson.Raw, []indexSpec, error) {
	cur, err := c.Indexes().List(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cur.Close(ctx)
	var raws []bson.Raw
	var specs []indexSpec
	for cur.Next(ctx) {
		raw := append(bson.Raw(nil), cur.Current...)
		var s indexSpec
		if err := bson.Unmarshal(raw, &s); err != nil {
			return nil, nil, err
		}
		raws = append(raws, raw)
		specs = append(specs, s)
	}
	return raws, specs, cur.Err()
}

func printSpecs(specs []indexSpec) {
	for _, s := range specs {
		fmt.Printf("    name=%-32s key=%v unique=%v\n", s.Name, s.Key, s.Unique)
	}
}

func portsClosed() {
	for _, p := range []string{"8080", "3000", "8090"} {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+p, 2*time.Second); err == nil {
			c.Close()
			die(4, "REFUS: le port %s est ouvert (serveur actif). Arrêtez 223CRASH avant la migration.", p)
		}
	}
}

func main() {
	apply := flag.Bool("apply", false, "applique la migration (confirmation interactive requise)")
	flag.Parse()

	_ = godotenv.Load(".env")
	uri, dbn := os.Getenv("MONGO_URI"), os.Getenv("MONGO_DB")
	if uri == "" || dbn == "" {
		die(2, "MONGO_URI ou MONGO_DB manquant (lancer depuis ~/223Gaming/223Crash)")
	}
	if *apply {
		portsClosed()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		die(1, "connexion: %v", err)
	}
	defer client.Disconnect(context.Background())
	if err := client.Ping(ctx, nil); err != nil {
		die(1, "ping Atlas ECHEC: %v", err)
	}
	fmt.Println("ping Atlas: OK")

	c := client.Database(dbn).Collection(collName)
	raws, specs, err := listIndexes(ctx, c)
	if err != nil {
		die(1, "liste des index: %v", err)
	}
	total, err := c.CountDocuments(ctx, bson.D{})
	if err != nil {
		die(1, "comptage: %v", err)
	}
	fmt.Printf("base=%s collection=%s documents=%d\n", dbn, collName, total)
	printSpecs(specs)

	var legacy *indexSpec
	perOp := false
	for i := range specs {
		if specs[i].Name == legacyIdx {
			legacy = &specs[i]
		}
		if isPerOperator(specs[i]) {
			perOp = true
		}
	}
	fmt.Printf("index unique par opérateur (operatorId, idempotencyKey): %v\n", map[bool]string{true: "PRÉSENT", false: "ABSENT"}[perOp])

	if legacy == nil {
		fmt.Println("Aucun index legacy idempotencyKey_1 : rien à faire.")
		return
	}
	if !isLegacyShape(*legacy) {
		die(5, "REFUS: idempotencyKey_1 n'a pas la forme attendue ({idempotencyKey:1}, unique, sans filtre). Aucune action.")
	}

	missing, err := c.CountDocuments(ctx, d("$or", bson.A{
		d("operatorId", d("$exists", false)),
		d("operatorId", nil),
		d("operatorId", ""),
	}))
	if err != nil {
		die(1, "contrôle operatorId: %v", err)
	}
	pipe := mongo.Pipeline{
		{{Key: "$group", Value: d("_id", d("o", "$operatorId", "k", "$idempotencyKey"), "n", d("$sum", 1))}},
		{{Key: "$match", Value: d("n", d("$gt", 1))}},
		{{Key: "$limit", Value: 1}},
	}
	acur, err := c.Aggregate(ctx, pipe)
	if err != nil {
		die(1, "contrôle doublons: %v", err)
	}
	dups := acur.Next(ctx)
	acur.Close(ctx)
	fmt.Printf("contrôles: documents sans operatorId=%d, doublon (operatorId,idempotencyKey)=%v\n", missing, dups)
	if missing > 0 || dups {
		die(6, "REFUS: les contrôles de sécurité échouent. Aucune action.")
	}

	// Sauvegarde des définitions d'index (jamais écrasée).
	ts := time.Now().Format("20060102-150405")
	if err := os.MkdirAll("backups", 0o700); err != nil {
		die(1, "backups/: %v", err)
	}
	bpath := filepath.Join("backups", fmt.Sprintf("index-migration-%s-%s.json", collName, ts))
	f, err := os.OpenFile(bpath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		die(1, "sauvegarde: %v", err)
	}
	var parts []string
	for _, r := range raws {
		j, err := bson.MarshalExtJSON(r, true, false)
		if err != nil {
			f.Close()
			die(1, "sauvegarde: %v", err)
		}
		parts = append(parts, string(j))
	}
	fmt.Fprintf(f, "{\"database\":%q,\"collection\":%q,\"timestamp\":%q,\"documents\":%d,\n \"rollback\":%q,\n \"indexes\":[\n  %s\n ]}\n",
		dbn, collName, ts, total,
		"db."+collName+".createIndex({idempotencyKey:1},{unique:true,name:\""+legacyIdx+"\"})",
		strings.Join(parts, ",\n  "))
	if err := f.Close(); err != nil {
		die(1, "sauvegarde: %v", err)
	}
	fmt.Println("sauvegarde des index:", bpath)

	if !*apply {
		fmt.Printf("\nSIMULATION: l'index %q serait supprimé de %s (aucun document touché).\n", legacyIdx, collName)
		fmt.Println("Pour appliquer : go run ./cmd/ledger_index_migrate -apply")
		return
	}

	expected := "SUPPRIMER " + legacyIdx + " SUR " + collName
	fmt.Printf("\nPour confirmer, tapez exactement :\n  %s\n> ", expected)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != expected {
		die(7, "Annulé : confirmation incorrecte. Aucune modification.")
	}
	portsClosed()

	dctx, dcancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer dcancel()
	if err := c.Indexes().DropOne(dctx, legacyIdx); err != nil {
		die(1, "DropOne: %v", err)
	}
	fmt.Printf("Index %q supprimé.\n", legacyIdx)

	_, after, err := listIndexes(dctx, c)
	if err != nil {
		die(1, "relecture index: %v", err)
	}
	totalAfter, _ := c.CountDocuments(dctx, bson.D{})
	fmt.Printf("documents avant=%d après=%d\n", total, totalAfter)
	printSpecs(after)
	fmt.Println("\nÉtape suivante : démarrer 223CRASH ; le serveur recrée lui-même l'index (operatorId, idempotencyKey).")
	fmt.Println("Rollback documenté dans", bpath)
}
