package database

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/apimgr/cairnbin/src/model"
)

// unavailableDB keeps the server's database dependency non-nil when opening
// or initializing a database fails, allowing health and maintenance routes to
// start while connection probes retry the configured factory.
type unavailableDB struct {
	mu      sync.RWMutex
	db      DB
	err     error
	retry   func() (DB, error)
	retryMu sync.Mutex
}

var _ DB = (*unavailableDB)(nil)
var _ OwnerDB = (*unavailableDB)(nil)
var _ RateLimitDB = (*unavailableDB)(nil)
var _ OwnerPasteLister = (*unavailableDB)(nil)
var _ ConnectionProbe = (*unavailableDB)(nil)
var _ interface {
	ExportAppSecrets() ([]AppSecretRecord, error)
} = (*unavailableDB)(nil)
var _ interface {
	DeleteExpiredSecurityReports(time.Time) (int64, error)
} = (*unavailableDB)(nil)
var _ interface {
	FreshAppSecret(string, time.Duration) ([]byte, bool)
} = (*unavailableDB)(nil)

func newUnavailableDB(err error, retry func() (DB, error)) DB {
	if err == nil {
		err = fmt.Errorf("database is unavailable")
	}
	return &unavailableDB{err: err, retry: retry}
}

func (u *unavailableDB) active() (DB, error) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.db != nil {
		return u.db, nil
	}
	return nil, u.err
}

func (u *unavailableDB) reconnect() (DB, error) {
	u.retryMu.Lock()
	defer u.retryMu.Unlock()
	if db, err := u.active(); db != nil {
		return db, err
	}
	if u.retry == nil {
		_, err := u.active()
		return nil, err
	}
	db, err := u.retry()
	u.mu.Lock()
	if db != nil {
		u.db = db
	}
	if err != nil {
		u.err = err
	} else if db != nil {
		u.err = nil
	}
	u.mu.Unlock()
	if db == nil && err == nil {
		err = fmt.Errorf("database retry returned no connection")
	}
	return db, err
}

func (u *unavailableDB) use() (DB, error) {
	return u.active()
}

func (u *unavailableDB) Close() error {
	db, _ := u.active()
	if db == nil {
		return nil
	}
	return db.Close()
}

func (u *unavailableDB) Type() string {
	db, _ := u.active()
	if db != nil {
		return db.Type()
	}
	return "unavailable"
}

func (u *unavailableDB) Ping() error {
	db, err := u.reconnect()
	if err != nil {
		return err
	}
	return db.Ping()
}

func (u *unavailableDB) CheckConnection(ctx context.Context) error {
	db, err := u.reconnect()
	if err != nil {
		return err
	}
	if probe, ok := db.(ConnectionProbe); ok {
		return probe.CheckConnection(ctx)
	}
	return db.Ping()
}

func (u *unavailableDB) CreatePaste(paste *model.Paste) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.CreatePaste(paste)
}

func (u *unavailableDB) GetPasteByID(id string) (*model.Paste, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.GetPasteByID(id)
}

func (u *unavailableDB) GetPublicPastes(page, limit int) ([]model.PasteListItem, int, error) {
	db, err := u.use()
	if err != nil {
		return nil, 0, err
	}
	return db.GetPublicPastes(page, limit)
}

func (u *unavailableDB) SearchPublicPastes(page, limit int, search, sortBy, order string, pasteType ...string) ([]model.PasteListItem, int, error) {
	db, err := u.use()
	if err != nil {
		return nil, 0, err
	}
	return db.SearchPublicPastes(page, limit, search, sortBy, order, pasteType...)
}

func (u *unavailableDB) IncrementViewsAndCheckBurn(id string) (int, bool, error) {
	db, err := u.use()
	if err != nil {
		return 0, false, err
	}
	return db.IncrementViewsAndCheckBurn(id)
}

func (u *unavailableDB) DeletePaste(id string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.DeletePaste(id)
}

func (u *unavailableDB) DeletePasteByToken(id, deleteTokenHash string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.DeletePasteByToken(id, deleteTokenHash)
}

func (u *unavailableDB) DeleteExpiredPastes() (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	return db.DeleteExpiredPastes()
}

func (u *unavailableDB) DeleteBurnedPastes() (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	return db.DeleteBurnedPastes()
}

func (u *unavailableDB) UpsertSchedulerTask(task *TaskState) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.UpsertSchedulerTask(task)
}

