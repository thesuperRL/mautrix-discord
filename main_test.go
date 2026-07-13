// mautrix-discord - A Matrix-Discord puppeting bridge.
// Copyright (C) 2022 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"maunium.net/go/maulogger/v2"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-discord/config"
	"go.mau.fi/mautrix-discord/database"
)

func newTestUser(mxid id.UserID, discordID string, loggedIn bool) *User {
	dbUser := &database.User{MXID: mxid, DiscordID: discordID}
	if loggedIn {
		dbUser.DiscordToken = "token"
	}
	return &User{
		User:    dbUser,
		Session: &discordgo.Session{},
	}
}

func newTestBridge(defaultRelays []string, users ...*User) *DiscordBridge {
	br := &DiscordBridge{
		Config:      &config.Config{},
		usersByMXID: map[id.UserID]*User{},
	}
	br.Log = maulogger.Create()
	br.Config.Bridge.Relay.DefaultRelays = defaultRelays
	for _, u := range users {
		br.usersByMXID[u.MXID] = u
	}
	return br
}

func TestGetLoggedInUserForPortal_PrefersConfiguredMXIDRelay(t *testing.T) {
	relay := newTestUser("@reconciler:example.com", "1", true)
	other := newTestUser("@someone:example.com", "2", true)
	br := newTestBridge([]string{"@reconciler:example.com"}, relay, other)

	got := br.getLoggedInUserForPortal()
	if got != relay {
		t.Fatalf("expected configured relay user to be picked, got %v", got)
	}
}

func TestGetLoggedInUserForPortal_PrefersConfiguredDiscordIDRelay(t *testing.T) {
	relay := newTestUser("@reconciler:example.com", "123456789", true)
	other := newTestUser("@someone:example.com", "2", true)
	br := newTestBridge([]string{"123456789"}, relay, other)

	got := br.getLoggedInUserForPortal()
	if got != relay {
		t.Fatalf("expected configured relay user to be picked by DiscordID, got %v", got)
	}
}

func TestGetLoggedInUserForPortal_FallsBackWhenRelayNotLoggedIn(t *testing.T) {
	relay := newTestUser("@reconciler:example.com", "1", false)
	other := newTestUser("@someone:example.com", "2", true)
	br := newTestBridge([]string{"@reconciler:example.com"}, relay, other)

	got := br.getLoggedInUserForPortal()
	if got != other {
		t.Fatalf("expected fallback to logged-in user, got %v", got)
	}
}

func TestGetLoggedInUserForPortal_NoRelayConfigured(t *testing.T) {
	other := newTestUser("@someone:example.com", "2", true)
	br := newTestBridge(nil, other)

	got := br.getLoggedInUserForPortal()
	if got != other {
		t.Fatalf("expected the only logged-in user to be picked, got %v", got)
	}
}

func TestGetLoggedInUserForPortal_NoneLoggedIn(t *testing.T) {
	other := newTestUser("@someone:example.com", "2", false)
	br := newTestBridge(nil, other)

	if got := br.getLoggedInUserForPortal(); got != nil {
		t.Fatalf("expected nil when nobody is logged in, got %v", got)
	}
}
