package gamecatalog

type Game struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Category          string `json:"category"`
	Provider          string `json:"provider"`
	Version           string `json:"version"`
	Status            string `json:"status"`
	IntegrationStatus string `json:"integrationStatus"`
}

const (
	StatusRegistered   = "registered"
	IntegrationPending = "pending"
)

var initialGames = []Game{
	{
		ID:                "223crash",
		Name:              "223CRASH",
		Category:          "crash",
		Provider:          "223GAMING",
		Version:           "2.2.0.56",
		Status:            StatusRegistered,
		IntegrationStatus: IntegrationPending,
	},
}

// List returns a copy of the registered game catalog.
func List() []Game {
	return append([]Game(nil), initialGames...)
}
