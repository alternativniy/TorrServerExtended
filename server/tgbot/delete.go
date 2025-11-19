package tgbot

import (
	"server/torr"

	tele "gopkg.in/telebot.v4"
)

func deleteTorrent(c tele.Context) {
	args := c.Args()
	hash := args[1]
	torr.RemTorrent(hash, false)
	return
}

func clear(c tele.Context) error {
	torrents := torr.ListTorrent()
	for _, t := range torrents {
		torr.RemTorrent(t.TorrentSpec.InfoHash.HexString(), false)
	}
	return nil
}
