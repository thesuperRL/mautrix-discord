package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.mau.fi/util/variationselector"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/database"
	"go.mau.fi/mautrix-discord/pkg/reactionmirror"
)

var discordReactionMirrorDebouncer = reactionmirror.NewDebouncer(300 * time.Millisecond)
var discordReactionMirrorErrorDebouncer = reactionmirror.NewDebouncer(2 * time.Minute)

func (portal *Portal) reactionMirrorSession() *discordgo.Session {
	user := portal.reactionSessionUser(nil)
	if user == nil || !user.TryLock() {
		return nil
	}
	defer user.Unlock()
	return user.Session
}

func (portal *Portal) scheduleReactionMirrorRefresh(msg *database.Message) {
	if msg == nil {
		return
	}
	key := portal.Key.ChannelID + "/" + msg.DiscordID
	discordReactionMirrorDebouncer.Schedule(key, func() {
		portal.refreshReactionMirror(msg)
	})
}

func (portal *Portal) matrixMemberDisplayName(_ context.Context, userID id.UserID) string {
	if puppet := portal.bridge.GetPuppetByMXID(userID); puppet != nil && puppet.Name != "" {
		return puppet.Name
	}
	if member, _ := portal.bridge.StateStore.GetMember(context.Background(), portal.MXID, userID); member != nil && member.Displayname != "" {
		return member.Displayname
	}
	return ""
}

func matrixEmojiToDiscordID(portal *Portal, evt *event.Event) string {
	content, ok := evt.Content.Parsed.(*event.ReactionEventContent)
	if !ok || content == nil {
		return ""
	}
	key := content.RelatesTo.Key
	if strings.HasPrefix(key, "mxc://") {
		uri, _ := id.ParseContentURI(key)
		if emojiInfo := portal.bridge.DMA.GetEmojiInfo(uri); emojiInfo != nil {
			return fmt.Sprintf("%s:%d", emojiInfo.Name, emojiInfo.EmojiID)
		}
		if emojiFile := portal.bridge.DB.File.GetEmojiByMXC(uri); emojiFile != nil && emojiFile.ID != "" && emojiFile.EmojiName != "" {
			return fmt.Sprintf("%s:%s", emojiFile.EmojiName, emojiFile.ID)
		}
		return ""
	}
	return variationselector.FullyQualify(key)
}

func mirroredEmojiReactorCount(aggregated reactionmirror.AggregatedReactions, discordEmoji string) int {
	for emoji, names := range aggregated {
		if emojiMatchesMirror(emoji, discordEmoji) {
			return len(names)
		}
	}
	return 0
}

func emojiMatchesMirror(displayKey, discordEmoji string) bool {
	if displayKey == discordEmoji {
		return true
	}
	if strings.HasPrefix(displayKey, ":") {
		name := strings.Trim(displayKey, ":")
		return strings.HasPrefix(discordEmoji, name+":")
	}
	return variationselector.FullyQualify(displayKey) == discordEmoji
}

func (portal *Portal) notifyReactionMirrorError(msg *database.Message, summary string) {
	if msg == nil || summary == "" || !portal.bridge.Config.Bridge.MessageErrorNotices {
		return
	}
	key := portal.Key.ChannelID + "/" + msg.DiscordID + "/error"
	discordReactionMirrorErrorDebouncer.Schedule(key, func() {
		portal.sendReactionMirrorError(msg, summary)
	})
}

func (portal *Portal) sendReactionMirrorError(msg *database.Message, summary string) {
	content := &event.MessageEventContent{}
	threadRoot := msg.MXID
	if msg.ThreadID != "" {
		threadRoot = ""
		if thread := portal.bridge.GetThreadByID(msg.ThreadID, nil); thread != nil {
			threadRoot = thread.RootMXID
		}
	}
	if threadRoot != "" {
		content.GetRelatesTo().SetThread(threadRoot, msg.MXID)
	}
	portal.sendErrorMessage(&event.Event{ID: msg.MXID, Content: event.Content{Parsed: content}}, "reaction summary", summary, true)
}

