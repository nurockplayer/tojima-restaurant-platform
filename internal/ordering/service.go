package ordering

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

type grantAccess interface {
	PrecheckGrant(context.Context, string, string) (tablesession.Session, error)
}

type menuAccess interface {
	LockStoreMenuForAdmissionTx(context.Context, pgx.Tx, catalog.StoreScope) (catalog.PublishedMenu, error)
	QuoteDineInTx(context.Context, pgx.Tx, catalog.PublishedMenu, time.Time, []catalog.MenuLine) (catalog.MenuQuote, error)
}

// Service joins P-76 capabilities to P-77 current pricing inside the short
// transaction that edits, previews, or commits a shared cart.
type Service struct {
	db       *pgxpool.Pool
	sessions grantAccess
	menus    menuAccess
	now      func(context.Context, pgx.Tx) (time.Time, error)
	newID    func() (string, error)
}

func New(db *pgxpool.Pool, sessions *tablesession.Service, menus *catalog.Service) (*Service, error) {
	if db == nil || sessions == nil || menus == nil {
		return nil, ErrInvalid
	}
	return &Service{db: db, sessions: sessions, menus: menus, now: databaseClock, newID: randomID}, nil
}

func databaseClock(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *Service) ReadCart(ctx context.Context, sessionID, grantToken string) (Cart, error) {
	tx, session, menu, cart, err := s.lockCurrent(ctx, sessionID, grantToken, false)
	if err != nil {
		return Cart{}, err
	}
	defer tx.Rollback(ctx)
	_ = session
	annotateCart(menu, &cart)
	return cart, tx.Commit(ctx)
}

