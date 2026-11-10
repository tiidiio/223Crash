package engine

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tiidiio/223Gaming/internal/game"
)

const testGrowth = 0.00006

// noAuto = cible sentinelle de la gateway (noAutoCashoutSentinel) : jamais atteignable (> MaxCrashPoint).
const noAuto = 50000.00

// newLiveEngine prépare un moteur dont le round courant est RUNNING depuis `ago`, avec crashDur donné.
func newLiveEngine(t *testing.T, ago, crashDur time.Duration, bets ...struct {
	user   string
	panel  game.BetPanel
	amount int64
	target float64
}) (*CrashEngine, []game.Bet) {
	t.Helper()
	cfg := DefaultConfig()
	e, err := NewCrashEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.bets.OpenRound("r1"); err != nil {
		t.Fatal(err)
	}
	var placed []game.Bet
	for _, b := range bets {
		p, err := e.PlaceBet(b.user, b.panel, b.amount, b.target)
		if err != nil {
			t.Fatal(err)
		}
		placed = append(placed, *p)
	}
	if err := e.bets.CloseBetting(); err != nil {
		t.Fatal(err)
	}
	if err := e.bets.StartRound(); err != nil {
		t.Fatal(err)
	}
	e.phaseMu.Lock()
	e.roundStartedAt = time.Now().Add(-ago)
	e.crashDur = crashDur
	e.live = true
	e.phaseMu.Unlock()
	return e, placed
}

type spec = struct {
	user   string
	panel  game.BetPanel
	amount int64
	target float64
}

func TestCrashDuration_ExactBoundary(t *testing.T) {
	for _, cp := range []float64{1.01, 1.5, 1.94, 2, 10, 100, 777.77, MaxCrashPoint} {
		d := crashDuration(cp, testGrowth)
		if multiplierAt(d, testGrowth) < cp {
			t.Errorf("cp=%v : multiplicateur à crashDur (%v) < cp", cp, d)
		}
		if multiplierAt(d-1, testGrowth) >= cp {
			t.Errorf("cp=%v : crashDur n'est pas minimal (d-1ns atteint déjà cp)", cp)
		}
	}
	if d := crashDuration(1.00, testGrowth); d != 0 {
		t.Errorf("crash immédiat : crashDur = %v, attendu 0", d)
	}
	// Valeurs de référence (spec) : 2x ≈ 11,55 s ; 100x ≈ 76,75 s ; 5000x ≈ 141,95 s.
	ref := map[float64]float64{2: 11.55, 100: 76.75, MaxCrashPoint: 141.95}
	for cp, sec := range ref {
		if got := crashDuration(cp, testGrowth).Seconds(); math.Abs(got-sec) > 0.01 {
			t.Errorf("cp=%v : %.3fs, attendu %.2fs", cp, got, sec)
		}
	}
}

func TestCashout_BeforeCrashPaysAtServerMultiplier(t *testing.T) {
	e, bets := newLiveEngine(t, time.Second, 10*time.Second, spec{"u1", 1, 1000, noAuto})
	got, err := e.Cashout(bets[0].ID)
	if err != nil {
		t.Fatalf("cashout valide refusé : %v", err)
	}
	// e^(0.06) = 1.0618 -> tronqué 1.06
	if got.CashoutMulti < 1.05 || got.CashoutMulti > 1.07 || got.Payout < 1050 || got.Payout > 1070 {
		t.Fatalf("paiement incohérent : multi=%v payout=%v", got.CashoutMulti, got.Payout)
	}
}

func TestCashout_AtOrAfterCrashBoundaryRejected(t *testing.T) {
	for _, ago := range []time.Duration{10 * time.Second, 10*time.Second + time.Millisecond, time.Hour} {
		e, bets := newLiveEngine(t, ago, 10*time.Second, spec{"u1", 1, 1000, noAuto})
		if _, err := e.Cashout(bets[0].ID); !errors.Is(err, game.ErrRoundNotRunning) {
			t.Errorf("ago=%v : err=%v, attendu ErrRoundNotRunning", ago, err)
		}
		b, _ := e.bets.GetBet(bets[0].ID)
		if b.State != game.BetPending || b.Payout != 0 {
			t.Errorf("ago=%v : la mise ne doit pas être modifiée (%+v)", ago, b)
		}
	}
}

