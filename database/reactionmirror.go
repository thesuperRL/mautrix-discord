package database

import (
	"database/sql"
	"errors"

	"go.mau.fi/util/dbutil"
	log "maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"
)

type ReactionMirrorQuery struct {
	db  *Database
	log log.Logger
}

const reactionMirrorSelect = `SELECT dc_chan_id, dc_chan_receiver, dc_msg_id, target_mxid, mirrored_emoji, summary_thread_id, summary_message_id FROM reaction_mirror_state`

func (q *ReactionMirrorQuery) GetByMessage(key PortalKey, messageID string) *ReactionMirrorState {
	row := q.db.QueryRow(reactionMirrorSelect+` WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND dc_msg_id=$3`, key.ChannelID, key.Receiver, messageID)
	if row == nil {
		return nil
	}
	return q.New().Scan(row)
}

func (q *ReactionMirrorQuery) IsMirrorThread(key PortalKey, threadID string) bool {
	if threadID == "" {
		return false
	}
	row := q.db.QueryRow(`SELECT 1 FROM reaction_mirror_state WHERE dc_chan_id=$1 AND dc_chan_receiver=$2 AND summary_thread_id=$3 LIMIT 1`, key.ChannelID, key.Receiver, threadID)
	var one int
	if err := row.Scan(&one); err != nil {
		return false
	}
	return one == 1
}

func (q *ReactionMirrorQuery) New() *ReactionMirrorState {
	return &ReactionMirrorState{
		db:  q.db,
		log: q.log,
	}
}

type ReactionMirrorState struct {
	db  *Database
	log log.Logger

	Channel          PortalKey
	MessageID        string
	TargetMXID       id.EventID
	MirroredEmoji    string
	SummaryThreadID  string
	SummaryMessageID string
}

func (s *ReactionMirrorState) Scan(row dbutil.Scannable) *ReactionMirrorState {
	err := row.Scan(&s.Channel.ChannelID, &s.Channel.Receiver, &s.MessageID, &s.TargetMXID, &s.MirroredEmoji, &s.SummaryThreadID, &s.SummaryMessageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		s.log.Errorfln("Failed to scan reaction mirror row: %v", err)
		return nil
	}
	return s
}

func (s *ReactionMirrorState) Upsert() {
	_, err := s.db.Exec(`
		INSERT INTO reaction_mirror_state (dc_chan_id, dc_chan_receiver, dc_msg_id, target_mxid, mirrored_emoji, summary_thread_id, summary_message_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (dc_chan_id, dc_chan_receiver, dc_msg_id) DO UPDATE SET
			target_mxid=excluded.target_mxid,
			mirrored_emoji=excluded.mirrored_emoji,
			summary_thread_id=excluded.summary_thread_id,
			summary_message_id=excluded.summary_message_id
	`, s.Channel.ChannelID, s.Channel.Receiver, s.MessageID, s.TargetMXID, s.MirroredEmoji, s.SummaryThreadID, s.SummaryMessageID)
	if err != nil {
		s.log.Errorfln("Failed to upsert reaction mirror state: %v", err)
	}
}
