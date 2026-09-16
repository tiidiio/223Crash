// Package provider définit le vocabulaire d'identité canonique partagé par tous les
// sous-packages du 223GAMING Provider Core (Ledger, Wallet Adapter, Settlement, Recovery,
// Game Registry, etc.). Ces types sont volontairement opaques : aucun sous-package ne doit
// réinterpréter leur contenu, et le Provider Core ne doit jamais brancher de logique
// conditionnelle sur une valeur de GameID — un Game Product est un simple identifiant de
// routage, jamais une branche de code.
//
// Ce package ne contient aucune logique métier, aucune règle de jeu, et aucune dépendance
// vers internal/engine, internal/game ou internal/wallet — uniquement des définitions de type.
package provider

// GameID identifie un Game Product (ex: "223crash", "223dice", "223mines") de façon opaque.
// Ne jamais brancher de switch/if sur cette valeur dans le Provider Core.
type GameID string

// OperatorID identifie l'opérateur B2B propriétaire du wallet joueur et de la relation joueur.
type OperatorID string

// PlayerID identifie le joueur côté opérateur — 223GAMING ne détient aucune donnée de compte
// joueur au-delà de cet identifiant opaque nécessaire au routage des mouvements financiers.
type PlayerID string

// TransactionID identifie de façon unique une entrée de Ledger, générée par
// internal/provider/ledger, puis propagée au Wallet Adapter pour corréler chaque Callback
// avec sa transaction Ledger d'origine. Distincte d'IdempotencyKey : plusieurs tentatives avec
// la même IdempotencyKey résolvent toujours vers le même TransactionID (la première tentative
// gagne).
type TransactionID string

// IdempotencyKey protège une opération contre le rejeu (retry réseau, double appel applicatif).
// Fournie par l'appelant, unique en base au niveau du Ledger.
type IdempotencyKey string

// ExternalRef est la référence métier fournie par le Game Product (ex: BetID, RoundID, ou une
// combinaison) — opaque pour le Provider Core, jamais interprétée, seulement stockée pour
// traçabilité. Distincte de TransactionID (identité interne du Ledger) et d'IdempotencyKey
// (clé de dédup).
type ExternalRef string
