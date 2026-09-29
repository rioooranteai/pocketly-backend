package main

import (
	"gorm.io/gorm"

	"pocketly/internal/delivery/http/handler"
	"pocketly/internal/infrastructure/ai/categorizer"
	"pocketly/internal/infrastructure/ai/vision"
	"pocketly/internal/infrastructure/auth"
	"pocketly/internal/infrastructure/config"
	persistence "pocketly/internal/infrastructure/persistence/gorm"
	"pocketly/internal/port"
	"pocketly/internal/usecase"
)

/*
App holds every fully-wired handler the router needs. It is the
single object main.go depends on, so main.go itself stays tiny and
free of construction details.
*/
type App struct {
	AuthHandler        *handler.AuthHandler
	TransactionHandler *handler.TransactionHandler

	/*
		TokenVerifier is what AuthMiddleware needs to check tokens. It is
		typed as the port rather than *auth.JWTSigner so main.go and the
		router never depend on the concrete JWT implementation.
	*/
	TokenVerifier port.TokenVerifier
}

/*
Bootstrap wires every layer together in dependency order —
repositories, signers, categorizer, usecases, then handlers — and
returns a ready-to-use App. This is the only function in the
codebase allowed to know about every concrete implementation at
once; everything it constructs is handed to consumers only through
the interfaces they depend on.
*/
func Bootstrap(cfg *config.Config, db *gorm.DB) (*App, error) {
	/*
		User feature wiring.
	*/

	/*
		Concrete implementation of repository.UserRepository, backed by
		GORM/SQLite. To switch database engines (e.g. to Postgres), only
		this line and persistence.Connect in main.go need to change —
		nothing below this line needs to know the difference.
	*/
	userRepository := persistence.NewGormUserRepository(db)

	/*
		Concrete implementation of port.TokenSigner, backed by
		signed JWTs. To switch to a different auth strategy (e.g.
		session tokens), replace this with a different implementation
		of the same interface — AuthUsecase never needs to change.
	*/
	jwtSigner, err := auth.NewJWTSigner(cfg.JWTSecret)
	if err != nil {
		return nil, err
	}

	/*
		Concrete implementation of port.PasswordHasher, backed by
		Argon2id. To switch algorithms (e.g. to bcrypt), replace this
		with a different implementation of the same interface —
		AuthUsecase never needs to change.
	*/
	hasher := auth.NewArgon2Hasher()

	/*
		Business logic for registration and login. Depends only on the
		three interfaces above, not on their concrete implementations.
	*/
	authUsecase := usecase.NewAuthUsecase(userRepository, jwtSigner, hasher)

	/*
		HTTP layer for the /register and /login endpoints.
	*/
	authHandler := handler.NewAuthHandler(authUsecase)

	/*
		Transaction feature wiring.
	*/

	/*
		Concrete implementation of port.Categorizer, backed by OpenAI's
		chat completion API. It shares the OpenAI key with the vision
		extractor. If a call fails (missing key, timeout, bad answer),
		TransactionUsecase logs it and stores the transaction as
		uncategorized. categorizer.NewTextBasedCategorizer() is a keyless
		drop-in replacement; TransactionUsecase needs no changes either
		way.
	*/
	categorizer := categorizer.NewOpenAICategorizer(cfg.VisionConfig.APIKey)

	/*
		Concrete implementation of port.VisionExtractor, backed by
		OpenAI's vision-capable chat completion API. Extracts a
		description and line items directly from a receipt image.
	*/
	visionExtractor := vision.NewOpenAIVisionExtractor(cfg.VisionConfig.APIKey)

	/*
		Concrete implementation of repository.TransactionRepository,
		backed by GORM/SQLite. Same swap story as userRepository above.
	*/
	transactionRepository := persistence.NewGormTransactionRepository(db)

	/*
		Business logic for creating, reading, updating, and deleting
		transactions — including image-based creation via
		visionExtractor. Depends only on the three interfaces above.
	*/
	transactionUsecase := usecase.NewTransactionUsecase(transactionRepository, categorizer, visionExtractor, cfg.VisionConfig.MaxFileSize)

	/*
		HTTP layer for the /transactions endpoints, including
		/transactions/scan for image-based creation.
	*/
	transactionHandler := handler.NewTransactionHandler(transactionUsecase)

	return &App{
		AuthHandler:        authHandler,
		TransactionHandler: transactionHandler,
		TokenVerifier:      jwtSigner,
	}, nil
}
