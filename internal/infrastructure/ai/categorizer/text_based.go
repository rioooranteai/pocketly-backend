package categorizer

import (
	"context"
	"strings"
	"unicode"

	"pocketly/internal/domain"
	"pocketly/internal/port"
)

/*
TextBasedCategorizer implements port.Categorizer with keyword
matching on the transaction text, without calling any external
service. It is free, instant and deterministic, so it works as the
default when no AI provider is configured, in local development and
in tests, and for descriptions whose wording is already obvious.
*/
type TextBasedCategorizer struct{}

var _ port.Categorizer = (*TextBasedCategorizer)(nil)

/*
NewTextBasedCategorizer builds a TextBasedCategorizer. It takes no
dependencies because it does not call any external service.
*/
func NewTextBasedCategorizer() *TextBasedCategorizer {
	return &TextBasedCategorizer{}
}

/*
categoryKeywords maps a category to lowercase keywords. A keyword
matches when a word in the text starts with it, so "kopi" also
matches "kopiku" but "tol" does not match "botol". A keyword with a
space ("rumah sakit") matches that phrase at the start of a word.
*/
var categoryKeywords = map[string][]string{
	domain.CategoryFood:           {"makan", "minum", "resto", "warung", "kopi", "cafe", "nasi", "ayam", "bakso", "mie", "sate", "gofood", "grabfood", "shopeefood", "snack", "sayur", "beras"},
	domain.CategoryTransportation: {"bensin", "pertalite", "pertamax", "pertamina", "grab", "gojek", "ojol", "ojek", "taksi", "parkir", "tol", "krl", "mrt", "transjakarta", "busway", "tiket kereta"},
	domain.CategoryShopping:       {"belanja", "shopee", "tokopedia", "lazada", "mall", "baju", "celana", "sepatu", "tas", "elektronik"},
	domain.CategoryBills:          {"listrik", "token pln", "pln", "pulsa", "kuota", "wifi", "internet", "indihome", "pdam", "bpjs", "cicilan", "tagihan"},
	domain.CategoryEntertainment:  {"nonton", "bioskop", "cinema", "netflix", "spotify", "youtube premium", "disney", "game", "steam", "konser"},
	domain.CategoryHealth:         {"apotek", "obat", "dokter", "rumah sakit", "klinik", "vitamin", "puskesmas"},
	domain.CategoryOthers:         {"kost", "sewa", "donasi", "zakat", "sedekah", "infaq", "transfer", "sekolah", "kuliah", "spp"},
}

/*
normalizeText lowercases text and turns every run of non-letter,
non-digit characters into a single space, padded on both sides, so
keywords can be matched at word starts with a plain substring search.
*/
func normalizeText(text string) string {
	var b strings.Builder
	b.WriteByte(' ')
	lastSpace := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	if !lastSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

/*
Categorize returns the first category, in domain.Categories order,
that has a keyword matching the text (description and item
names). Walking the ordered
slice instead of the map keeps the result the same on every run, and
because CategoryOthers is last a specific category always wins over
it. It returns domain.CategoryUncategorized when nothing matches, and
never returns an error since it has no external dependency that can
fail.
*/
func (t *TextBasedCategorizer) Categorize(ctx context.Context, text string) (string, error) {
	normalized := normalizeText(text)

	for _, category := range domain.Categories {
		for _, kw := range categoryKeywords[category] {
			if strings.Contains(normalized, " "+kw) {
				return category, nil
			}
		}
	}

	return domain.CategoryUncategorized, nil
}
