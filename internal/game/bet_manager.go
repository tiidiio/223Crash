package game

import (
	"errors"
	"math"
	"sync"
	"time"
)

var (
	ErrBetAlreadyExists  = errors.New("bet already exists for this panel")
	ErrBetNotFound       = errors.New("bet not found")
	ErrInvalidUserID     = errors.New("invalid user id")
	ErrInvalidBetAmount  = errors.New("invalid bet amount")
	ErrInvalidCashout    = errors.New("invalid cashout")
	ErrBettingClosed     = errors.New("betting is closed")
	ErrAlreadyCashedOut  = errors.New("bet already cashed out")
	ErrBetAlreadyLost    = errors.New("bet already lost")
	ErrCashoutTooLow     = errors.New("cashout multiplier is below 1.00")
	ErrRoundNotRunning   = errors.New("round is not running")
	ErrInvalidMultiplier = errors.New("invalid multiplier")
	ErrInvalidPayout     = errors.New("invalid payout")
	ErrInvalidPanel      = errors.New("invalid bet panel")
)

type BetState string

const (
	BetPending   BetState = "PENDING"
	BetCashedOut BetState = "CASHED_OUT"
	BetLost      BetState = "LOST"
)

type BetPanel uint8

const (
	Panel1 BetPanel = 1
	Panel2 BetPanel = 2
)

func (p BetPanel) IsValid() bool {
	return p == Panel1 || p == Panel2
}

func (p BetPanel) String() string {
	switch p {
	case Panel1:
		return "PANEL_1"
	case Panel2:
		return "PANEL_2"
	default:
		return "UNKNOWN"
	}
}

type Bet struct {
	ID            string
	RoundID       string
	UserID        string
	Panel         BetPanel
	Amount        int64
	CashoutTarget float64
	State         BetState
	CashoutAt     time.Time
	CashoutMulti  float64
	Payout        int64
}

type panelKey struct {
	UserID string
	Panel  BetPanel
}

type BetManager struct {
	mu sync.RWMutex

	nextID uint64

	roundID     string
	bettingOpen bool
	running     bool

	bets map[string]*Bet

	activePanels map[panelKey]string
}

func NewBetManager() *BetManager {
	return &BetManager{
		bets:         make(map[string]*Bet),
		activePanels: make(map[panelKey]string),
	}
}

func (bm *BetManager) OpenRound(roundID string) error {
	if roundID == "" {
		return ErrInvalidState
	}

	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID != "" && bm.roundID != roundID {
		return ErrInvalidState
	}

	bm.roundID = roundID
	bm.bettingOpen = true
	bm.running = false

	return nil
}

func (bm *BetManager) CloseBetting() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID == "" {
		return ErrNoActiveRound
	}

	if !bm.bettingOpen {
		return ErrBettingClosed
	}

	bm.bettingOpen = false

	return nil
}

func (bm *BetManager) StartRound() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID == "" {
		return ErrNoActiveRound
	}

	if bm.bettingOpen {
		return ErrInvalidState
	}

	if bm.running {
		return ErrRoundNotRunning
	}

	bm.running = true

	return nil
}

func (bm *BetManager) PlaceBet(
	userID string,
	panel BetPanel,
	amount int64,
	cashoutTarget float64,
) (*Bet, error) {

	if userID == "" {
		return nil, ErrInvalidUserID
	}

	if !panel.IsValid() {
		return nil, ErrInvalidPanel
	}

	if amount <= 0 {
		return nil, ErrInvalidBetAmount
	}

	if !isValidMultiplier(cashoutTarget) {
		return nil, ErrInvalidCashout
	}

	cashoutTarget = normalizeMultiplier(cashoutTarget)

	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID == "" {
		return nil, ErrNoActiveRound
	}

	if !bm.bettingOpen {
		return nil, ErrBettingClosed
	}

	key := panelKey{
		UserID: userID,
		Panel:  panel,
	}

	if _, exists := bm.activePanels[key]; exists {
		return nil, ErrBetAlreadyExists
	}

	bm.nextID++

	betID := "bet-" + uint64ToString(bm.nextID)

	bet := &Bet{
		ID:            betID,
		RoundID:       bm.roundID,
		UserID:        userID,
		Panel:         panel,
		Amount:        amount,
		CashoutTarget: cashoutTarget,
		State:         BetPending,
	}

	bm.bets[betID] = bet
	bm.activePanels[key] = betID

	return cloneBet(bet), nil
}

