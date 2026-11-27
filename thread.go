package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/rs/zerolog"
	"golang.org/x/exp/slices"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
)

type Thread struct {
	*database.Thread
	Parent *Portal

	creationNoticeLock       sync.Mutex
	initialBackfillAttempted bool
}

func (br *DiscordBridge) threadCreationSession() *discordgo.Session {
	for _, relayID := range br.Config.Bridge.Relay.DefaultRelays {
		var user *User
		var discordID string
		if strings.HasPrefix(relayID, "@") {
			user = br.GetCachedUserByMXID(id.UserID(relayID))
		} else if relayID != "" {
			user = br.GetCachedUserByID(relayID)
			discordID = relayID
		}
		if user == nil || !user.TryLock() {
			continue
		}
		session := user.Session
		user.Unlock()
		if session != nil && authenticatedThreadBot(session, discordID) {
			return session
		}
	}
	return nil
}

func authenticatedThreadBot(session *discordgo.Session, discordID string) bool {
	for attempts := 0; !session.TryRLock(); attempts++ {
		if attempts == 2 {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	defer session.RUnlock()
	token, isBotToken := strings.CutPrefix(session.Token, "Bot ")
	if session.IsUser || !isBotToken || strings.TrimSpace(token) == "" || !session.DataReady || session.State == nil {
		return false
	}
	session.State.RLock()
	defer session.State.RUnlock()
	account := session.State.User
	return account != nil && account.Bot && account.ID != "" && (discordID == "" || account.ID == discordID)
}

func (portal *Portal) startDiscordThread(root *database.Message, name string, discoverySession *discordgo.Session) (string, error) {
	if thread := portal.bridge.GetThreadByID(root.DiscordID, nil); thread != nil {
		if thread.ParentID != portal.Key.ChannelID {
			return "", errors.New("Discord thread belongs to a different channel")
		}
		return thread.ID, nil
	}
	if summary := portal.bridge.DB.ReactionMirror.GetByMessage(portal.Key, root.DiscordID); summary != nil && summary.SummaryThreadID == root.DiscordID {
		return portal.bridge.GetThreadByID(root.DiscordID, root).ID, nil
	}
	if discoverySession != nil {
		thread, err := discoverySession.Channel(root.DiscordID)
		if err == nil && portal.matchesDiscordThread(thread, root.DiscordID) {
			return portal.bridge.GetThreadByID(thread.ID, root).ID, nil
		}
	}
	if name == "" {
		rootEvt, err := portal.getEvent(root.MXID)
		if err != nil {
			return "", errors.New("Could not read the Matrix thread root. Check that the bridge can read the event")
		}
		name = genThreadName(rootEvt)
	}

	session := portal.bridge.threadCreationSession()
	if session == nil {
		return "", errNoThreadBot
	}

	thread, err := session.MessageThreadStart(portal.Key.ChannelID, root.DiscordID, name, 24*60)
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Message != nil && restErr.Message.Code == discordgo.ErrCodeThreadAlreadyCreatedForThisMessage {
		thread, err = session.Channel(root.DiscordID)
	}
	if err != nil {
		return "", discordThreadCreationError(err)
	}
	if !portal.matchesDiscordThread(thread, root.DiscordID) {
		return "", errors.New("Discord returned an unexpected thread; check the source message and try again")
	}
	portal.bridge.GetThreadByID(thread.ID, root)
	return thread.ID, nil
}

func (portal *Portal) matchesDiscordThread(thread *discordgo.Channel, rootID string) bool {
	return thread != nil && thread.ID == rootID && thread.ParentID == portal.Key.ChannelID &&
		(thread.Type == discordgo.ChannelTypeGuildPublicThread || thread.Type == discordgo.ChannelTypeGuildNewsThread)
}

var errThreadBotAccess = fmt.Errorf("%w: the Discord thread bot needs View Channel and Create Public Threads in this channel", errCantStartThread)

func discordThreadCreationError(err error) error {
	// REST errors can contain arbitrary response bodies. Report only known, safe details.
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) {
		if restErr.Response != nil {
			switch restErr.Response.StatusCode {
			case http.StatusUnauthorized:
				return fmt.Errorf("%w: Discord rejected the thread bot's token; ask a bridge administrator to sign it in again", errCantStartThread)
			case http.StatusForbidden:
				return errThreadBotAccess
			case http.StatusNotFound:
				return fmt.Errorf("%w: the Discord thread bot could not find the source message or thread; check that it exists and the bot can view the channel", errCantStartThread)
			}
		}
		if restErr.Message != nil {
			switch restErr.Message.Code {
			case discordgo.ErrCodeMissingAccess, discordgo.ErrCodeMissingPermissions:
				return errThreadBotAccess
			case discordgo.ErrCodeThreadIsLocked, discordgo.ErrCodePerformedOperationOnArchivedThread:
				return fmt.Errorf("%w: ask a Discord moderator to unlock or reopen the thread", errCantStartThread)
			case discordgo.ErrCodeMaximumNumberOfActiveThreadsReached, discordgo.ErrCodeMaximumNumberOfActiveAnnouncementThreadsReached:
				return errors.New("Discord has reached its active thread limit; try again after a thread is archived")
			}
		}
	}
	return errors.New("Discord thread creation failed; try again, or ask a bridge administrator to check the bot and source channel")
}

func (br *DiscordBridge) GetThreadByID(id string, root *database.Message) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByID[id]
	if !ok {
		return br.loadThread(br.DB.Thread.GetByDiscordID(id), id, root)
	}
	return thread
}

