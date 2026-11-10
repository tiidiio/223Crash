package main

// engineID est l'identité d'un joueur côté moteur de jeu et table des sessions (gw.clients).
//
// Un playerID n'est unique que chez son opérateur : deux opérateurs peuvent avoir un joueur « 123 ».
// La clé composée operatorID:playerID empêche qu'ils partagent session, paris et panels. Elle est
// injective : wsIDPattern interdit ':' dans le Subject du JWT, donc le dernier ':' sépare sans
// ambiguïté l'opérateur du joueur, quel que soit le contenu de operatorID.
//
// Wallet, Ledger, callbacks et messages vers le client continuent d'utiliser operatorID et
// playerID bruts : cette clé ne sort jamais du processus.
func (c *client) engineID() string {
	return c.operatorID + ":" + c.playerID
}