func (portal *Portal) refreshReactionMirror(msg *database.Message) {
	portal.reactionMirrorLock.Lock()
	defer portal.reactionMirrorLock.Unlock()

	ctx := context.Background()
	client := portal.MainIntent().Client
	if client == nil && portal.bridge.Bot != nil {
		client = portal.bridge.Bot.Client
	}
	if client == nil {
		portal.log.Warn().Str("message_id", msg.DiscordID).Msg("Skipping reaction mirror refresh: no Matrix client")
		portal.notifyReactionMirrorError(msg, "no Matrix client")
		return
	}
	targetEvt, err := client.GetEvent(context.Background(), portal.MXID, msg.MXID)
	if err != nil {
		return
	}
	_ = targetEvt.Content.ParseRaw(targetEvt.Type)
	if content, ok := targetEvt.Content.Parsed.(*event.MessageEventContent); !ok || !reactionmirror.MessageHasMirrorReactionTrigger(content.Body) {
		return
	}
	session := portal.reactionMirrorSession()
	if session == nil {
		portal.log.Warn().Str("message_id", msg.DiscordID).Msg("Skipping reaction mirror refresh—no logged-in Discord session")
		portal.notifyReactionMirrorError(msg, "Ask a bridge administrator to connect the Discord reaction summary sender")
		return
	}
	lookup := func(ctx context.Context, roomID id.RoomID, userID id.UserID) string {
		return portal.matrixMemberDisplayName(ctx, userID)
	}
	aggregated, err := reactionmirror.AggregateReactions(ctx, client, portal.MXID, msg.MXID, lookup, nil, nil)
	if err != nil {
		portal.log.Err(err).Str("message_id", msg.DiscordID).Msg("Failed to aggregate reactions for mirror summary")
		portal.notifyReactionMirrorError(msg, "Could not read the Matrix reactions; check that the bridge can read this room")
		return
	}

	events, err := reactionmirror.FetchAnnotationRelations(ctx, client, portal.MXID, msg.MXID)
	if err != nil {
		portal.log.Err(err).Msg("Failed to fetch reaction relations")
		portal.notifyReactionMirrorError(msg, "Could not read the Matrix reactions; check that the bridge can read this room")
		return
	}

	state := portal.bridge.DB.ReactionMirror.GetByMessage(portal.Key, msg.DiscordID)
	if state == nil {
		state = portal.bridge.DB.ReactionMirror.New()
		state.Channel = portal.Key
		state.MessageID = msg.DiscordID
		state.TargetMXID = msg.MXID
	}

	if state.MirroredEmoji == "" {
		if _, ok := reactionmirror.FirstCrossBridgeReaction(reactionmirror.IsSlackSourcedReaction, events); ok {
			for _, evt := range events {
				if evt.Unsigned.RedactedBecause != nil || !reactionmirror.IsSlackSourcedReaction(evt) {
					continue
				}
				if discordEmoji := matrixEmojiToDiscordID(portal, evt); discordEmoji != "" {
					state.MirroredEmoji = discordEmoji
					break
				}
			}
		}
	}

	if state.MirroredEmoji != "" {
		if mirroredEmojiReactorCount(aggregated, state.MirroredEmoji) > 0 {
			_ = session.MessageReactionAddUser(portal.GuildID, msg.DiscordProtoChannelID(), msg.DiscordID, state.MirroredEmoji)
		} else {
			_ = session.MessageReactionRemoveUser(portal.GuildID, msg.DiscordProtoChannelID(), msg.DiscordID, state.MirroredEmoji, "@me")
		}
	}

	portal.forwardBackfillLock.Lock()
	defer portal.forwardBackfillLock.Unlock()
	summaryBody := reactionmirror.FormatSummary(aggregated)
	threadID, summaryMsgID, err := portal.ensureReactionMirrorThread(session, msg, state, summaryBody)
	if err != nil {
		portal.log.Err(err).Msg("Failed to update reaction mirror summary thread")
		if state.MirroredEmoji != "" {
			state.Upsert()
		}
		summary := "Check that the Discord reaction summary sender can view and send messages in this thread"
		if state.SummaryThreadID == "" {
			summary = err.Error()
		}
		portal.notifyReactionMirrorError(msg, summary)
		return
	}
	state.SummaryThreadID = threadID
	state.SummaryMessageID = summaryMsgID
	state.TargetMXID = msg.MXID
	state.Upsert()
}

func (portal *Portal) ensureReactionMirrorThread(session *discordgo.Session, msg *database.Message, state *database.ReactionMirrorState, body string) (threadID, summaryMsgID string, err error) {
	threadID = portal.resolveReactionMirrorThreadID(session, msg, state)
	if threadID == "" {
		threadID, err = portal.startDiscordThread(msg, "Reactions", nil)
		if err != nil {
			return "", "", err
		}
	}
	state.SummaryThreadID = threadID

	summaryMsgID = state.SummaryMessageID
	if summaryMsgID == "" {
		summaryMsgID = portal.findReactionSummaryMessage(session, threadID)
	}
	options := portal.RefererOptIfUser(session, threadID)
	editSummary := func(messageID string) error {
		_, err := session.ChannelMessageEditComplex(&discordgo.MessageEdit{
			Channel: threadID,
			ID:      messageID,
			Content: &body,
		}, options...)
		return err
	}
	if summaryMsgID != "" {
		err = editSummary(summaryMsgID)
		if err == nil {
			return threadID, summaryMsgID, nil
		}
		var restErr *discordgo.RESTError
		if !errors.As(err, &restErr) || restErr.Message == nil || restErr.Message.Code != discordgo.ErrCodeUnknownMessage {
			return threadID, summaryMsgID, fmt.Errorf("edit reaction summary: %w", err)
		}
		if found := portal.findReactionSummaryMessage(session, threadID); found != "" && found != summaryMsgID {
			return threadID, found, editSummary(found)
		}
	}

	sent, err := session.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{Content: body}, options...)
	if err != nil {
		return threadID, summaryMsgID, fmt.Errorf("post reaction summary: %w", err)
	}
	return threadID, sent.ID, nil
}

