package main

import "testing"

func TestEngineIDIsolatesOperators(t *testing.T) {
	a := &client{operatorID: "opA", playerID: "p1"}
	b := &client{operatorID: "opB", playerID: "p1"}
	if a.engineID() == b.engineID() {
		t.Fatalf("même clé moteur pour deux opérateurs distincts: %q", a.engineID())
	}
}

func TestSubjectCannotContainSeparator(t *testing.T) {
	// L'injectivité de engineID repose sur cette propriété.
	if wsIDPattern.MatchString("a:b") {
		t.Fatal("wsIDPattern accepte ':' — engineID n'est plus injective")
	}
}

func TestRegisterKeepsOtherOperatorSession(t *testing.T) {
	gw := &gateway{clients: make(map[string]*client)}
	a := &client{operatorID: "opA", playerID: "p1", done: make(chan struct{})}
	b := &client{operatorID: "opB", playerID: "p1", done: make(chan struct{})}

	gw.register(a)
	gw.register(b)
	if len(gw.clients) != 2 {
		t.Fatalf("attendu 2 sessions, obtenu %d", len(gw.clients))
	}

	gw.unregister(a)
	if _, ok := gw.clients[a.engineID()]; ok {
		t.Fatal("session opA toujours présente après unregister")
	}
	if cur, ok := gw.clients[b.engineID()]; !ok || cur != b {
		t.Fatal("session opB perdue par unregister d'opA")
	}
}
