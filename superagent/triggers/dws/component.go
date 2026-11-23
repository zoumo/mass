package dws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/trigger"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

type Component struct{ config dwsConfig }

func Load(path string, section io.Reader, coreEndpoint string) (Component, error) {
	c, err := loadDWSConfig(path, section, coreEndpoint)
	return Component{config: c}, err
}

func (c Component) Describe() trigger.Descriptor {
	d := c.config
	paths := trigger.Descriptor{
		Source: d.Source, AccountID: d.AccountID, InstanceID: d.InstanceID,
		CoreTokenEnv: d.TokenEnv, StatusListen: d.Listen, DatabasePath: d.DatabasePath,
		OutboxConsumer: d.repliesEnabled(),
	}
	if d.Feedback != nil {
		paths.FeedbackPath = d.Feedback.StatePath
	}
	return paths
}

func (c Component) Check(ctx context.Context, out io.Writer) error {
	paths := c.Describe()
	if err := triggerdb.CheckDestination(paths.DatabasePath, dwsDatabaseIdentity(c.config)); err != nil {
		return err
	}
	if paths.FeedbackPath != "" {
		if err := triggerdb.CheckParent(paths.FeedbackPath); err != nil {
			return fmt.Errorf("feedback.statePath: %w", err)
		}
		info, err := os.Stat(paths.FeedbackPath)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || !info.Mode().IsRegular()) {
			return errors.New("feedback.statePath must be a regular file")
		}
	}
	_, version, err := c.preflight(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"status": "contract_checked", "version": version, "profileExplicit": true, "liveSubscriptionStarted": false,
	})
}

func (c Component) Serve(ctx context.Context, out io.Writer) error {
	core, _, err := c.preflight(ctx)
	if err != nil {
		return err
	}
	return run(ctx, c.config, core, out)
}

func (c Component) preflight(ctx context.Context) (*coreclient.Client, string, error) {
	d := c.config
	core, err := coreclient.New(d.CoreEndpoint, time.Duration(d.RequestMillis)*time.Millisecond, os.Getenv(d.TokenEnv))
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	version, err := preflight(ctx, d)
	return core, version, err
}
