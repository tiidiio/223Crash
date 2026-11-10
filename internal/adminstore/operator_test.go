package adminstore

import (
	"errors"
	"testing"
)

func validOperatorInput() CreateOperatorInput {
	return CreateOperatorInput{
		Name:     "Bet Power",
		Code:     "bet_power",
		Currency: "xof",
	}
}

func TestValidateOperatorInputNormalizesValues(t *testing.T) {
	input := CreateOperatorInput{
		Name:     "  Bet Power  ",
		Code:     "  bet_power ",
		Currency: " xof ",
	}

	got, err := ValidateOperatorInput(input)
	if err != nil {
		t.Fatalf("ValidateOperatorInput() error = %v", err)
	}

	if got.Name != "Bet Power" {
		t.Errorf("Name = %q, want %q", got.Name, "Bet Power")
	}
	if got.Code != "BET_POWER" {
		t.Errorf("Code = %q, want %q", got.Code, "BET_POWER")
	}
	if got.Currency != "XOF" {
		t.Errorf("Currency = %q, want %q", got.Currency, "XOF")
	}
}

func TestValidateOperatorInputAcceptsSupportedCurrencies(t *testing.T) {
	currencies := []string{"XOF", "XAF", "EUR", "USD", "GNF", "MAD"}

	for _, currency := range currencies {
		t.Run(currency, func(t *testing.T) {
			input := validOperatorInput()
			input.Currency = currency

			if _, err := ValidateOperatorInput(input); err != nil {
				t.Fatalf("currency %q rejected: %v", currency, err)
			}
		})
	}
}

func TestValidateOperatorInputRejectsInvalidNames(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "one character", value: "A"},
		{name: "too long", value: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz1234567890"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validOperatorInput()
			input.Name = tt.value

			_, err := ValidateOperatorInput(input)
			if !errors.Is(err, ErrInvalidOperator) {
				t.Fatalf("error = %v, want ErrInvalidOperator", err)
			}
		})
	}
}

func TestValidateOperatorInputRejectsInvalidCodes(t *testing.T) {
	codes := []string{
		"",
		"AB",
		"BET POWER",
		"BET-POWER",
		"BET.POWER",
		"BET/POWER",
		"BET@POWER",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ",
	}

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			input := validOperatorInput()
			input.Code = code

			_, err := ValidateOperatorInput(input)
			if !errors.Is(err, ErrInvalidOperator) {
				t.Fatalf("code %q: error = %v, want ErrInvalidOperator", code, err)
			}
		})
	}
}

func TestValidateOperatorInputRejectsUnsupportedCurrency(t *testing.T) {
	input := validOperatorInput()
	input.Currency = "BTC"

	_, err := ValidateOperatorInput(input)
	if !errors.Is(err, ErrInvalidOperator) {
		t.Fatalf("error = %v, want ErrInvalidOperator", err)
	}
}

func TestValidateOperatorInputAcceptsEmptyCallbackURL(t *testing.T) {
	input := validOperatorInput()
	input.CallbackURL = ""

	if _, err := ValidateOperatorInput(input); err != nil {
		t.Fatalf("empty callback URL should be accepted: %v", err)
	}
}

func TestValidateOperatorInputAcceptsHTTPSCallbackURL(t *testing.T) {
	input := validOperatorInput()
	input.CallbackURL = "https://wallet.example.com/provider/callback"

	got, err := ValidateOperatorInput(input)
	if err != nil {
		t.Fatalf("valid HTTPS callback URL rejected: %v", err)
	}

	if got.CallbackURL != input.CallbackURL {
		t.Errorf("CallbackURL = %q, want %q", got.CallbackURL, input.CallbackURL)
	}
}

func TestValidateOperatorInputRejectsUnsafeCallbackURLs(t *testing.T) {
	urls := []string{
		"http://wallet.example.com/callback",
		"ftp://wallet.example.com/callback",
		"javascript:alert(1)",
		"https:///missing-host",
		"https://user:password@wallet.example.com/callback",
		"https://wallet.example.com/callback#fragment",
		"not-a-url",
	}

	for _, callbackURL := range urls {
		t.Run(callbackURL, func(t *testing.T) {
			input := validOperatorInput()
			input.CallbackURL = callbackURL

			_, err := ValidateOperatorInput(input)
			if !errors.Is(err, ErrInvalidOperator) {
				t.Fatalf("URL %q: error = %v, want ErrInvalidOperator", callbackURL, err)
			}
		})
	}
}

func TestGenerateOperatorID(t *testing.T) {
	first, err := generateOperatorID()
	if err != nil {
		t.Fatalf("generateOperatorID() error = %v", err)
	}

	second, err := generateOperatorID()
	if err != nil {
		t.Fatalf("generateOperatorID() second call error = %v", err)
	}

	if len(first) != 32 {
		t.Fatalf("ID length = %d, want 32 hexadecimal characters", len(first))
	}
	if first == second {
		t.Fatal("two generated operator IDs must not be equal")
	}
}

func TestGeneratedOperatorIdentifiersAreDistinct(t *testing.T) {
	recordID, err := generateOperatorID()
	if err != nil {
		t.Fatalf("generate record ID: %v", err)
	}

	operatorID, err := generateOperatorID()
	if err != nil {
		t.Fatalf("generate operator ID: %v", err)
	}

	if recordID == operatorID {
		t.Fatal("MongoDB record ID and business operator ID must be distinct")
	}

	if len(recordID) != 32 || len(operatorID) != 32 {
		t.Fatalf("unexpected identifier lengths: record=%d operator=%d",
			len(recordID), len(operatorID))
	}
}