func (s *Service) Mutate(ctx context.Context, input Mutation) (Cart, error) {
	if !validIdempotencyKey(input.IdempotencyKey) || input.ExpectedGeneration <= 0 || input.ExpectedVersion <= 0 {
		return Cart{}, ErrInvalid
	}
	// Reject oversized selections before copying/sorting them, hashing the
	// request, or acquiring catalog/session/cart locks.
	if len(input.Options) > MaxOptionsPerLine {
		return Cart{}, ErrInvalid
	}
	input.Options = canonicalOptions(input.Options)
	digest, err := digestJSON(struct {
		Generation int64                     `json:"generation"`
		Version    int64                     `json:"version"`
		Kind       MutationKind              `json:"kind"`
		LineID     string                    `json:"line_id"`
		ProductKey string                    `json:"product_key"`
		Quantity   int64                     `json:"quantity"`
		Options    []catalog.OptionSelection `json:"options"`
	}{input.ExpectedGeneration, input.ExpectedVersion, input.Kind, input.LineID, input.ProductKey, input.Quantity, input.Options})
	if err != nil {
		return Cart{}, ErrInvalid
	}
	tx, session, menu, cart, err := s.lockCurrent(ctx, input.SessionID, input.GrantToken, true)
	if err != nil {
		return Cart{}, err
	}
	defer tx.Rollback(ctx)
	if _, found, e := mutationReplay(ctx, tx, session.ID, input.ExpectedGeneration, input.IdempotencyKey, digest); e != nil {
		return cart, e
	} else if found {
		return cart, tx.Commit(ctx)
	}
	if cart.Generation != input.ExpectedGeneration || cart.Version != input.ExpectedVersion {
		annotateCart(menu, &cart)
		return cart, ErrConflict
	}
	if err = applyMutation(ctx, tx, menu, cart, input, s.newID); err != nil {
		annotateCart(menu, &cart)
		return cart, err
	}
	newVersion := cart.Version + 1
	if _, err = tx.Exec(ctx, `UPDATE table_carts SET version=$2,updated_at=clock_timestamp() WHERE session_id=$1`, session.ID, newVersion); err != nil {
		return cart, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO table_cart_idempotency(session_id,generation,idempotency_key,request_digest,result_version)
		VALUES ($1,$2,$3,$4,$5)`, session.ID, cart.Generation, input.IdempotencyKey, digest[:], newVersion); err != nil {
		return cart, err
	}
	trustedAt, err := s.now(ctx, tx)
	if err != nil {
		return cart, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, input.GrantToken, trustedAt); err != nil {
		return cart, ErrDenied
	}
	updated, err := readCart(ctx, tx, session.ID)
	if err != nil {
		return Cart{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Cart{}, err
	}
	annotateCart(menu, &updated)
	return updated, nil
}

func applyMutation(ctx context.Context, tx pgx.Tx, menu catalog.PublishedMenu, cart Cart, input Mutation, idgen func() (string, error)) error {
	lineIndex := -1
	for i := range cart.Lines {
		if cart.Lines[i].LineID == input.LineID && input.LineID != "" {
			lineIndex = i
			break
		}
	}
	switch input.Kind {
	case AddLine:
		if len(cart.Lines) >= MaxCartLines || cartOptionCount(cart)+len(input.Options) > MaxCartOptions || input.LineID != "" || !validCatalogKey(input.ProductKey) || !validLine(input.Quantity, input.Options) {
			return ErrInvalid
		}
		lineID, err := idgen()
		if err != nil {
			return err
		}
		line := CartLine{LineID: lineID, ProductKey: input.ProductKey, Quantity: input.Quantity}
		if err = enrichLine(menu, &line, input.Options); err != nil {
			return err
		}
		return insertLine(ctx, tx, cart.SessionID, cart.Generation, line)
	case SetLine:
		if lineIndex < 0 || !validLine(input.Quantity, input.Options) || input.ProductKey != "" {
			return ErrInvalid
		}
		line := cart.Lines[lineIndex]
		remainingOptions := cartOptionCount(cart) - len(line.Options)
		if remainingOptions < 0 || remainingOptions+len(input.Options) > MaxCartOptions {
			return ErrInvalid
		}
		line.Quantity = input.Quantity
		if err := enrichLine(menu, &line, input.Options); err != nil {
			return err
		}
		return updateLine(ctx, tx, cart.SessionID, cart.Generation, line)
	case RemoveLine:
		if lineIndex < 0 || input.ProductKey != "" || input.Quantity != 0 || len(input.Options) != 0 {
			return ErrConflict
		}
		command, err := tx.Exec(ctx, `DELETE FROM table_cart_lines WHERE session_id=$1 AND generation=$2 AND line_id=$3`, cart.SessionID, cart.Generation, input.LineID)
		if err == nil && command.RowsAffected() != 1 {
			return ErrConflict
		}
		return err
	default:
		return ErrInvalid
	}
}

func cartOptionCount(cart Cart) int {
	count := 0
	for _, line := range cart.Lines {
		count += len(line.Options)
	}
	return count
}

func (s *Service) Preview(ctx context.Context, sessionID, grantToken string, expectedGeneration, expectedVersion int64) (Preview, error) {
	tx, session, menu, cart, err := s.lockCurrent(ctx, sessionID, grantToken, false)
	if err != nil {
		return Preview{}, err
	}
	defer tx.Rollback(ctx)
	if cart.Generation != expectedGeneration || cart.Version != expectedVersion {
		annotateCart(menu, &cart)
		return Preview{SessionID: session.ID, Generation: cart.Generation, Version: cart.Version, LatestCart: &cart}, ErrConflict
	}
	quote, fingerprint, err := s.quote(ctx, tx, session, menu, cart)
	if err != nil {
		return Preview{}, err
	}
	trustedAt, err := s.now(ctx, tx)
	if err != nil {
		return Preview{}, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, grantToken, trustedAt); err != nil {
		return Preview{}, ErrDenied
	}
	if err = tx.Commit(ctx); err != nil {
		return Preview{}, err
	}
	return Preview{SessionID: session.ID, Generation: cart.Generation, Version: cart.Version, Fingerprint: fingerprint, Quote: quote}, nil
}

func (s *Service) Commit(ctx context.Context, input CommitRequest) (CommitResult, error) {
	if !validIdempotencyKey(input.IdempotencyKey) || input.ExpectedGeneration <= 0 || input.ExpectedVersion <= 0 || input.PreviewFingerprint == "" || !validPayment(input.PaymentMethod) {
		return CommitResult{}, ErrInvalid
	}
	digest, err := digestJSON(struct {
		Generation  int64         `json:"generation"`
		Version     int64         `json:"version"`
		Fingerprint string        `json:"fingerprint"`
		Method      PaymentMethod `json:"method"`
	}{input.ExpectedGeneration, input.ExpectedVersion, input.PreviewFingerprint, input.PaymentMethod})
	if err != nil {
		return CommitResult{}, ErrInvalid
	}
	tx, session, menu, cart, err := s.lockCurrent(ctx, input.SessionID, input.GrantToken, false)
	if err != nil {
		return CommitResult{}, err
	}
	defer tx.Rollback(ctx)
	if prior, found, e := orderReplay(ctx, tx, session.ID, input.ExpectedGeneration, input.IdempotencyKey, digest); e != nil {
		return CommitResult{}, e
	} else if found {
		if err = bindOrderReplay(ctx, tx, session.ID, input.ExpectedGeneration, input.IdempotencyKey, digest, prior); err != nil {
			return CommitResult{}, err
		}
		result, readErr := resultForOrder(ctx, tx, prior, AlreadyCommitted)
		if readErr != nil {
			return CommitResult{}, readErr
		}
		if err = tx.Commit(ctx); err != nil {
			return CommitResult{}, err
		}
		return result, nil
	}
	if order, found, e := loadOrderByGeneration(ctx, tx, session.ID, input.ExpectedGeneration); e != nil {
		return CommitResult{}, e
	} else if found {
		if err = bindOrderReplay(ctx, tx, session.ID, input.ExpectedGeneration, input.IdempotencyKey, digest, order.ID); err != nil {
			return CommitResult{}, err
		}
		result, readErr := resultForOrder(ctx, tx, order.ID, AlreadyCommitted)
		if readErr != nil {
			return CommitResult{}, readErr
		}
		if err = tx.Commit(ctx); err != nil {
			return CommitResult{}, err
		}
		return result, nil
	}
	if cart.Generation != input.ExpectedGeneration || cart.Version != input.ExpectedVersion {
		annotateCart(menu, &cart)
		return CommitResult{Cart: cart}, ErrConflict
	}
	if len(cart.Lines) == 0 {
		return CommitResult{Cart: cart}, ErrEmptyCart
	}
	quote, fingerprint, err := s.quote(ctx, tx, session, menu, cart)
	if err != nil {
		return CommitResult{Cart: cart}, err
	}
	if fingerprint != input.PreviewFingerprint {
		annotateCart(menu, &cart)
		return CommitResult{Cart: cart, CurrentPreview: &Preview{SessionID: session.ID, Generation: cart.Generation, Version: cart.Version, Fingerprint: fingerprint, Quote: quote}}, ErrRequote
	}
	orderID, err := s.newID()
	if err != nil {
		return CommitResult{}, err
	}
	if orderID == "" || len(orderID) != 32 {
		return CommitResult{}, ErrInvalid
	}
	taxRecord := quote.Tax.Record()
	snapshot, err := json.Marshal(storedSnapshot{CatalogID: menu.CatalogID, Quote: quote, Tax: taxRecord})
	if err != nil {
		return CommitResult{}, err
	}
	taxJSON, err := json.Marshal(taxRecord)
	if err != nil {
		return CommitResult{}, err
	}
	total := quote.Tax.Total()
	trustedAt, err := s.now(ctx, tx)
	if err != nil {
		return CommitResult{}, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, input.GrantToken, trustedAt); err != nil {
		return CommitResult{}, ErrDenied
	}
	_, err = tx.Exec(ctx, `INSERT INTO table_orders(order_id,session_id,tenant_id,store_id,table_id,generation,payment_method,requires_staff_acceptance,catalog_id,catalog_mode,catalog_revision,quote_snapshot,tax_snapshot,total_gross_jpy)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb,$13::jsonb,$14)`, orderID, session.ID, session.Scope.TenantID, session.Scope.StoreID, session.Scope.TableID,
		cart.Generation, string(input.PaymentMethod), session.RequiresStaffAcceptance, menu.CatalogID, menu.Mode, menu.Revision, snapshot, taxJSON, total.GrossJPY)
	if err != nil {
		return CommitResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO table_order_idempotency(session_id,generation,idempotency_key,request_digest,order_id)
		VALUES ($1,$2,$3,$4,$5)`, session.ID, cart.Generation, input.IdempotencyKey, digest[:], orderID); err != nil {
		return CommitResult{}, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM table_cart_lines WHERE session_id=$1 AND generation=$2`, session.ID, cart.Generation); err != nil {
		return CommitResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE table_carts SET generation=generation+1,version=1,updated_at=clock_timestamp() WHERE session_id=$1 AND generation=$2`, session.ID, cart.Generation); err != nil {
		return CommitResult{}, err
	}
	result, err := resultForOrder(ctx, tx, orderID, Committed)
	if err != nil {
		return CommitResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CommitResult{}, err
	}
	return result, nil
}

