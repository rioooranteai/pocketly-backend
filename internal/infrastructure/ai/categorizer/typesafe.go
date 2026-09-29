package categorizer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pocketly/internal/domain"
	"pocketly/internal/port"
)

/*
Defaults and tuning values for TypeSafeCategorizer. The base URL,
model, and timeout mirror the defaults of TypeSafe's official SDKs.
typeSafeMinConfidence is the lowest confidence accepted before the
answer is treated as a guess and replaced with
domain.CategoryUncategorized.
*/
const (
	typeSafeDefaultBaseURL = "https://api.typesafe.ai"
	typeSafeDefaultModel   = "jev-latest"
	typeSafeTimeout        = 10 * time.Second
	typeSafeMinConfidence  = 0.6
	typeSafeQuestionID     = "category"
	typeSafeErrorBodyLimit = 4096
)

/*
typeSafeCategoryDescriptions tells Jev what each domain category
covers. It only holds descriptions; the set of categories itself comes
from domain.Categories.
*/
var typeSafeCategoryDescriptions = map[string]string{
	domain.CategoryFood:           "Makanan, minuman, restoran, warung, kopi, belanja bahan dapur",
	domain.CategoryTransportation: "Bensin, ojek online, taksi, parkir, tol, tiket kendaraan umum",
	domain.CategoryShopping:       "Belanja barang non-makanan: pakaian, elektronik, marketplace, mall",
	domain.CategoryBills:          "Tagihan rutin: listrik, air, internet, pulsa, BPJS, cicilan",
	domain.CategoryEntertainment:  "Hiburan: bioskop, game, langganan streaming musik atau film",
	domain.CategoryHealth:         "Kesehatan: apotek, obat, dokter, klinik, rumah sakit",
	domain.CategoryOthers:         "Lainnya: transaksi jelas yang tidak masuk kategori lain, seperti sewa kos, donasi, transfer ke orang, biaya pendidikan",
}

/*
typeSafeCategoryCriteria is the choice criteria sent to Jev: one entry
per domain.Categories value. Building it from the domain list means a
category added there is offered to Jev automatically; one without a
description falls back to its own name.
*/
var typeSafeCategoryCriteria = buildTypeSafeCategoryCriteria()

func buildTypeSafeCategoryCriteria() map[string]string {
	criteria := make(map[string]string, len(domain.Categories))
	for _, category := range domain.Categories {
		description, ok := typeSafeCategoryDescriptions[category]
		if !ok {
			description = category
		}
		criteria[category] = description
	}
	return criteria
}

/*
typeSafeQuestion and typeSafeRequest are the request body shape of
TypeSafe's POST /v1/systemone endpoint.
*/
type typeSafeQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria,omitempty"`
}

type typeSafeRequest struct {
	State     string                      `json:"state"`
	Model     string                      `json:"model"`
	Questions map[string]typeSafeQuestion `json:"questions"`
}

/*
typeSafeAnswer and typeSafeResponse hold only the response fields a
choice question needs; the rest of the payload is ignored.
*/
type typeSafeAnswer struct {
	Type       string  `json:"type"`
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

type typeSafeResponse struct {
	Answers map[string]typeSafeAnswer `json:"answers"`
}

type TypeSafeCategorizer struct {
	apyKey     string
	httpClient *http.Client
	baseUrl string
	model string
}

var _ port.Categorizer = (*TypeSafeCategorizer)(nil)

func NewTypeSafeCategorizer(apiKey string, baseUrl string, model string) *TypeSafeCategorizer {
	if baseUrl == "" {
		baseUrl = typeSafeDefaultBaseURL
	}
	if model == "" {
		model = typeSafeDefaultModel
	}

	return &TypeSafeCategorizer{
		apyKey: apiKey,
		httpClient: &http.Client{Timeout: typeSafeTimeout},
		baseUrl: baseUrl,
		model: model,
	}
}

/*
Categorize asks Jev to pick one category from typeSafeCategoryCriteria
for the given transaction text. Technical failures (network, non-200
status, malformed or unexpected response) are returned as errors so
the usecase logs them and falls back. Empty text or a
low-confidence answer is not a failure, so it returns
domain.CategoryUncategorized with no error.
*/
func (t *TypeSafeCategorizer) Categorize(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return domain.CategoryUncategorized, nil
	}
	if t.apyKey == "" {
		return "", errors.New("typesafe API key is not configured")
	}

	body, err := json.Marshal(typeSafeRequest{
		State: text,
		Model: t.model,
		Questions: map[string]typeSafeQuestion{
			typeSafeQuestionID: {
				Type:         "choice",
				Instructions: "Which spending category best fits this transaction?",
				Criteria:     typeSafeCategoryCriteria,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to encode typesafe request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.baseUrl, "/")+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to build typesafe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apyKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("typesafe request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, typeSafeErrorBodyLimit))
		return "", fmt.Errorf("typesafe returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var parsed typeSafeResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("failed to parse typesafe response as JSON: %w", err)
	}

	answer, ok := parsed.Answers[typeSafeQuestionID]
	if !ok {
		return "", fmt.Errorf("typesafe response has no %q answer", typeSafeQuestionID)
	}
	if _, known := typeSafeCategoryCriteria[answer.Choice]; !known {
		return "", fmt.Errorf("typesafe returned unknown category %q", answer.Choice)
	}
	if answer.Confidence < typeSafeMinConfidence {
		return domain.CategoryUncategorized, nil
	}

	return answer.Choice, nil
}