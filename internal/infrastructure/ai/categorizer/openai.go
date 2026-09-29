package categorizer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"pocketly/internal/domain"
	"pocketly/internal/port"
)

/*
categorizerTimeout bounds one Categorize call, including the SDK's
automatic retries. The OpenAI client has no request timeout by
default, so without this a hung provider would hold the request open
for as long as the client waits.
*/
const categorizerTimeout = 25 * time.Second

/*
extractedCategory is the intermediate shape used to unmarshal the
model's JSON response before the category is validated against the
domain list.
*/
type extractedCategory struct {
	Category string `json:"category"`
}

/*
OpenAICategorizer implements port.Categorizer using OpenAI's chat
completion API. It sends the transaction text (description and item names) together with
categorizerPrompt and expects a strict JSON response naming exactly
one domain category.
*/
type OpenAICategorizer struct {
	client openai.Client
}

var _ port.Categorizer = (*OpenAICategorizer)(nil)

/*
NewOpenAICategorizer builds an OpenAICategorizer that authenticates
with the given OpenAI API key.
*/
func NewOpenAICategorizer(apiKey string) *OpenAICategorizer {
	client := openai.NewClient(option.WithAPIKey(apiKey))
	return &OpenAICategorizer{client: client}
}

/*
categorizerPrompt is the system prompt that restricts the model to
the categories in domain.Categories and asks for JSON only. Keep the
list here in sync with domain.Categories; Categorize rejects any
answer outside that list anyway.
*/
const categorizerPrompt = `You are a financial transaction classification system.
Your input is a transaction: a short description (often just the merchant name) followed by the purchased item names.
Your task: categorize the whole transaction into EXACTLY ONE of the following categories, judging mainly by what was bought:

- food: Food, drinks, restaurants, food stalls, coffee, grocery shopping
- transportation: Gas/fuel, ride-hailing, taxi, parking, tolls, public transit tickets
- shopping: Non-food shopping: clothing, electronics, marketplace, mall
- bills: Recurring bills: electricity, water, internet, phone credit, BPJS, installments
- entertainment: Entertainment: cinema, games, music or video streaming subscriptions
- health: Health: pharmacy, medicine, doctor, clinic, hospital
- others: A clear transaction that fits none of the above: rent, donations, transfers to people, education fees

Pick a specific category whenever it reasonably fits. Use "others" only when none of the specific categories applies.

Respond with ONLY valid JSON, no other text, no markdown code fences, matching exactly this shape:

{
  "category": "one of: food, transportation, shopping, bills, entertainment, health, others"
}`

/*
Categorize asks the model to pick one category for the given
transaction text. Technical failures (request error, no choices, malformed
JSON, a category outside domain.Categories) are returned as errors so
the usecase logs them and falls back. Empty text is not a
failure, so it returns domain.CategoryUncategorized without calling
the API. The whole call, retries included, is cut off after
categorizerTimeout.
*/
func (o *OpenAICategorizer) Categorize(ctx context.Context, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return domain.CategoryUncategorized, nil
	}

	ctx, cancel := context.WithTimeout(ctx, categorizerTimeout)
	defer cancel()

	completion, err := o.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT5_6Luna,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(categorizerPrompt),
			openai.UserMessage(text),
		},
	})
	if err != nil {
		return "", fmt.Errorf("openai categorize request failed: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("openai categorize returned no choices")
	}

	rawContent := completion.Choices[0].Message.Content

	var parsed extractedCategory
	if err := json.Unmarshal([]byte(rawContent), &parsed); err != nil {
		return "", fmt.Errorf("failed to parse categorize response as JSON: %w", err)
	}

	/*
		The model may answer with different casing or stray whitespace,
		or invent a category. Normalize first, then accept only a value
		from domain.Categories; "uncategorized" is a fallback, not a pick.
	*/
	category := strings.ToLower(strings.TrimSpace(parsed.Category))
	if category == domain.CategoryUncategorized || !domain.IsValidCategory(category) {
		return "", fmt.Errorf("openai returned unknown category %q", parsed.Category)
	}

	return category, nil
}