func (s *Service) ListOrders(ctx context.Context, sessionID, grantToken string) ([]Order, error) {
	tx, session, _, _, err := s.lockCurrent(ctx, sessionID, grantToken, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT order_id FROM table_orders WHERE session_id=$1 ORDER BY generation`, session.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	orders := make([]Order, 0, len(ids))
	for _, id := range ids {
		order, readErr := readOrder(ctx, tx, id)
		if readErr != nil {
			return nil, readErr
		}
		orders = append(orders, order)
	}
	trustedAt, err := s.now(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, grantToken, trustedAt); err != nil {
		return nil, ErrDenied
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return orders, nil
}

func (s *Service) lockCurrent(ctx context.Context, sessionID, grantToken string, initialize bool) (pgx.Tx, tablesession.Session, catalog.PublishedMenu, Cart, error) {
	if strings.TrimSpace(sessionID) == "" || grantToken == "" {
		return nil, tablesession.Session{}, catalog.PublishedMenu{}, Cart{}, ErrDenied
	}
	pre, err := s.sessions.PrecheckGrant(ctx, sessionID, grantToken)
	if err != nil {
		return nil, tablesession.Session{}, catalog.PublishedMenu{}, Cart{}, ErrDenied
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, tablesession.Session{}, catalog.PublishedMenu{}, Cart{}, err
	}
	fail := func(err error) (pgx.Tx, tablesession.Session, catalog.PublishedMenu, Cart, error) {
		_ = tx.Rollback(ctx)
		return nil, tablesession.Session{}, catalog.PublishedMenu{}, Cart{}, err
	}
	storeScope := catalog.StoreScope{TenantID: pre.Scope.TenantID, StoreID: pre.Scope.StoreID}
	menu, err := s.menus.LockStoreMenuForAdmissionTx(ctx, tx, storeScope)
	if err != nil {
		return fail(mapCatalogError(err))
	}
	session, err := tablesession.LockAndValidateGrantTx(ctx, tx, pre.Scope, sessionID, grantToken)
	if err != nil {
		return fail(ErrDenied)
	}
	if session.Status != tablesession.Open || session.Expired {
		return fail(ErrDenied)
	}
	if initialize {
		if _, err = tx.Exec(ctx, `INSERT INTO table_carts(session_id) VALUES ($1) ON CONFLICT(session_id) DO NOTHING`, session.ID); err != nil {
			return fail(err)
		}
	}
	var generation, version int64
	err = tx.QueryRow(ctx, `SELECT generation,version FROM table_carts WHERE session_id=$1 FOR UPDATE`, session.ID).Scan(&generation, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		cart := Cart{SessionID: session.ID, Generation: 1, Version: 1, Lines: make([]CartLine, 0)}
		trustedAt, clockErr := s.now(ctx, tx)
		if clockErr != nil {
			return fail(clockErr)
		}
		if _, checkErr := tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, grantToken, trustedAt); checkErr != nil {
			return fail(ErrDenied)
		}
		annotateCart(menu, &cart)
		return tx, session, menu, cart, nil
	}
	if err != nil {
		return fail(err)
	}
	cart, err := readCart(ctx, tx, session.ID)
	if err != nil {
		return fail(err)
	}
	cart.Generation, cart.Version = generation, version
	trustedAt, err := s.now(ctx, tx)
	if err != nil {
		return fail(err)
	}
	if _, err = tablesession.RecheckGrantTx(ctx, tx, session.Scope, session.ID, grantToken, trustedAt); err != nil {
		return fail(ErrDenied)
	}
	annotateCart(menu, &cart)
	return tx, session, menu, cart, nil
}

func readCart(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}, sessionID string) (Cart, error) {
	var cart Cart
	var generation, version int64
	if err := q.QueryRow(ctx, `SELECT generation,version FROM table_carts WHERE session_id=$1`, sessionID).Scan(&generation, &version); err != nil {
		return Cart{}, err
	}
	cart = Cart{SessionID: sessionID, Generation: generation, Version: version, Lines: make([]CartLine, 0)}
	rows, err := q.Query(ctx, `SELECT line_id,product_key,product_name,quantity,selections FROM table_cart_lines WHERE session_id=$1 AND generation=$2 ORDER BY created_at,line_id`, sessionID, generation)
	if err != nil {
		return Cart{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var line CartLine
		var raw []byte
		if err := rows.Scan(&line.LineID, &line.ProductKey, &line.ProductName, &line.Quantity, &raw); err != nil {
			return Cart{}, err
		}
		if err := json.Unmarshal(raw, &line.Options); err != nil {
			return Cart{}, err
		}
		if line.Options == nil {
			line.Options = make([]CartOption, 0)
		}
		cart.Lines = append(cart.Lines, line)
	}
	return cart, rows.Err()
}

func (s *Service) quote(ctx context.Context, tx pgx.Tx, session tablesession.Session, menu catalog.PublishedMenu, cart Cart) (catalog.MenuQuote, string, error) {
	if len(cart.Lines) == 0 || len(cart.Lines) > MaxCartLines {
		return catalog.MenuQuote{}, "", ErrEmptyCart
	}
	selections := make([]catalog.MenuLine, len(cart.Lines))
	for i, line := range cart.Lines {
		if !validLine(line.Quantity, lineSelections(line.Options)) {
			return catalog.MenuQuote{}, "", ErrUnavailable
		}
		selections[i] = catalog.MenuLine{LineID: line.LineID, ProductKey: line.ProductKey, Options: lineSelections(line.Options), Quantity: line.Quantity}
	}
	// The time is obtained after catalog, schedule, table/session, and cart locks.
	at, err := s.now(ctx, tx)
	if err != nil {
		return catalog.MenuQuote{}, "", err
	}
	quote, err := s.menus.QuoteDineInTx(ctx, tx, menu, at, selections)
	if err != nil {
		if errors.Is(err, catalog.ErrDenied) || errors.Is(err, catalog.ErrNotFound) {
			return catalog.MenuQuote{}, "", ErrUnavailable
		}
		return catalog.MenuQuote{}, "", err
	}
	basis := struct {
		SessionID   string            `json:"session_id"`
		Generation  int64             `json:"generation"`
		Version     int64             `json:"version"`
		CatalogID   int64             `json:"catalog_id"`
		CatalogMode string            `json:"catalog_mode"`
		Revision    int64             `json:"revision"`
		Sellability []sellabilitySeal `json:"sellability"`
		Quote       catalog.MenuQuote `json:"quote"`
	}{session.ID, cart.Generation, cart.Version, menu.CatalogID, menu.Mode, menu.Revision, sellabilityFor(menu, cart), quote}
	fingerprint, err := digestJSON(basis)
	if err != nil {
		return catalog.MenuQuote{}, "", err
	}
	return quote, hex.EncodeToString(fingerprint[:]), nil
}

type sellabilitySeal struct {
	ProductKey             string               `json:"product_key"`
	ProductActive          bool                 `json:"product_active"`
	ProductAvailability    catalog.Availability `json:"product_availability"`
	ProductAvailabilityVer int64                `json:"product_availability_version"`
	EffectivePriceMinor    int64                `json:"effective_price_minor"`
	PriceOverrideVersion   int64                `json:"price_override_version"`
	Options                []optionSeal         `json:"options"`
}

type optionSeal struct {
	GroupKey        string               `json:"group_key"`
	OptionKey       string               `json:"option_key"`
	Active          bool                 `json:"active"`
	Availability    catalog.Availability `json:"availability"`
	AvailabilityVer int64                `json:"availability_version"`
}

func sellabilityFor(menu catalog.PublishedMenu, cart Cart) []sellabilitySeal {
	seals := make([]sellabilitySeal, 0, len(cart.Lines))
	for _, line := range cart.Lines {
		var seal sellabilitySeal
		seal.ProductKey = line.ProductKey
		seal.Options = make([]optionSeal, 0, len(line.Options))
		for _, category := range menu.Categories {
			for _, product := range category.Products {
				if product.Key != line.ProductKey {
					continue
				}
				seal.ProductActive = product.Active
				seal.ProductAvailability = product.Availability
				seal.ProductAvailabilityVer = product.AvailabilityVersion
				seal.EffectivePriceMinor = product.EffectivePriceMinor
				seal.PriceOverrideVersion = product.PriceOverrideVersion
				for _, selected := range line.Options {
					state := optionSeal{GroupKey: selected.GroupKey, OptionKey: selected.OptionKey}
					for _, group := range product.Groups {
						if group.Key != selected.GroupKey {
							continue
						}
						for _, option := range group.Options {
							if option.Key == selected.OptionKey {
								state.Active = option.Active
								state.Availability = option.Availability
								state.AvailabilityVer = option.AvailabilityVersion
							}
						}
					}
					seal.Options = append(seal.Options, state)
				}
				break
			}
		}
		seals = append(seals, seal)
	}
	return seals
}

func mutationReplay(ctx context.Context, tx pgx.Tx, sessionID string, generation int64, key string, digest [32]byte) (int64, bool, error) {
	var existing []byte
	var version int64
	err := tx.QueryRow(ctx, `SELECT request_digest,result_version FROM table_cart_idempotency WHERE session_id=$1 AND generation=$2 AND idempotency_key=$3`, sessionID, generation, key).Scan(&existing, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !equalBytes(existing, digest[:]) {
		return 0, false, ErrIdempotencyConflict
	}
	return version, true, nil
}

func orderReplay(ctx context.Context, tx pgx.Tx, sessionID string, generation int64, key string, digest [32]byte) (string, bool, error) {
	var orderID string
	var existing []byte
	err := tx.QueryRow(ctx, `SELECT order_id,request_digest FROM table_order_idempotency WHERE session_id=$1 AND generation=$2 AND idempotency_key=$3`, sessionID, generation, key).Scan(&orderID, &existing)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !equalBytes(existing, digest[:]) {
		return "", false, ErrIdempotencyConflict
	}
	return orderID, true, nil
}

func bindOrderReplay(ctx context.Context, tx pgx.Tx, sessionID string, generation int64, key string, digest [32]byte, orderID string) error {
	_, err := tx.Exec(ctx, `INSERT INTO table_order_idempotency(session_id,generation,idempotency_key,request_digest,order_id)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT(session_id,generation,idempotency_key) DO NOTHING`, sessionID, generation, key, digest[:], orderID)
	return err
}

func loadOrderByGeneration(ctx context.Context, tx pgx.Tx, sessionID string, generation int64) (Order, bool, error) {
	var order Order
	err := tx.QueryRow(ctx, `SELECT order_id FROM table_orders WHERE session_id=$1 AND generation=$2`, sessionID, generation).Scan(&order.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, err
	}
	return order, true, nil
}

func resultForOrder(ctx context.Context, tx pgx.Tx, orderID string, outcome CommitOutcome) (CommitResult, error) {
	order, err := readOrder(ctx, tx, orderID)
	if err != nil {
		return CommitResult{}, err
	}
	cart, err := readCart(ctx, tx, order.SessionID)
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{Outcome: outcome, Order: order, Cart: cart}, nil
}

func readOrder(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, orderID string) (Order, error) {
	var order Order
	var raw []byte
	var created time.Time
	err := q.QueryRow(ctx, `SELECT order_id,session_id,generation,payment_method,requires_staff_acceptance,catalog_id,quote_snapshot,created_at FROM table_orders WHERE order_id=$1`, orderID).
		Scan(&order.ID, &order.SessionID, &order.Generation, &order.PaymentMethod, &order.RequiresStaffAcceptance, &order.CatalogID, &raw, &created)
	if err != nil {
		return Order{}, err
	}
	var snapshot storedSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return Order{}, err
	}
	order.Quote = snapshot.Quote
	order.CreatedAt = created.UTC().Format(time.RFC3339Nano)
	return order, nil
}

func insertLine(ctx context.Context, tx pgx.Tx, sessionID string, generation int64, line CartLine) error {
	selections, err := json.Marshal(line.Options)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO table_cart_lines(session_id,generation,line_id,product_key,product_name,quantity,selections) VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb)`, sessionID, generation, line.LineID, line.ProductKey, line.ProductName, line.Quantity, selections)
	return err
}

