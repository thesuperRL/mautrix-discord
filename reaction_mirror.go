package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
	"go.mau.fi/util/variationselector"

	"go.mau.fi/mautrix-discord/database"
	"go.mau.fi/mautrix-discord/pkg/reactionmirror"
)

var discordReactionMirrorDebouncer = reactionmirror.NewDebouncer(300 * time.Millisecond)
var discordReactionMirrorErrorDebouncer = reactionmirror.NewDebouncer(2 * time.Minute)

func (portal *Portal) reactionMirrorBotUser() *User {
	return portal.reactionSessionUser(nil)
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

func (portal *Portal) notifyReactionMirrorError(msg *database.Message, state *database.ReactionMirrorState, summary string) {
	if msg == nil || summary == "" {
		return
	}
	key := portal.Key.ChannelID + "/" + msg.DiscordID + "/error"
	discordReactionMirrorErrorDebouncer.Schedule(key, func() {
		portal.postReactionMirrorError(msg, state, summary)
	})
}

func (portal *Portal) postReactionMirrorError(msg *database.Message, state *database.ReactionMirrorState, summary string) {
	body := fmt.Sprintf("Reaction summary error: %s", summary)
	threadID := msg.ThreadID
	if state != nil && state.SummaryThreadID != "" {
		threadID = state.SummaryThreadID
	}
	channelID := msg.DiscordProtoChannelID()

	botUser := portal.reactionMirrorBotUser()
	if botUser != nil && botUser.Session != nil {
		targetChannel := threadID
		if targetChannel == "" {
			targetChannel = channelID
		}
		send := &discordgo.MessageSend{Content: body}
		if threadID == "" {
			send.Reference = &discordgo.MessageReference{
				MessageID: msg.DiscordID,
				ChannelID: channelID,
				GuildID:   portal.GuildID,
			}
		}
		if _, err := botUser.Session.ChannelMessageSendComplex(targetChannel, send, portal.RefererOptIfUser(botUser.Session, threadID)...); err != nil {
			portal.log.Err(err).Str("message_id", msg.DiscordID).Msg("Failed to post reaction mirror error to Discord")
		}
		return
	}

	if portal.RelayWebhookID == "" {
		portal.log.Warn().Str("message_id", msg.DiscordID).Str("summary", summary).Msg("Cannot post reaction mirror error to Discord: no session or relay webhook")
		return
	}
	params := &discordgo.WebhookParams{Content: body, Username: "Bridge"}
	var err error
	if threadID != "" {
		_, err = relayClient.WebhookThreadExecute(portal.RelayWebhookID, portal.RelayWebhookSecret, true, threadID, params)
	} else {
		_, err = relayClient.WebhookExecute(portal.RelayWebhookID, portal.RelayWebhookSecret, true, params)
	}
	if err != nil {
		portal.log.Err(err).Str("message_id", msg.DiscordID).Msg("Failed to post reaction mirror error via Discord relay webhook")
	}
}

func (portal *Portal) refreshReactionMirror(msg *database.Message) {
	botUser := portal.reactionMirrorBotUser()
	if botUser == nil || botUser.Session == nil {
		portal.log.Warn().Str("message_id", msg.DiscordID).Msg("Skipping reaction mirror refresh: no logged-in Discord session")
		portal.notifyReactionMirrorError(msg, nil, "no logged-in Discord session")
		return
	}
	ctx := context.Background()
	client := portal.MainIntent().Client
	if client == nil && portal.bridge.Bot != nil {
		client = portal.bridge.Bot.Client
	}
	if client == nil {
		portal.log.Warn().Str("message_id", msg.DiscordID).Msg("Skipping reaction mirror refresh: no Matrix client")
		portal.notifyReactionMirrorError(msg, nil, "no Matrix client")
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
	lookup := func(ctx context.Context, roomID id.RoomID, userID id.UserID) string {
		return portal.matrixMemberDisplayName(ctx, userID)
	}
	state := portal.bridge.DB.ReactionMirror.GetByMessage(portal.Key, msg.DiscordID)
	if state == nil {
		state = portal.bridge.DB.ReactionMirror.New()
		state.Channel = portal.Key
		state.MessageID = msg.DiscordID
		state.TargetMXID = msg.MXID
	}
	aggregated, err := reactionmirror.AggregateReactions(ctx, client, portal.MXID, msg.MXID, lookup, nil, nil)
	if err != nil {
		portal.log.Err(err).Str("message_id", msg.DiscordID).Msg("Failed to aggregate reactions for mirror summary")
		portal.notifyReactionMirrorError(msg, state, fmt.Sprintf("aggregate reactions: %v", err))
		return
	}

	events, err := reactionmirror.FetchAnnotationRelations(ctx, client, portal.MXID, msg.MXID)
	if err != nil {
		portal.log.Err(err).Msg("Failed to fetch reaction relations")
		portal.notifyReactionMirrorError(msg, state, fmt.Sprintf("fetch reaction relations: %v", err))
		return
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
		count := mirroredEmojiReactorCount(aggregated, state.MirroredEmoji)
		if count > 0 {
			_ = botUser.Session.MessageReactionAddUser(portal.GuildID, msg.DiscordProtoChannelID(), msg.DiscordID, state.MirroredEmoji)
		} else {
			_ = botUser.Session.MessageReactionRemoveUser(portal.GuildID, msg.DiscordProtoChannelID(), msg.DiscordID, state.MirroredEmoji, botUser.DiscordID)
		}
	}

	summaryBody := reactionmirror.FormatSummary(aggregated)
	threadID, summaryMsgID, err := portal.ensureReactionMirrorThread(botUser, msg, state, summaryBody)
	if err != nil {
		portal.log.Err(err).Msg("Failed to update reaction mirror summary thread")
		portal.notifyReactionMirrorError(msg, state, fmt.Sprintf("update summary thread: %v", err))
		return
	}
	state.SummaryThreadID = threadID
	state.SummaryMessageID = summaryMsgID
	state.TargetMXID = msg.MXID
	state.Upsert()
}

func (portal *Portal) ensureReactionMirrorThread(botUser *User, msg *database.Message, state *database.ReactionMirrorState, body string) (threadID, summaryMsgID string, err error) {
	threadID = portal.resolveReactionMirrorThreadID(botUser, msg, state)
	if threadID == "" {
		ch, err := botUser.Session.MessageThreadStartComplex(portal.Key.ChannelID, msg.DiscordID, &discordgo.ThreadStart{
			Name:                "Reactions",
			AutoArchiveDuration: 24 * 60,
			Type:                discordgo.ChannelTypeGuildPublicThread,
			Location:            "Message",
		}, portal.RefererOptIfUser(botUser.Session, "")...)
		if err != nil {
			if recovered := portal.recoverReactionMirrorThreadID(botUser, msg, err); recovered != "" {
				threadID = recovered
			} else {
				return "", "", fmt.Errorf("start reaction thread: %w", err)
			}
		} else {
			threadID = ch.ID
		}
	}
	state.SummaryThreadID = threadID

	summaryMsgID = state.SummaryMessageID
	if summaryMsgID == "" {
		summaryMsgID = portal.findReactionSummaryMessage(botUser, threadID)
	}
	if summaryMsgID == "" {
		sent, err := botUser.Session.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{
			Content: body,
		}, portal.RefererOptIfUser(botUser.Session, threadID)...)
		if err != nil {
			return threadID, "", fmt.Errorf("post reaction summary: %w", err)
		}
		return threadID, sent.ID, nil
	}
	_, err = botUser.Session.ChannelMessageEditComplex(&discordgo.MessageEdit{
		Channel: threadID,
		ID:      summaryMsgID,
		Content: &body,
	}, portal.RefererOptIfUser(botUser.Session, threadID)...)
	if err != nil {
		if found := portal.findReactionSummaryMessage(botUser, threadID); found != "" && found != summaryMsgID {
			summaryMsgID = found
			_, err = botUser.Session.ChannelMessageEditComplex(&discordgo.MessageEdit{
				Channel: threadID,
				ID:      summaryMsgID,
				Content: &body,
			}, portal.RefererOptIfUser(botUser.Session, threadID)...)
		}
	}
	if err != nil {
		sent, sendErr := botUser.Session.ChannelMessageSendComplex(threadID, &discordgo.MessageSend{
			Content: body,
		}, portal.RefererOptIfUser(botUser.Session, threadID)...)
		if sendErr != nil {
			return threadID, summaryMsgID, fmt.Errorf("edit reaction summary: %w", err)
		}
		return threadID, sent.ID, nil
	}
	return threadID, summaryMsgID, nil
}

func (portal *Portal) resolveReactionMirrorThreadID(botUser *User, msg *database.Message, state *database.ReactionMirrorState) string {
	if state.SummaryThreadID != "" {
		return state.SummaryThreadID
	}
	if msg.ThreadID != "" {
		return msg.ThreadID
	}
	if dbThread := portal.bridge.DB.Thread.GetByMatrixRootMsg(msg.MXID); dbThread != nil {
		return dbThread.ID
	}
	return portal.fetchReactionMirrorThreadFromMessage(botUser, msg)
}

func (portal *Portal) recoverReactionMirrorThreadID(botUser *User, msg *database.Message, startErr error) string {
	var restErr *discordgo.RESTError
	if !errors.As(startErr, &restErr) || restErr.Message == nil || restErr.Message.Code != discordgo.ErrCodeThreadAlreadyCreatedForThisMessage {
		return ""
	}
	return portal.fetchReactionMirrorThreadFromMessage(botUser, msg)
}

func (portal *Portal) fetchReactionMirrorThreadFromMessage(botUser *User, msg *database.Message) string {
	dmsg, err := botUser.Session.ChannelMessage(portal.Key.ChannelID, msg.DiscordID, portal.RefererOptIfUser(botUser.Session, "")...)
	if err != nil || dmsg == nil || dmsg.Thread == nil {
		return ""
	}
	return dmsg.Thread.ID
}

func (portal *Portal) findReactionSummaryMessage(botUser *User, threadID string) string {
	messages, err := botUser.Session.ChannelMessages(threadID, 50, "", "", "", portal.RefererOptIfUser(botUser.Session, threadID)...)
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
	botUser := portal.reactionMirrorBotUser()
	if botUser == nil || msg.Author == nil || msg.Author.ID != botUser.DiscordID {
		return false
	}
	return portal.bridge.DB.ReactionMirror.IsMirrorThread(portal.Key, msg.ChannelID)
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