func (portal *Portal) resolveReactionMirrorThreadID(session *discordgo.Session, msg *database.Message, state *database.ReactionMirrorState) string {
	if state.SummaryThreadID != "" {
		return state.SummaryThreadID
	}
	if msg.ThreadID != "" {
		return msg.ThreadID
	}
	if dbThread := portal.bridge.DB.Thread.GetByMatrixRootMsg(msg.MXID); dbThread != nil {
		return dbThread.ID
	}
	return portal.fetchReactionMirrorThreadFromMessage(session, msg)
}

func (portal *Portal) fetchReactionMirrorThreadFromMessage(session *discordgo.Session, msg *database.Message) string {
	dmsg, err := session.ChannelMessage(portal.Key.ChannelID, msg.DiscordID, portal.RefererOptIfUser(session, "")...)
	if err != nil || dmsg == nil || dmsg.Thread == nil {
		return ""
	}
	return dmsg.Thread.ID
}

func (portal *Portal) findReactionSummaryMessage(session *discordgo.Session, threadID string) string {
	messages, err := session.ChannelMessages(threadID, 50, "", "", "", portal.RefererOptIfUser(session, threadID)...)
	if err != nil {
		return ""
	}
	for _, message := range messages {
		if message == nil {
			continue
		}
		if strings.HasPrefix(message.Content, "Reactions\n") || strings.HasPrefix(message.Content, "Reaction summary error:") {
			return message.ID
		}
	}
	return ""
}

func (portal *Portal) shouldSkipReactionMirrorMessage(msg *discordgo.Message) bool {
	return msg.ChannelID != portal.Key.ChannelID && portal.bridge.DB.ReactionMirror.IsSummaryMessage(portal.Key, msg.ChannelID, msg.ID)
}

func (portal *Portal) reactionSessionUser(_ *User) *User {
	if u := portal.pickReactionMirrorUser(portal.bridge.DB.GetUsersInPortal(portal.reactionMirrorLookupID())); u != nil {
		return u
	}
	domain := portal.bridge.Config.Homeserver.Domain
	for _, localpart := range reactionMirrorPreferredLocalparts {
		if u := portal.bridge.GetUserByMXID(id.NewUserID(localpart, domain)); u != nil && u.IsLoggedIn() && u.Session != nil {
			return u
		}
	}
	return portal.bridge.getLoggedInUserForPortal()
}

var reactionMirrorPreferredLocalparts = []string{"reconciler", "bridge-bot", "thesuperrl"}

func (portal *Portal) reactionMirrorLookupID() string {
	if portal.GuildID != "" {
		return portal.GuildID
	}
	return portal.Key.ChannelID
}

func (portal *Portal) pickReactionMirrorUser(candidates []id.UserID) *User {
	inPortal := make(map[id.UserID]struct{}, len(candidates))
	for _, mxid := range candidates {
		inPortal[mxid] = struct{}{}
	}
	domain := portal.bridge.Config.Homeserver.Domain
	for _, localpart := range reactionMirrorPreferredLocalparts {
		mxid := id.NewUserID(localpart, domain)
		if _, ok := inPortal[mxid]; !ok {
			continue
		}
		if u := portal.bridge.GetUserByMXID(mxid); u != nil && u.IsLoggedIn() && u.Session != nil {
			return u
		}
	}
	for _, mxid := range candidates {
		if mxid.Localpart() == "ap-1" {
			continue
		}
		if u := portal.bridge.GetUserByMXID(mxid); u != nil && u.IsLoggedIn() && u.Session != nil {
			return u
		}
	}
	return nil
}

func (portal *Portal) isMatrixReactionCrossBridge(evt *event.Event) bool {
	if reactionmirror.IsSlackSourcedReaction(evt) {
		return true
	}
	return reactionmirror.IsDiscordBridgeGhost(evt.Sender)
}
