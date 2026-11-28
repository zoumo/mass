package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

type Component struct{ config githubConfig }

func Load(path string, section io.Reader, coreEndpoint string) (Component, error) {
	c, err := loadGitHubConfig(path, section, coreEndpoint)
	return Component{config: c}, err
}

func (c Component) Describe() trigger.Descriptor {
	g := c.config
	return trigger.Descriptor{
		Source: githubSource, AccountID: g.AccountID, InstanceID: g.InstanceID,
		CoreTokenEnv: g.TokenEnv, UpstreamTokenEnv: g.GitHubTokenEnv,
		StatusListen: g.Listen, DatabasePath: g.DatabasePath,
	}
}

func (c Component) Check(ctx context.Context, out io.Writer) error {
	if err := triggerdb.CheckDestination(c.config.DatabasePath, githubDatabaseIdentity(c.config)); err != nil {
		return err
	}
	api, _, err := c.clients()
	if err != nil {
		return err
	}
	if _, _, _, err := api.notificationPage(ctx, time.Time{}, 1, 1); err != nil {
		return fmt.Errorf("cannot read GitHub Notifications (classic PAT with notifications or repo scope): %w", err)
	}
	return json.NewEncoder(out).Encode(map[string]string{"status": "notifications_checked"})
}

func (c Component) Serve(ctx context.Context, out io.Writer) error {
	api, core, err := c.clients()
	if err != nil {
		return err
	}
	return runGitHub(ctx, c.config, api, core, out)
}

func (c Component) clients() (*githubAPI, *coreclient.Client, error) {
	g := c.config
	token, coreToken := os.Getenv(g.GitHubTokenEnv), os.Getenv(g.TokenEnv)
	if token == "" || strings.ContainsAny(token, " \t\r\n") || token == coreToken {
		return nil, nil, errors.New("GitHub credential reference must resolve to a non-whitespace token distinct from the Core token")
	}
	timeout := time.Duration(g.RequestMillis) * time.Millisecond
	core, err := coreclient.New(g.CoreEndpoint, timeout, coreToken, githubSource, g.AccountID, g.InstanceID)
	if err != nil {
		return nil, nil, err
	}
	return newGitHubAPI(githubEndpoint(), token, timeout), core, nil
}
