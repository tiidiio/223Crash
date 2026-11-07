// Package provider (côté 223CRASH) ne définit plus aucun type : le vocabulaire d'identité
// canonique appartient au Provider Core (github.com/tiidiio/223Gaming/internal/provider).
//
// Ces alias (type X = core.X, pas de redéfinition) rendent GameID, OperatorID, PlayerID,
// TransactionID, IdempotencyKey et ExternalRef strictement identiques aux types du Core et
// du Ledger canonique : aucune conversion n'est nécessaire aux frontières wallet/recovery/
// gateway. Ce fichier est une couche de compatibilité, supprimable une fois les imports du
// produit migrés directement vers le Core.
package provider

import coreprovider "github.com/tiidiio/223Gaming/internal/provider"

type (
	GameID         = coreprovider.GameID
	OperatorID     = coreprovider.OperatorID
	PlayerID       = coreprovider.PlayerID
	TransactionID  = coreprovider.TransactionID
	IdempotencyKey = coreprovider.IdempotencyKey
	ExternalRef    = coreprovider.ExternalRef
)