func (u *unavailableDB) GetSchedulerTask(taskID string) (*TaskState, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.GetSchedulerTask(taskID)
}

func (u *unavailableDB) ListSchedulerTasks() ([]*TaskState, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.ListSchedulerTasks()
}

func (u *unavailableDB) UpdateTaskRun(taskID string, lastRun time.Time, status, lastError string, runCount, failCount int64, nextRun time.Time) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.UpdateTaskRun(taskID, lastRun, status, lastError, runCount, failCount, nextRun)
}

func (u *unavailableDB) SetTaskEnabled(taskID string, enabled bool) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.SetTaskEnabled(taskID, enabled)
}

func (u *unavailableDB) RecordTaskHistory(history *TaskHistory) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.RecordTaskHistory(history)
}

func (u *unavailableDB) ListTaskHistory(taskID string, limit int) ([]*TaskHistory, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.ListTaskHistory(taskID, limit)
}

func (u *unavailableDB) CreateAPIToken(hash, prefix, resourceType, resourceID string, expiresAt *time.Time) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.CreateAPIToken(hash, prefix, resourceType, resourceID, expiresAt)
}

func (u *unavailableDB) VerifyAPIToken(hash [32]byte, resourceType, resourceID string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.VerifyAPIToken(hash, resourceType, resourceID)
}

func (u *unavailableDB) ValidateAPIToken(hash [32]byte, resourceType string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.ValidateAPIToken(hash, resourceType)
}

func (u *unavailableDB) RevokeAPIToken(prefix, reason string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.RevokeAPIToken(prefix, reason)
}

func (u *unavailableDB) ListAPITokens() ([]*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.ListAPITokens()
}

func (u *unavailableDB) GetAPITokenByPrefix(prefix string) (*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.GetAPITokenByPrefix(prefix)
}

func (u *unavailableDB) DeleteExpiredAPITokens() (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	return db.DeleteExpiredAPITokens()
}

func (u *unavailableDB) FindResourceIDByToken(hash [32]byte, resourceType string) (string, error) {
	db, err := u.use()
	if err != nil {
		return "", err
	}
	return db.FindResourceIDByToken(hash, resourceType)
}

func (u *unavailableDB) EnsureAppSecret(key string) ([]byte, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.EnsureAppSecret(key)
}

func (u *unavailableDB) CountPastes() (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	return db.CountPastes()
}

func (u *unavailableDB) CreateSecurityReport(report *SecurityReport) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.CreateSecurityReport(report)
}

func (u *unavailableDB) GetSecurityReport(trackingID string) (*SecurityReport, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.GetSecurityReport(trackingID)
}

func (u *unavailableDB) UpdateSecurityReportStatus(trackingID, status, comment string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.UpdateSecurityReportStatus(trackingID, status, comment)
}

func (u *unavailableDB) ListDisclosedSecurityReports() ([]*SecurityReport, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.ListDisclosedSecurityReports()
}

func (u *unavailableDB) MarkSecurityReportTokenUsed(trackingID string, at time.Time) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.MarkSecurityReportTokenUsed(trackingID, at)
}

func (u *unavailableDB) GetSecurityKeypair() (*SecurityKeypair, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	return db.GetSecurityKeypair()
}

func (u *unavailableDB) UpsertSecurityKeypair(keypair *SecurityKeypair) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	return db.UpsertSecurityKeypair(keypair)
}

func (u *unavailableDB) CheckRateLimit(key string, now time.Time, limit int, window time.Duration) (bool, time.Duration, error) {
	db, err := u.use()
	if err != nil {
		return false, 0, err
	}
	store, ok := db.(RateLimitDB)
	if !ok {
		return false, 0, fmt.Errorf("active database does not support rate-limit storage")
	}
	return store.CheckRateLimit(key, now, limit, window)
}

func (u *unavailableDB) RateLimitExceeded(key string, now time.Time, limit int, window time.Duration) (bool, error) {
	db, err := u.use()
	if err != nil {
		return false, err
	}
	store, ok := db.(RateLimitDB)
	if !ok {
		return false, fmt.Errorf("active database does not support rate-limit storage")
	}
	return store.RateLimitExceeded(key, now, limit, window)
}

func (u *unavailableDB) ListAllAPITokens() ([]*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.ListAllAPITokens()
}

func (u *unavailableDB) PurgeIdleOwners(days int) (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return 0, fmt.Errorf("active database does not support owner tokens")
	}
	return store.PurgeIdleOwners(days)
}