func TestCashout_AfterCrashRejectedAndDoubleCashout(t *testing.T) {
	e, bets := newLiveEngine(t, time.Second, 10*time.Second, spec{"u1", 1, 1000, noAuto}, spec{"u2", 1, 500, noAuto})
	if _, err := e.Cashout(bets[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Cashout(bets[0].ID); !errors.Is(err, game.ErrAlreadyCashedOut) {
		t.Errorf("double cashout : err=%v, attendu ErrAlreadyCashedOut", err)
	}
	_, _, lost, err := e.crashBets(5.0)
	if err != nil {
		t.Fatal(err)
	}
	if len(lost) != 1 || lost[0].ID != bets[1].ID {
		t.Fatalf("seule la mise non encaissée doit être perdue : %+v", lost)
	}
	if _, err := e.Cashout(bets[1].ID); !errors.Is(err, game.ErrRoundNotRunning) {
		t.Errorf("cashout après crash : err=%v, attendu ErrRoundNotRunning", err)
	}
}

func TestAutoCashout_PaidAtExactTarget(t *testing.T) {
	e, bets := newLiveEngine(t, time.Second, 10*time.Second,
		spec{"u1", 1, 1000, 2.00}, // atteinte
		spec{"u2", 1, 1000, 3.00}, // non atteinte au multiplicateur 2.5
		spec{"u3", 1, 1000, 2.5},  // exactement égale au multiplicateur courant
	)
	e.phaseMu.Lock()
	paid := e.sweepAutoLocked(2.5, time.Now())
	e.phaseMu.Unlock()
	if len(paid) != 2 {
		t.Fatalf("2 mises attendues, obtenu %d", len(paid))
	}
	for _, p := range paid {
		switch p.ID {
		case bets[0].ID:
			if p.CashoutMulti != 2.00 || p.Payout != 2000 {
				t.Errorf("cible 2.00 : multi=%v payout=%v", p.CashoutMulti, p.Payout)
			}
		case bets[2].ID:
			if p.CashoutMulti != 2.50 || p.Payout != 2500 {
				t.Errorf("cible 2.50 : multi=%v payout=%v", p.CashoutMulti, p.Payout)
			}
		default:
			t.Errorf("mise inattendue payée : %+v", p)
		}
	}
}

func TestAutoCashout_TargetEqualToCrashPointWins(t *testing.T) {
	// Règle publique : gain si crash >= cible. Cible 2.00 avec crash exactement à 2.00 => payée 2000.
	e, bets := newLiveEngine(t, time.Second, 10*time.Second,
		spec{"u1", 1, 1000, 2.00}, spec{"u2", 1, 1000, 2.01})
	_, auto, lost, err := e.crashBets(2.00)
	if err != nil {
		t.Fatal(err)
	}
	if len(auto) != 1 || auto[0].ID != bets[0].ID || auto[0].Payout != 2000 {
		t.Fatalf("cible == crash doit être payée à la cible : %+v", auto)
	}
	if len(lost) != 1 || lost[0].ID != bets[1].ID {
		t.Fatalf("cible > crash doit être perdue : %+v", lost)
	}
}

func TestCashoutVsCrash_Concurrent(t *testing.T) {
	for iter := 0; iter < 200; iter++ {
		e, bets := newLiveEngine(t, time.Second, 10*time.Second, spec{"u1", 1, 1000, noAuto})
		var ok int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := e.Cashout(bets[0].ID); err == nil {
					atomic.AddInt32(&ok, 1)
				}
			}()
		}
		var lost []game.Bet
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, l, err := e.crashBets(5.0)
			if err != nil {
				t.Error(err)
			}
			lost = l
		}()
		close(start)
		wg.Wait()

		b, _ := e.bets.GetBet(bets[0].ID)
		switch {
		case ok == 1 && b.State == game.BetCashedOut && b.Payout > 0 && len(lost) == 0:
		case ok == 0 && b.State == game.BetLost && b.Payout == 0 && len(lost) == 1:
		default:
			t.Fatalf("iter %d : résultat contradictoire ok=%d state=%v payout=%d lost=%d", iter, ok, b.State, b.Payout, len(lost))
		}
	}
}

// --- chaîne de fairness ---

type memChain struct {
	mu   sync.Mutex
	recs []ChainRecord
}

func (m *memChain) LoadLast(ctx context.Context) (*ChainRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recs) == 0 {
		return nil, nil
	}
	r := m.recs[len(m.recs)-1]
	return &r, nil
}
func (m *memChain) Commit(ctx context.Context, rec ChainRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, rec)
	return nil
}
func (m *memChain) Reveal(ctx context.Context, nonce int64, cp float64, aborted bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.recs {
		if m.recs[i].Nonce == nonce {
			m.recs[i].Revealed, m.recs[i].Aborted = true, aborted
		}
	}
	return nil
}

func TestChain_RecoveryContinuesAfterInterruptedRound(t *testing.T) {
	store := &memChain{}
	cfg := DefaultConfig()
	cfg.ChainStore = store
	e1, _ := NewCrashEngine(cfg)
	if err := e1.prepareFairness(context.Background(), "r1"); err != nil { // round commité, jamais révélé
		t.Fatal(err)
	}
	seed1 := e1.serverSeed

	e2, _ := NewCrashEngine(cfg) // "redémarrage"
	if err := e2.recoverChain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.recs[0].Revealed || !store.recs[0].Aborted {
		t.Fatalf("le round interrompu doit être révélé comme aborted : %+v", store.recs[0])
	}
	if err := e2.prepareFairness(context.Background(), "r2"); err != nil {
		t.Fatal(err)
	}
	if e2.clientSeed != NextClientSeed(seed1) {
		t.Fatal("la chaîne doit continuer : clientSeed(N) = SHA256(serverSeed(N-1))")
	}
	if ok, _ := VerifyCrashPoint(seed1, store.recs[0].ClientSeed, store.recs[0].Nonce, store.recs[0].CrashPoint); !ok {
		t.Fatal("le round interrompu doit rester vérifiable")
	}
}

