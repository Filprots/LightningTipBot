package telegram

import (
	"fmt"
	"github.com/eko/gocache/store"
	"reflect"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/LightningTipBot/LightningTipBot/internal"
	"github.com/LightningTipBot/LightningTipBot/internal/database"
	"github.com/LightningTipBot/LightningTipBot/internal/storage"
	"github.com/tidwall/buntdb"

	log "github.com/sirupsen/logrus"

	"github.com/LightningTipBot/LightningTipBot/internal/lnbits"
	tb "gopkg.in/tucnak/telebot.v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	MessageOrderedByReplyToFrom = "message.reply_to_message.from.id"
	TipTooltipKeyPattern        = "tip-tool-tip:*"
)

func createBunt() *storage.DB {
	// create bunt database
	bunt := storage.NewBunt(internal.Configuration.Database.BuntDbPath)
	// create bunt database index for ascending (searching) TipTooltips
	err := bunt.CreateIndex(MessageOrderedByReplyToFrom, TipTooltipKeyPattern, buntdb.IndexJSON(MessageOrderedByReplyToFrom))
	if err != nil {
		panic(err)
	}
	return bunt
}

type cachedUser struct {
	goodUntil time.Duration
	*lnbits.User
}

func (bot *TipBot) enableUsersCache() chan *lnbits.User {
	cacheChan := make(chan *lnbits.User, 1)
	go func() {
		// create routine recovery
		var withRecovery = func() {
			if r := recover(); r != nil {
				log.Errorln("Recovered panic: ", r)
				debug.PrintStack()
			}
		}
		defer withRecovery()
		// fetch all users in interval i and update in map
		for {
			user := <-cacheChan
			// todo -- configurable good until
			bot.Cache.Set(user.Name, user, &store.Options{Expiration: 10 * time.Second})
		}
	}()
	return cacheChan
}

func ColumnMigrationTasks(db *gorm.DB) error {
	var err error
	if !db.Migrator().HasColumn(&lnbits.User{}, "anon_id") {
		// first we need to auto migrate the user. This will create anon_id column
		err = db.AutoMigrate(&lnbits.User{})
		if err != nil {
			panic(err)
		}
		log.Info("Running ano_id database migrations ...")
		// run the migration on anon_id
		err = database.MigrateAnonIdHash(db)
	}
	// todo -- add more database field migrations here in the future
	return err
}

func AutoMigration() (db *gorm.DB, txLogger *gorm.DB) {
	orm, err := gorm.Open(sqlite.Open(internal.Configuration.Database.DbPath), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, FullSaveAssociations: true})
	if err != nil {
		panic("Initialize orm failed.")
	}
	err = ColumnMigrationTasks(orm)
	if err != nil {
		panic(err)
	}
	err = orm.AutoMigrate(&lnbits.User{})
	if err != nil {
		panic(err)
	}

	txLogger, err = gorm.Open(sqlite.Open(internal.Configuration.Database.TransactionsPath), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true, FullSaveAssociations: true})
	if err != nil {
		panic("Initialize orm failed.")
	}
	err = txLogger.AutoMigrate(&Transaction{})
	if err != nil {
		panic(err)
	}
	return orm, txLogger
}

func GetUserByTelegramUsername(toUserStrWithoutAt string, bot TipBot) (*lnbits.User, error) {
	toUserDb := &lnbits.User{}
	tx := bot.Database.Where("telegram_username = ? COLLATE NOCASE", toUserStrWithoutAt).First(toUserDb)
	if tx.Error != nil || toUserDb.Wallet == nil {
		err := tx.Error
		if toUserDb.Wallet == nil {
			err = fmt.Errorf("%s | user @%s has no wallet", tx.Error, toUserStrWithoutAt)
		}
		return nil, err
	}
	return toUserDb, nil
}
func getCachedUser(u *tb.User, bot TipBot) (*lnbits.User, error) {
	user := &lnbits.User{Name: strconv.Itoa(u.ID)}
	if us, err := bot.Cache.Get(user.Name); err == nil {
		return us.(*lnbits.User), nil
	}
	user.Telegram = u
	return user, gorm.ErrRecordNotFound
}

// GetLnbitsUser will not update the user in Database.
// this is required, because fetching lnbits.User from a incomplete tb.User
// will update the incomplete (partial) user in storage.
// this function will accept users like this:
// &tb.User{ID: toId, Username: username}
// without updating the user in storage.
func GetLnbitsUser(u *tb.User, bot TipBot) (*lnbits.User, error) {
	user := &lnbits.User{Name: strconv.Itoa(u.ID)}
	tx := bot.Database.First(user)
	if tx.Error != nil {
		errmsg := fmt.Sprintf("[GetUser] Couldn't fetch %s from Database: %s", GetUserStr(u), tx.Error.Error())
		log.Warnln(errmsg)
		user.Telegram = u
		return user, tx.Error
	}
	// todo -- unblock this !
	bot.Cache.userChan <- user
	return user, nil
}

// GetUser from Telegram user. Update the user if user information changed.
func GetUser(u *tb.User, bot TipBot) (*lnbits.User, error) {
	var user *lnbits.User
	var err error
	if user, err = getCachedUser(u, bot); err != nil {
		user, err = GetLnbitsUser(u, bot)
		if err != nil {
			return user, err
		}
		updateCachedUser(user, bot)
	}
	if telegramUserChanged(u, user.Telegram) {
		// update possibly changed user details in Database
		user.Telegram = u
		updateCachedUser(user, bot)
		go updatePersistentUser(user, bot)
	}
	return user, err
}
func updateCachedUser(apiUser *lnbits.User, bot TipBot) {
	bot.Cache.Set(apiUser.Name, apiUser, &store.Options{Expiration: 10 * time.Second})
}

func updatePersistentUser(apiUser *lnbits.User, bot TipBot) {
	err := UpdateUserRecord(apiUser, bot)
	if err != nil {
		log.Warnln(fmt.Sprintf("[UpdateUserRecord] %s", err.Error()))
	}
}

func telegramUserChanged(apiUser, stateUser *tb.User) bool {
	if reflect.DeepEqual(apiUser, stateUser) {
		return false
	}
	return true
}

func UpdateUserRecord(user *lnbits.User, bot TipBot) error {
	user.UpdatedAt = time.Now()
	tx := bot.Database.Save(user)
	if tx.Error != nil {
		errmsg := fmt.Sprintf("[UpdateUserRecord] Error: Couldn't update %s's info in Database.", GetUserStr(user.Telegram))
		log.Errorln(errmsg)
		return tx.Error
	}
	log.Debugf("[UpdateUserRecord] Records of user %s updated.", GetUserStr(user.Telegram))
	return nil
}