func (br *DiscordBridge) GetThreadByRootMXID(mxid id.EventID) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByRootMXID[mxid]
	if !ok {
		return br.loadThread(br.DB.Thread.GetByMatrixRootMsg(mxid), "", nil)
	}
	return thread
}

func (br *DiscordBridge) GetThreadByRootOrCreationNoticeMXID(mxid id.EventID) *Thread {
	br.threadsLock.Lock()
	defer br.threadsLock.Unlock()
	thread, ok := br.threadsByRootMXID[mxid]
	if !ok {
		thread, ok = br.threadsByCreationNoticeMXID[mxid]
		if !ok {
			return br.loadThread(br.DB.Thread.GetByMatrixRootOrCreationNoticeMsg(mxid), "", nil)
		}
	}
	return thread
}

func (br *DiscordBridge) loadThread(dbThread *database.Thread, id string, root *database.Message) *Thread {
	if dbThread == nil {
		if root == nil {
			return nil
		}
		dbThread = br.DB.Thread.New()
		dbThread.ID = id
		dbThread.RootDiscordID = root.DiscordID
		dbThread.RootMXID = root.MXID
		dbThread.ParentID = root.Channel.ChannelID
		dbThread.Insert()
	}
	thread := &Thread{
		Thread: dbThread,
	}
	thread.Parent = br.GetExistingPortalByID(database.NewPortalKey(thread.ParentID, ""))
	br.threadsByID[thread.ID] = thread
	br.threadsByRootMXID[thread.RootMXID] = thread
	if thread.CreationNoticeMXID != "" {
		br.threadsByCreationNoticeMXID[thread.CreationNoticeMXID] = thread
	}
	return thread
}

func (br *DiscordBridge) threadFound(ctx context.Context, source *User, rootMessage *database.Message, id string, metadata *discordgo.Channel) {
	thread := br.GetThreadByID(id, rootMessage)
	log := zerolog.Ctx(ctx)
	log.Debug().Msg("Marked message as thread root")
	// Do not bridge thread creation notices to Matrix (bad Slack relay attribution).
	// TODO member_ids_preview is probably not guaranteed to contain the source user
	if source != nil && metadata != nil && slices.Contains(metadata.MemberIDsPreview, source.DiscordID) && !source.IsInPortal(thread.ID) {
		source.MarkInPortal(database.UserPortal{
			DiscordID: thread.ID,
			Type:      database.UserPortalTypeThread,
			Timestamp: time.Now(),
		})
		if metadata.MessageCount > 0 {
			go thread.maybeInitialBackfill(source)
		} else {
			thread.initialBackfillAttempted = true
		}
	}
}

func (thread *Thread) maybeInitialBackfill(source *User) {
	if thread.initialBackfillAttempted || thread.Parent.bridge.Config.Bridge.Backfill.Limits.Initial.Thread == 0 {
		return
	}
	thread.Parent.forwardBackfillLock.Lock()
	if thread.Parent.bridge.DB.Message.GetLastInThread(thread.Parent.Key, thread.ID) != nil {
		thread.Parent.forwardBackfillLock.Unlock()
		return
	}
	thread.Parent.forwardBackfillInitial(source, thread)
}

func (thread *Thread) RefererOpt() discordgo.RequestOption {
	return discordgo.WithThreadReferer(thread.Parent.GuildID, thread.ParentID, thread.ID)
}

func (thread *Thread) Join(user *User) {
	if user.IsInPortal(thread.ID) {
		return
	}
	log := user.log.With().Str("thread_id", thread.ID).Str("channel_id", thread.ParentID).Logger()
	log.Debug().Msg("Joining thread")

	var doBackfill, backfillStarted bool
	if !thread.initialBackfillAttempted && thread.Parent.bridge.Config.Bridge.Backfill.Limits.Initial.Thread > 0 {
		thread.Parent.forwardBackfillLock.Lock()
		lastMessage := thread.Parent.bridge.DB.Message.GetLastInThread(thread.Parent.Key, thread.ID)
		if lastMessage != nil {
			thread.Parent.forwardBackfillLock.Unlock()
		} else {
			doBackfill = true
			defer func() {
				if !backfillStarted {
					thread.Parent.forwardBackfillLock.Unlock()
				}
			}()
		}
	}

	var err error
	if user.Session.IsUser {
		err = user.Session.ThreadJoin(thread.ID, discordgo.WithLocationParam(discordgo.ThreadJoinLocationContextMenu), thread.RefererOpt())
	} else {
		err = user.Session.ThreadJoin(thread.ID)
	}
	if err != nil {
		log.Error().Err(err).Msg("Error joining thread")
	} else {
		user.MarkInPortal(database.UserPortal{
			DiscordID: thread.ID,
			Type:      database.UserPortalTypeThread,
			Timestamp: time.Now(),
		})
		if doBackfill {
			go thread.Parent.forwardBackfillInitial(user, thread)
			backfillStarted = true
		}
	}
}