// Cashout reçoit UNIQUEMENT le bet ID et le multiplicateur
// fourni par le moteur autoritaire.
//
// Le client ne doit jamais fournir currentMultiplier directement.
//
// Le CrashEngine appelle cette méthode avec son propre
// multiplicateur courant.
func (bm *BetManager) Cashout(
	betID string,
	currentMultiplier float64,
	now time.Time,
) (*Bet, error) {

	if betID == "" {
		return nil, ErrBetNotFound
	}

	if !isValidMultiplier(currentMultiplier) {
		return nil, ErrInvalidMultiplier
	}

	currentMultiplier = normalizeMultiplier(currentMultiplier)

	bm.mu.Lock()
	defer bm.mu.Unlock()

	if !bm.running {
		return nil, ErrRoundNotRunning
	}

	bet, ok := bm.bets[betID]
	if !ok {
		return nil, ErrBetNotFound
	}

	if bet.State == BetCashedOut {
		return nil, ErrAlreadyCashedOut
	}

	if bet.State == BetLost {
		return nil, ErrBetAlreadyLost
	}

	if currentMultiplier < 1.00 {
		return nil, ErrCashoutTooLow
	}

	payout, err := calculatePayout(bet.Amount, currentMultiplier)
	if err != nil {
		return nil, err
	}

	bet.State = BetCashedOut
	bet.CashoutAt = now
	bet.CashoutMulti = currentMultiplier
	bet.Payout = payout

	delete(
		bm.activePanels,
		panelKey{
			UserID: bet.UserID,
			Panel:  bet.Panel,
		},
	)

	return cloneBet(bet), nil
}

func (bm *BetManager) Crash(now time.Time) ([]Bet, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID == "" {
		return nil, ErrNoActiveRound
	}

	if !bm.running {
		return nil, ErrRoundNotRunning
	}

	bm.running = false

	lost := make([]Bet, 0)

	for _, bet := range bm.bets {
		if bet.RoundID != bm.roundID {
			continue
		}

		if bet.State != BetPending {
			continue
		}

		bet.State = BetLost
		bet.CashoutAt = now
		bet.CashoutMulti = 0
		bet.Payout = 0

		delete(
			bm.activePanels,
			panelKey{
				UserID: bet.UserID,
				Panel:  bet.Panel,
			},
		)

		lost = append(lost, *bet)
	}

	return lost, nil
}

func (bm *BetManager) GetBet(betID string) (Bet, error) {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	bet, ok := bm.bets[betID]
	if !ok {
		return Bet{}, ErrBetNotFound
	}

	return *bet, nil
}

func (bm *BetManager) GetPanelBet(
	userID string,
	panel BetPanel,
) (Bet, error) {

	if userID == "" {
		return Bet{}, ErrInvalidUserID
	}

	if !panel.IsValid() {
		return Bet{}, ErrInvalidPanel
	}

	bm.mu.RLock()
	defer bm.mu.RUnlock()

	betID, ok := bm.activePanels[panelKey{
		UserID: userID,
		Panel:  panel,
	}]

	if !ok {
		return Bet{}, ErrBetNotFound
	}

	bet, ok := bm.bets[betID]
	if !ok {
		return Bet{}, ErrBetNotFound
	}

	return *bet, nil
}

func (bm *BetManager) RoundBets() []Bet {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	result := make([]Bet, 0)

	for _, bet := range bm.bets {
		if bet.RoundID != bm.roundID {
			continue
		}

		result = append(result, *bet)
	}

	return result
}

func (bm *BetManager) UserRoundBets(userID string) []Bet {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	result := make([]Bet, 0)

	for _, bet := range bm.bets {
		if bet.RoundID != bm.roundID {
			continue
		}

		if bet.UserID != userID {
			continue
		}

		result = append(result, *bet)
	}

	return result
}

func (bm *BetManager) ResetRound() error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.roundID == "" {
		return ErrNoActiveRound
	}

	if bm.running {
		return ErrRoundNotRunning
	}

	bm.roundID = ""
	bm.bettingOpen = false
	bm.running = false

	bm.bets = make(map[string]*Bet)
	bm.activePanels = make(map[panelKey]string)

	return nil
}

func (bm *BetManager) RoundID() string {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	return bm.roundID
}

func (bm *BetManager) IsBettingOpen() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	return bm.bettingOpen
}

func (bm *BetManager) IsRunning() bool {
	bm.mu.RLock()
	defer bm.mu.RUnlock()

	return bm.running
}

func calculatePayout(amount int64, multiplier float64) (int64, error) {
	if amount <= 0 {
		return 0, ErrInvalidBetAmount
	}

	if !isValidMultiplier(multiplier) {
		return 0, ErrInvalidMultiplier
	}

	multiplier = normalizeMultiplier(multiplier)

	scaled := int64(math.Round(multiplier * 100))

	if scaled <= 0 {
		return 0, ErrInvalidMultiplier
	}

	const maxInt64 = int64(^uint64(0) >> 1)

	if amount > maxInt64/scaled {
		return 0, ErrInvalidPayout
	}

	payout := (amount * scaled) / 100

	if payout < amount {
		return 0, ErrInvalidPayout
	}

	return payout, nil
}

func isValidMultiplier(multiplier float64) bool {
	return !math.IsNaN(multiplier) &&
		!math.IsInf(multiplier, 0) &&
		multiplier >= 1.00 &&
		multiplier <= 50000.00
}

func normalizeMultiplier(multiplier float64) float64 {
	return math.Floor(multiplier*100) / 100
}

func cloneBet(bet *Bet) *Bet {
	if bet == nil {
		return nil
	}

	copy := *bet
	return &copy
}

func uint64ToString(value uint64) string {
	if value == 0 {
		return "0"
	}

	var buf [20]byte
	i := len(buf)

	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}

	return string(buf[i:])
}