func (u *unavailableDB) CreatePasteWithPrimaryToken(paste *model.Paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return fmt.Errorf("active database does not support owner tokens")
	}
	return store.CreatePasteWithPrimaryToken(paste, tokenHash, tokenPrefix, tokenSuffix, tokenEnc)
}

func (u *unavailableDB) CreateOwnerWithPrimaryToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return fmt.Errorf("active database does not support owner tokens")
	}
	return store.CreateOwnerWithPrimaryToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, tokenEnc)
}

func (u *unavailableDB) GetOwnerTokenByHash(tokenHash [32]byte) (*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.GetOwnerTokenByHash(tokenHash)
}

func (u *unavailableDB) GetOwnerTokenByHashReadOnly(tokenHash [32]byte) (*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.GetOwnerTokenByHashReadOnly(tokenHash)
}

func (u *unavailableDB) GetOwnerTokenByID(ownerID string, id int64) (*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.GetOwnerTokenByID(ownerID, id)
}

func (u *unavailableDB) GetOwnerTokenByPublicID(ownerID, publicID string) (*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.GetOwnerTokenByPublicID(ownerID, publicID)
}

func (u *unavailableDB) CreateLinkedToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, name, tokenEnc string, expiresAt *time.Time, maxActive int) (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return 0, fmt.Errorf("active database does not support owner tokens")
	}
	return store.CreateLinkedToken(ownerID, tokenHash, tokenPrefix, tokenSuffix, name, tokenEnc, expiresAt, maxActive)
}

func (u *unavailableDB) ListOwnerTokens(ownerID string) ([]*APITokenRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return nil, fmt.Errorf("active database does not support owner tokens")
	}
	return store.ListOwnerTokens(ownerID)
}

func (u *unavailableDB) RotateOwnerToken(ownerID string, id int64, tokenHash, tokenPrefix, tokenSuffix, tokenEnc string) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return fmt.Errorf("active database does not support owner tokens")
	}
	return store.RotateOwnerToken(ownerID, id, tokenHash, tokenPrefix, tokenSuffix, tokenEnc)
}

func (u *unavailableDB) UpdateOwnerToken(ownerID string, id int64, name string, expiresAt *time.Time, maxActive int) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return fmt.Errorf("active database does not support owner tokens")
	}
	return store.UpdateOwnerToken(ownerID, id, name, expiresAt, maxActive)
}

func (u *unavailableDB) RevokeOwnerToken(ownerID string, id int64) error {
	db, err := u.use()
	if err != nil {
		return err
	}
	store, ok := db.(OwnerDB)
	if !ok {
		return fmt.Errorf("active database does not support owner tokens")
	}
	return store.RevokeOwnerToken(ownerID, id)
}

func (u *unavailableDB) SearchOwnerPastes(ownerID string, page, limit int, search, sortBy, order string, pasteTypes ...string) ([]model.PasteListItem, int, error) {
	db, err := u.use()
	if err != nil {
		return nil, 0, err
	}
	store, ok := db.(OwnerPasteLister)
	if !ok {
		return nil, 0, fmt.Errorf("active database does not support owner paste listing")
	}
	return store.SearchOwnerPastes(ownerID, page, limit, search, sortBy, order, pasteTypes...)
}

func (u *unavailableDB) ExportAppSecrets() ([]AppSecretRecord, error) {
	db, err := u.use()
	if err != nil {
		return nil, err
	}
	store, ok := db.(interface {
		ExportAppSecrets() ([]AppSecretRecord, error)
	})
	if !ok {
		return nil, fmt.Errorf("active database does not support application secret export")
	}
	return store.ExportAppSecrets()
}

func (u *unavailableDB) DeleteExpiredSecurityReports(cutoff time.Time) (int64, error) {
	db, err := u.use()
	if err != nil {
		return 0, err
	}
	store, ok := db.(interface {
		DeleteExpiredSecurityReports(time.Time) (int64, error)
	})
	if !ok {
		return 0, fmt.Errorf("active database does not support security report retention")
	}
	return store.DeleteExpiredSecurityReports(cutoff)
}

func (u *unavailableDB) FreshAppSecret(key string, maxAge time.Duration) ([]byte, bool) {
	db, err := u.use()
	if err != nil {
		return nil, false
	}
	store, ok := db.(interface {
		FreshAppSecret(string, time.Duration) ([]byte, bool)
	})
	if !ok {
		return nil, false
	}
	return store.FreshAppSecret(key, maxAge)
}
