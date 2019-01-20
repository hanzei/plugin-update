package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/google/go-github/github"
	"github.com/mattermost/mattermost-server/model"
	"github.com/mattermost/mattermost-server/plugin"
)

type Plugin struct {
	plugin.MattermostPlugin

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *configuration

	BotUserID string
}

const (
	botUsername    = "pluginupdate"
	botDisplayName = "Plugin Update"
	botDescription = "TODO"
)

func (p *Plugin) OnActivate() error {
	bot, appErr := p.API.GetUserByUsername(botUsername)

	if appErr != nil {
		if appErr.StatusCode != 404 {
			return appErr
		}
		newBot := &model.Bot{
			Username:    botUsername,
			DisplayName: botDisplayName,
			Description: botDescription,
		}
		rBot, appErr := p.API.CreateBot(newBot)
		if appErr != nil {
			return appErr
		}
		p.BotUserID = rBot.UserId
	} else {
		p.BotUserID = bot.Id
	}

	go func() {
		for {
			if err := p.CheckForUpdate(); err != nil {
				p.API.LogError("failed to check for update", "err", err.Error())
			}
			time.Sleep(1 * time.Minute)
		}
	}()

	return nil
}
func (p *Plugin) CheckForUpdate() error {
	config := p.getConfiguration()

	if config.Username == "" {
		return errors.New("you need to set a user")
	}

	notifiedUser, err := p.API.GetUserByUsername(config.Username)
	if err != nil {
		return err
	}

	p.API.LogDebug("checking for plugin updates")
	manifests, appErr := p.API.GetPlugins()
	if appErr != nil {
		return appErr
	}

	for _, manifest := range manifests {
		p.API.LogDebug("checking for updates", "id", manifest.Id)
		repositoryURL, ok := manifest.Props["repository"].(string)
		if !ok || repositoryURL == "" {
			continue
		}

		strippedRepositoryURL := strings.TrimPrefix(repositoryURL, "https://")
		strippedRepositoryURL = strings.TrimPrefix(strippedRepositoryURL, "github.com/")

		tmp := strings.Split(strippedRepositoryURL, "/")
		if len(tmp) != 2 {
			return fmt.Errorf("failed to extract username and repository from URL %s", repositoryURL)
		}

		client := github.NewClient(nil)
		release, _, err := client.Repositories.GetLatestRelease(context.Background(), tmp[0], tmp[1])
		if err != nil {
			return err
		}
		tag := release.GetTagName()
		currentVersion := semver.MustParse(manifest.Version)
		newVersion := semver.MustParse(strings.TrimPrefix(tag, "v"))
		if newVersion.LT(currentVersion) {
			p.API.LogDebug("no need to update.", "id", manifest.Id)
			continue
		}
		p.API.LogDebug("need to update.", "id", manifest.Id)

		lastNotificationVersionBytes, appErr := p.API.KVGet(manifest.Id)
		if appErr != nil {
			return appErr
		}
		if lastNotificationVersionBytes != nil {
			lastNotificationVersion := semver.MustParse(string(lastNotificationVersionBytes))

			if newVersion.LE(lastNotificationVersion) {
				p.API.LogDebug("no need to post an notification. We allready send one for this release.", "id", manifest.Id)
				continue
			}
		}
		p.API.LogDebug("we need to post an notification.", "id", manifest.Id)

		channel, appErr := p.API.GetDirectChannel(p.BotUserID, notifiedUser.Id)
		if appErr != nil {
			return appErr
		}

		_ = p.API.KVSet(manifest.Id, []byte(newVersion.String()))

		updateURL := repositoryURL + "/releases/tag/" + tag
		post := &model.Post{
			UserId:    p.BotUserID,
			ChannelId: channel.Id,
			Message:   fmt.Sprintf("%s can be updates to Version %s. [Press here to jump to the release](%s)", manifest.Name, newVersion.String(), updateURL),
		}
		if _, appErr = p.API.CreatePost(post); appErr != nil {
			return appErr
		}
	}
	return nil
}
