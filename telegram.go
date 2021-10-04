package main

import (
	"github.com/LightningTipBot/LightningTipBot/internal/limiter"
	log "github.com/sirupsen/logrus"
	tb "gopkg.in/tucnak/telebot.v2"
	"strconv"
	"time"
)

func checkLimit(limiter *limiter.ChatIDRateLimiter, chatId string) {
	rl := limiter.GetLimiter(chatId)
	if !rl.Allow() {
		time.Sleep(1)
		checkLimit(limiter, chatId)
	}
}

func (bot TipBot) tryForwardMessage(to tb.Recipient, what tb.Editable, options ...interface{}) (msg *tb.Message) {
	checkLimit(bot.limiter, to.Recipient())
	msg, err := bot.telegram.Forward(to, what, options...)
	if err != nil {
		log.Errorln(err.Error())
	}
	return
}
func (bot TipBot) trySendMessage(to tb.Recipient, what interface{}, options ...interface{}) (msg *tb.Message) {
	checkLimit(bot.limiter, to.Recipient())
	msg, err := bot.telegram.Send(to, what, options...)
	if err != nil {
		log.Errorln(err.Error())
	}
	return
}

func (bot TipBot) tryReplyMessage(to *tb.Message, what interface{}, options ...interface{}) (msg *tb.Message) {
	checkLimit(bot.limiter, strconv.FormatInt(to.Chat.ID, 10))
	msg, err := bot.telegram.Reply(to, what, options...)
	if err != nil {
		log.Errorln(err.Error())
	}
	return
}

func (bot TipBot) tryEditMessage(to tb.Editable, what interface{}, options ...interface{}) (msg *tb.Message) {
	msg, err := bot.telegram.Edit(to, what, options...)
	if err != nil {
		log.Errorln(err.Error())
	}
	return
}

func (bot TipBot) tryDeleteMessage(msg tb.Editable) {
	err := bot.telegram.Delete(msg)
	if err != nil {
		log.Errorln(err.Error())
	}
}
