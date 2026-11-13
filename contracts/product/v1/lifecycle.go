package v1

type ProductLifecycle interface {
	Start() error
	Stop() error
	Recover(lastRound ProductRound) error
}
