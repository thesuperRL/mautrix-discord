// Patched into mautrix-discord as identity_mentions.go (package main).
package main

import (
	"strings"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/pkg/bridgeidentity"
)

func (portal *Portal) applyIdentityReplyMentions(
	content *event.MessageEventContent,
	allowed *discordgo.MessageAllowedMentions,
	replyToUser id.UserID,
) {
	if allowed == nil {
		return
	}

	if content.Mentions != nil {
		content.Mentions.UserIDs = bridgeidentity.DedupeLinkedMentions(content.Mentions.UserIDs)
	}

	if replyToUser != "" {
		if discordID := bridgeidentity.DiscordIDForMXID(replyToUser); discordID != "" {
			allowed.Users = appendIfNotContains(allowed.Users, discordID)
			allowed.RepliedUser = false
		}
	}

	if content.Mentions == nil {
		return
	}
	filtered := content.Mentions.UserIDs[:0]
	for _, userID := range content.Mentions.UserIDs {
		if bridgeidentity.IsDiscordBridgeBot(userID) || bridgeidentity.IsSlackBridgeBot(userID) {
			if discordID := bridgeidentity.DiscordIDForMXID(replyToUser); discordID != "" {
				allowed.Users = appendIfNotContains(allowed.Users, discordID)
				continue
			}
		}
		if discordID := bridgeidentity.DiscordIDForMXID(userID); discordID != "" {
			allowed.Users = appendIfNotContains(allowed.Users, discordID)
			filtered = append(filtered, userID)
			continue
		}
		filtered = append(filtered, userID)
	}
	content.Mentions.UserIDs = filtered
}

func (portal *Portal) discordSessionForMatrixSender(sender *User, senderMXID id.UserID) (*User, *discordgo.Session) {
	if sender.Session != nil {
		return sender, sender.Session
	}
	domain := portal.bridge.Config.Homeserver.Domain

	if lp, d, err := senderMXID.Parse(); err == nil && d == domain &&
		!bridgeidentity.IsDiscordBridgeBot(senderMXID) && !bridgeidentity.IsSlackBridgeBot(senderMXID) &&
		bridgeidentity.ParseDiscordGhostMXID(senderMXID) == "" && bridgeidentity.ParseSlackGhostUserID(senderMXID) == "" {
		if u := portal.bridge.GetConnectedUserByMXID(id.NewUserID(strings.ToLower(lp), d)); u != nil {
			return u, u.Session
		}
	}

	if discordID := bridgeidentity.DiscordIDForMXID(senderMXID); discordID != "" {
		if u := portal.bridge.GetConnectedUserByDiscordID(discordID); u != nil {
			return u, u.Session
		}
		portal.log.Warn().
			Str("sender", senderMXID.String()).
			Str("discord_id", discordID).
			Msg("Linked Discord user not connected; using relay webhook")
		return sender, nil
	}

	if slackUID := bridgeidentity.ParseSlackGhostUserID(senderMXID); slackUID != "" {
		identity := bridgeidentity.GetCached()
		localpart := identity.MatrixLocalpartForSlack(slackUID)
		portal.log.Info().
			Str("sender", senderMXID.String()).
			Str("slack_uid", slackUID).
			Str("matrix_localpart", localpart).
			Msg("Slack ghost outbound puppet lookup")
		if localpart != "" {
			if u := portal.bridge.GetConnectedUserByMXID(id.NewUserID(localpart, domain)); u != nil {
				return u, u.Session
			}
		}
		if u := portal.bridge.GetConnectedUserForSlackUID(slackUID); u != nil {
			return u, u.Session
		}
		portal.log.Warn().
			Str("sender", senderMXID.String()).
			Str("slack_uid", slackUID).
			Str("matrix_localpart", localpart).
			Msg("Slack ghost has no connected Discord session; using relay webhook")
	}
	return sender, nil
}
