// mongoinspect — diagnostic ponctuel : liste tous les documents crash_broadcast
// pour un roundId donné, pour détecter une collision d'ID entre redémarrages serveur.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: mongoinspect <roundId>")
		os.Exit(1)
	}
	roundID := os.Args[1]

	uri := os.Getenv("MONGO_URI")
	db := os.Getenv("MONGO_DB")
	if uri == "" || db == "" {
		fmt.Println("MONGO_URI / MONGO_DB manquants")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer client.Disconnect(context.Background())

	col := client.Database(db).Collection("crash_broadcast")

	filter := bson.D{
		{Key: "type", Value: "CRASH"},
		{Key: "data.roundId", Value: roundID},
	}

	cur, err := col.Find(ctx, filter)
	if err != nil {
		fmt.Println("find:", err)
		os.Exit(1)
	}
	defer cur.Close(ctx)

	count := 0
	for cur.Next(ctx) {
		count++
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			continue
		}
		fmt.Printf("--- doc #%d ---\n%+v\n\n", count, doc)
	}

	fmt.Printf("TOTAL documents CRASH pour roundId=%s : %d\n", roundID, count)
	if count > 1 {
		fmt.Println("⚠️  COLLISION CONFIRMÉE : plusieurs rounds distincts partagent le même ID.")
	}
}