func TestChain_CorruptedRecordRefusesToStart(t *testing.T) {
	store := &memChain{}
	cfg := DefaultConfig()
	cfg.ChainStore = store
	e1, _ := NewCrashEngine(cfg)
	_ = e1.prepareFairness(context.Background(), "r1")
	store.recs[0].ServerSeedHash = "deadbeef"
	e2, _ := NewCrashEngine(cfg)
	if err := e2.recoverChain(context.Background()); !errors.Is(err, ErrChainCorrupted) {
		t.Fatalf("err=%v, attendu ErrChainCorrupted", err)
	}
}

func TestChain_NonceStrictlyIncreasingAndUnique(t *testing.T) {
	store := &memChain{}
	cfg := DefaultConfig()
	cfg.ChainStore = store
	e, _ := NewCrashEngine(cfg)
	for i := 0; i < 50; i++ {
		if err := e.prepareFairness(context.Background(), "r"); err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		e.lastServerSeed = e.serverSeed
		e.mu.Unlock()
	}
	for i := 1; i < len(store.recs); i++ {
		if store.recs[i].Nonce != store.recs[i-1].Nonce+1 {
			t.Fatalf("nonce non continu à l'index %d", i)
		}
		if store.recs[i].ClientSeed != NextClientSeed(store.recs[i-1].ServerSeed) {
			t.Fatalf("chaîne rompue à l'index %d", i)
		}
	}
}

// Distribution sur le chemin réel du moteur (prepareFairness : seeds aléatoires, chaîne, nonces).
func TestEnginePath_RTPAndDistribution(t *testing.T) {
	e, _ := NewCrashEngine(DefaultConfig())
	const n = 100000
	targets := []float64{1.5, 2, 10}
	wins := make([]int, len(targets))
	instant := 0
	for i := 0; i < n; i++ {
		if err := e.prepareFairness(context.Background(), "r"); err != nil {
			t.Fatal(err)
		}
		cp := e.crashPoint
		if cp < 1 || cp > MaxCrashPoint {
			t.Fatalf("crash point hors bornes : %v", cp)
		}
		if cp == 1.00 {
			instant++
		}
		for j, tg := range targets {
			if cp >= tg {
				wins[j]++
			}
		}
		e.mu.Lock()
		e.lastServerSeed = e.serverSeed
		e.mu.Unlock()
	}
	for j, tg := range targets {
		rtp := tg * float64(wins[j]) / n
		t.Logf("cible %.1fx : RTP mesuré %.4f", tg, rtp)
		if math.Abs(rtp-0.97) > 0.03 {
			t.Errorf("cible %.1fx : RTP %.4f hors de 0.97 ± 0.03", tg, rtp)
		}
	}
	// P(crash 1.00x) = P(97/(1-X) < 101) = P(X < 4/101) = 3,96 % (la formule floor(97/(1-X))/100 est plafonnée à 1.00 en dessous de 1.01).
	if f := float64(instant) / n; math.Abs(f-4.0/101.0) > 0.004 {
		t.Errorf("fréquence crash 1.00x = %.4f, attendu 4/101 = 0.0396 ± 0.004", f)
	}
}

// Cycle complet avec cashout manuel, auto-cashout et vérification indépendante du round.
func TestRun_FullCycleEventsAndIndependentVerification(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BettingDuration = 60 * time.Millisecond
	cfg.CrashedDuration = 40 * time.Millisecond
	cfg.TickInterval = 10 * time.Millisecond
	cfg.GrowthFactor = 0.01 // courbe accélérée : 5000x en < 1 s
	store := &memChain{}
	cfg.ChainStore = store
	e, err := NewCrashEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()

	var types []EventType
	var crash Event
	deadline := time.After(7 * time.Second)
loop:
	for {
		select {
		case ev := <-e.Events():
			if ev.Type != EventTick {
				types = append(types, ev.Type)
			}
			if ev.Type == EventCrash {
				crash = ev
			}
			if ev.Type == EventRoundSettled {
				break loop
			}
		case <-deadline:
			t.Fatal("round non terminé à temps")
		}
	}
	cancel()
	<-done

	want := []EventType{EventRoundCreated, EventBettingOpen, EventBettingClosed, EventRoundRunning, EventCrash, EventRoundSettled}
	if len(types) != len(want) {
		t.Fatalf("séquence %v, attendu %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("séquence %v, attendu %v", types, want)
		}
	}
	if HashServerSeed(crash.ServerSeed) != crash.ServerSeedHash {
		t.Fatal("le hash publié ne correspond pas au seed révélé")
	}
	if ok, err := VerifyCrashPoint(crash.ServerSeed, crash.ClientSeed, crash.Nonce, crash.CrashPoint); err != nil || !ok {
		t.Fatalf("vérification indépendante échouée : ok=%v err=%v", ok, err)
	}
	if len(store.recs) == 0 || !store.recs[0].Revealed || store.recs[0].Aborted {
		t.Fatalf("la chaîne doit contenir le round révélé : %+v", store.recs)
	}
}