func updateLine(ctx context.Context, tx pgx.Tx, sessionID string, generation int64, line CartLine) error {
	selections, err := json.Marshal(line.Options)
	if err != nil {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE table_cart_lines SET product_name=$4,quantity=$5,selections=$6::jsonb,updated_at=clock_timestamp() WHERE session_id=$1 AND generation=$2 AND line_id=$3`, sessionID, generation, line.LineID, line.ProductName, line.Quantity, selections)
	if err == nil && command.RowsAffected() != 1 {
		return ErrConflict
	}
	return err
}

func validateSelectable(menu catalog.PublishedMenu, line CartLine) error {
	if _, err := catalog.Quote(menu, line.ProductKey, lineSelections(line.Options)); err != nil {
		if errors.Is(err, catalog.ErrDenied) || errors.Is(err, catalog.ErrNotFound) {
			return ErrUnavailable
		}
		return ErrInvalid
	}
	return nil
}

func enrichLine(menu catalog.PublishedMenu, line *CartLine, selections []catalog.OptionSelection) error {
	if _, err := catalog.Quote(menu, line.ProductKey, selections); err != nil {
		if errors.Is(err, catalog.ErrDenied) || errors.Is(err, catalog.ErrNotFound) {
			return ErrUnavailable
		}
		return ErrInvalid
	}
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			if product.Key != line.ProductKey {
				continue
			}
			line.ProductName = product.Name
			line.Options = make([]CartOption, 0, len(selections))
			for _, selection := range selections {
				var selected CartOption
				for _, group := range product.Groups {
					if group.Key != selection.GroupKey {
						continue
					}
					selected.GroupKey, selected.GroupName = group.Key, group.Name
					for _, option := range group.Options {
						if option.Key == selection.OptionKey {
							selected.OptionKey, selected.OptionName = option.Key, option.Name
							selected.Available = option.Active && option.Availability == catalog.Available
						}
					}
				}
				line.Options = append(line.Options, selected)
			}
			return nil
		}
	}
	return ErrUnavailable
}

func annotateCart(menu catalog.PublishedMenu, cart *Cart) {
	products := make(map[string]catalog.Product)
	for _, category := range menu.Categories {
		for _, product := range category.Products {
			products[product.Key] = product
		}
	}
	for i := range cart.Lines {
		line := &cart.Lines[i]
		product, found := products[line.ProductKey]
		line.ProductAvailable = found && product.Active && product.Availability == catalog.Available && product.Selectable
		line.UnavailableCode = ""
		if !found || !product.Active || product.Availability != catalog.Available || !product.Selectable {
			line.UnavailableCode = "PRODUCT_UNAVAILABLE"
		}
		currentOptions := make(map[string]catalog.Option)
		currentGroups := make(map[string]catalog.OptionGroup)
		if found {
			for _, group := range product.Groups {
				currentGroups[group.Key] = group
				for _, option := range group.Options {
					currentOptions[group.Key+"\x00"+option.Key] = option
				}
			}
		}
		for j := range line.Options {
			option := &line.Options[j]
			group, groupFound := currentGroups[option.GroupKey]
			current, optionFound := currentOptions[option.GroupKey+"\x00"+option.OptionKey]
			option.Available = groupFound && optionFound && current.Active && current.Availability == catalog.Available
			option.UnavailableCode = ""
			if !option.Available {
				option.UnavailableCode = "OPTION_UNAVAILABLE"
			}
			if groupFound {
				option.GroupName = group.Name
			}
			if optionFound {
				option.OptionName = current.Name
			}
		}
	}
}

func lineSelections(options []CartOption) []catalog.OptionSelection {
	selections := make([]catalog.OptionSelection, len(options))
	for i, option := range options {
		selections[i] = catalog.OptionSelection{GroupKey: option.GroupKey, OptionKey: option.OptionKey}
	}
	return selections
}

func validLine(quantity int64, options []catalog.OptionSelection) bool {
	if quantity < 1 || quantity > MaxLineQuantity || len(options) > MaxOptionsPerLine {
		return false
	}
	for _, option := range options {
		if !validCatalogKey(option.GroupKey) || !validCatalogKey(option.OptionKey) {
			return false
		}
	}
	return true
}

func validCatalogKey(value string) bool {
	return value != "" && len([]rune(value)) <= 100 && strings.TrimSpace(value) == value
}

func validIdempotencyKey(value string) bool {
	if value == "" || len(value) > MaxIdempotencyKeyLen || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return false
		}
	}
	return true
}

func validPayment(method PaymentMethod) bool { return method == Stripe || method == Cash }

func canonicalOptions(options []catalog.OptionSelection) []catalog.OptionSelection {
	out := append([]catalog.OptionSelection(nil), options...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].GroupKey == out[j].GroupKey {
			return out[i].OptionKey < out[j].OptionKey
		}
		return out[i].GroupKey < out[j].GroupKey
	})
	return out
}

func digestJSON(value any) ([32]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var mismatch byte
	for i := range left {
		mismatch |= left[i] ^ right[i]
	}
	return mismatch == 0
}

func mapCatalogError(err error) error {
	if errors.Is(err, catalog.ErrDenied) {
		return ErrDenied
	}
	if errors.Is(err, catalog.ErrNotFound) || errors.Is(err, catalog.ErrNotPublished) || errors.Is(err, catalog.ErrNotDetached) {
		return ErrUnavailable
	}
	return fmt.Errorf("ordering catalog admission: %w", err)
}
